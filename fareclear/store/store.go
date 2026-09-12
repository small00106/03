// Package store 是 fareclear 的 PostgreSQL 持久化层。
// 全部使用 pgx + 手写 SQL，不引入 ORM。
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"fareclear/fare"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store 包装连接池。Loc 为计价归属日的业务时区。
type Store struct {
	pool *pgxpool.Pool
	loc  *time.Location
}

// Open 建立连接池并验证连通性。
func Open(ctx context.Context, connString string, loc *time.Location) (*Store, error) {
	if loc == nil {
		loc = time.Local
	}
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool, loc: loc}, nil
}

func (s *Store) Close() { s.pool.Close() }

// StationID 按车站代码查物理站 ID。
func (s *Store) StationID(ctx context.Context, code string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM stations WHERE code=$1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: station %q: %w", code, err)
	}
	return id, nil
}

// TicketTypeID 按票种代码查 ID。
func (s *Store) TicketTypeID(ctx context.Context, code string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM ticket_types WHERE code=$1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: ticket type %q: %w", code, err)
	}
	return id, nil
}

// Migrate 按文件名顺序执行内嵌 SQL，迁移轨迹记录在 schema_migrations。
// 每个文件在一个事务内执行，已应用的文件不会重复执行。
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename=$1)`, name,
		).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store: apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations(filename) VALUES($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// LoadGraph 读取全部区间构造无向图。区间未单独指定运营方时继承线路运营方。
func (s *Store) LoadGraph(ctx context.Context) (*fare.Graph, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seg.id, seg.line_id, COALESCE(seg.operator_id, l.operator_id) AS operator_id,
		       seg.from_station_id, seg.to_station_id, seg.distance_m
		FROM segments seg
		JOIN lines l ON l.id = seg.line_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var segs []fare.SegmentInput
	for rows.Next() {
		var in fare.SegmentInput
		if err := rows.Scan(&in.SegmentID, &in.LineID, &in.OperatorID,
			&in.FromID, &in.ToID, &in.DistanceM); err != nil {
			return nil, err
		}
		segs = append(segs, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return fare.BuildGraph(segs), nil
}

// LoadRuleBook 读取全部计价规则版本及其分段，交由内核做区间不重叠校验。
func (s *Store) LoadRuleBook(ctx context.Context) (*fare.RuleBook, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.id, v.code, v.valid_from, v.valid_until, v.cap_fare_cents,
		       b.ordinal, b.from_distance_m, b.to_distance_m,
		       b.base_fare_cents, b.step_distance_m, b.step_fare_cents
		FROM fare_rule_versions v
		JOIN fare_bands b ON b.rule_version_id = v.id
		ORDER BY v.valid_from, b.ordinal`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	versions := make(map[int64]*fare.RuleVersion)
	var order []int64
	for rows.Next() {
		var (
			v                    fare.RuleVersion
			b                    fare.Band
			validFrom            time.Time
			validUntil           *time.Time
			toDistance, stepDist *int
		)
		if err := rows.Scan(&v.ID, &v.Code, &validFrom, &validUntil, &v.CapFareCents,
			&b.Ordinal, &b.FromDistanceM, &toDistance,
			&b.BaseFareCents, &stepDist, &b.StepFareCents); err != nil {
			return nil, err
		}
		v.ValidFrom = validFrom
		if validUntil != nil {
			vu := *validUntil
			v.ValidUntil = &vu
		}
		if toDistance != nil {
			b.ToDistanceM = *toDistance
		}
		if stepDist != nil {
			b.StepDistanceM = *stepDist
		}
		if _, ok := versions[v.ID]; !ok {
			vv := v
			versions[v.ID] = &vv
			order = append(order, v.ID)
		}
		versions[v.ID].Bands = append(versions[v.ID].Bands, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	vs := make([]fare.RuleVersion, 0, len(order))
	for _, id := range order {
		vs = append(vs, *versions[id])
	}
	return fare.NewRuleBook(vs)
}

// NewEngine 用当前线网与规则快照构造计价引擎。
func (s *Store) NewEngine(ctx context.Context) (*fare.Engine, error) {
	g, err := s.LoadGraph(ctx)
	if err != nil {
		return nil, err
	}
	book, err := s.LoadRuleBook(ctx)
	if err != nil {
		return nil, err
	}
	return fare.NewEngine(g, book, s.loc), nil
}

// GateEventInput 落库一条 AFC 原始事件。
type GateEventInput struct {
	CardNo       string
	TicketTypeID int64
	Time         time.Time
	Direction    fare.Direction
	StationID    int64
	GateCode     string
}

// ErrTicketTypeInactive 票种已停用，不得再用于新产生的闸机事件。
// 停用前已落库的事件与在途行程不受影响：计价只认 fare_kind/discount_bp，
// 不读 active，否则跨天在途会因票种停用而无法计价。
var ErrTicketTypeInactive = errors.New("store: ticket type is inactive")

// ErrUnknownTicketType 票种 id 不存在。
var ErrUnknownTicketType = errors.New("store: unknown ticket type")

// InsertGateEvents 批量写入闸机事件并返回带库内 ID 与完整票种信息的事件，
// 返回值可直接交给 fare.PairEvents / Engine.PricePair 计价。
// 停用票种在此写入边界被拒绝（ErrTicketTypeInactive）。
func (s *Store) InsertGateEvents(ctx context.Context, in []GateEventInput) ([]fare.GateEvent, error) {
	if len(in) == 0 {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// 一次性取本批涉及的票种（含 active 校验）。
	idSet := make(map[int64]struct{})
	ids := make([]int64, 0, len(in))
	for _, e := range in {
		if _, ok := idSet[e.TicketTypeID]; !ok {
			idSet[e.TicketTypeID] = struct{}{}
			ids = append(ids, e.TicketTypeID)
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT id, code, name, fare_kind, discount_bp, active
		FROM ticket_types WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	tickets := make(map[int64]fare.TicketType, len(ids))
	for rows.Next() {
		var tt fare.TicketType
		var kind string
		var active bool
		if err := rows.Scan(&tt.ID, &tt.Code, &tt.Name, &kind, &tt.DiscountBp, &active); err != nil {
			rows.Close()
			return nil, err
		}
		tt.Kind = fare.TicketKind(kind)
		tt.Active = active
		tickets[tt.ID] = tt
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		tt, ok := tickets[id]
		if !ok {
			return nil, fmt.Errorf("%w: id=%d", ErrUnknownTicketType, id)
		}
		if !tt.Active {
			return nil, fmt.Errorf("%w: %s", ErrTicketTypeInactive, tt.Code)
		}
	}

	out := make([]fare.GateEvent, 0, len(in))
	for _, e := range in {
		var ge fare.GateEvent
		err := tx.QueryRow(ctx, `
			INSERT INTO gate_events(card_no, ticket_type_id, event_time, direction, station_id, gate_code)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`,
			e.CardNo, e.TicketTypeID, e.Time.UTC(), string(e.Direction), e.StationID, e.GateCode,
		).Scan(&ge.ID)
		if err != nil {
			return nil, err
		}
		ge.CardNo = e.CardNo
		ge.Ticket = tickets[e.TicketTypeID] // 关键：回填完整票种，否则免费/折扣票会被按全价计
		ge.Time = e.Time.UTC()
		ge.Direction = e.Direction
		ge.StationID = e.StationID
		ge.GateCode = e.GateCode
		out = append(out, ge)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// TripRecord trips 表一行（含库内 ID）。
type TripRecord struct {
	fare.Quote
	ID                   int64
	CardNo               string
	Ticket               fare.TicketType
	EnterEventID         int64
	ExitEventID          int64
	OriginStationID      int64
	DestinationStationID int64
	EnterTime            time.Time
	ExitTime             time.Time
	TravelDate           time.Time
	Status               string
}

// ErrUnpairedEvents 表示该卡存在真正无法配对的异常事件
// （无进站的出站、被新进站冲掉的旧进站）。在途未闭合的进站不在此列。
var ErrUnpairedEvents = errors.New("store: unpaired gate events")

// ProcessCard 对一张卡尚未入账的事件做配对、计价并落库。
// 整批在一个事务内完成：事件行 FOR UPDATE 锁定，行程只追加不改写；
// 已被 trips 引用的事件不会重复计价。跨零点在途天然支持：昨晚的进站
// 尚未被引用，今晨出站到达后两者会在同一次查询中被捞出配对，
// 行程 travel_date 仍取进站当天。
//
// openEnters 为截至目前仍未闭合的进站（乘客在途，次日出站再配对），
// 属正常状态、不构成错误；anomalies 为真正无法配对的事件（无进站的
// 出站、被新进站冲掉的旧进站），同时以 ErrUnpairedEvents 返回。
func (s *Store) ProcessCard(ctx context.Context, cardNo string, eng *fare.Engine) (created []TripRecord, openEnters []fare.GateEvent, anomalies []fare.GateEvent, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT ge.id, ge.card_no,
		       tt.id, tt.code, tt.name, tt.fare_kind, tt.discount_bp,
		       ge.event_time, ge.direction, ge.station_id, ge.gate_code
		FROM gate_events ge
		JOIN ticket_types tt ON tt.id = ge.ticket_type_id
		WHERE ge.card_no = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM trips t
		      WHERE t.enter_event_id = ge.id OR t.exit_event_id = ge.id)
		ORDER BY ge.event_time, ge.id
		FOR UPDATE OF ge`, cardNo)
	if err != nil {
		return nil, nil, nil, err
	}
	var events []fare.GateEvent
	for rows.Next() {
		var ge fare.GateEvent
		var kind, direction string
		if err := rows.Scan(&ge.ID, &ge.CardNo,
			&ge.Ticket.ID, &ge.Ticket.Code, &ge.Ticket.Name, &kind, &ge.Ticket.DiscountBp,
			&ge.Time, &direction, &ge.StationID, &ge.GateCode); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		ge.Ticket.Kind = fare.TicketKind(kind)
		ge.Direction = fare.Direction(direction)
		events = append(events, ge)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}

	pairs, open, bad := fare.PairEvents(events)
	for _, p := range pairs {
		q, perr := eng.PricePair(p)
		if perr != nil {
			return nil, nil, nil, fmt.Errorf("store: price trip card=%s: %w", cardNo, perr)
		}
		rec, ierr := s.insertTrip(ctx, tx, p, q)
		if ierr != nil {
			return nil, nil, nil, ierr
		}
		created = append(created, rec)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, nil, err
	}
	if len(bad) > 0 {
		return created, open, bad, fmt.Errorf("%w: %d event(s)", ErrUnpairedEvents, len(bad))
	}
	return created, open, nil, nil
}

func (s *Store) insertTrip(ctx context.Context, tx pgx.Tx, p fare.PairedTrip, q fare.Quote) (TripRecord, error) {
	travelDate := fare.TravelDate(p.Enter.Time, s.loc)
	pathIDs := make([]int64, 0, len(q.Path))
	for _, e := range q.Path {
		pathIDs = append(pathIDs, e.SegmentID)
	}

	var rec TripRecord
	err := tx.QueryRow(ctx, `
		INSERT INTO trips (
		    card_no, ticket_type_id, enter_event_id, exit_event_id,
		    origin_station_id, destination_station_id,
		    enter_time, exit_time, travel_date,
		    distance_m, path_segment_ids, fare_rule_version_id,
		    full_fare_cents, paid_fare_cents, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'priced')
		RETURNING id, status`,
		p.Enter.CardNo, p.Enter.Ticket.ID, p.Enter.ID, p.Exit.ID,
		p.Enter.StationID, p.Exit.StationID,
		p.Enter.Time.UTC(), p.Exit.Time.UTC(), travelDate,
		q.DistanceM, pathIDs, q.Version.ID,
		q.FullFareCents, q.PaidFareCents,
	).Scan(&rec.ID, &rec.Status)
	if err != nil {
		return rec, err
	}

	for i, edge := range q.Path {
		if _, err := tx.Exec(ctx, `
			INSERT INTO trip_legs(trip_id, ordinal, segment_id, line_id, operator_id, distance_m)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			rec.ID, i+1, edge.SegmentID, edge.LineID, edge.OperatorID, edge.DistanceM); err != nil {
			return rec, err
		}
	}

	rec.Quote = q
	rec.CardNo = p.Enter.CardNo
	rec.Ticket = p.Enter.Ticket
	rec.EnterEventID = p.Enter.ID
	rec.ExitEventID = p.Exit.ID
	rec.OriginStationID = p.Enter.StationID
	rec.DestinationStationID = p.Exit.StationID
	rec.EnterTime = p.Enter.Time
	rec.ExitTime = p.Exit.Time
	rec.TravelDate = travelDate
	return rec, nil
}
