package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"fareclear/fare"
	"fareclear/store"
)

// dsn 从 FARECLEAR_TEST_DATABASE 读取（例如 postgres://...?sslmode=disable）。
// 未设置时跳过：计价内核的正确性由 fare 包单测保证，本测试只验证
// SQL 与内核在真实 PostgreSQL 上的端到端协作。
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("FARECLEAR_TEST_DATABASE")
	if dsn == "" {
		t.Skip("FARECLEAR_TEST_DATABASE 未设置，跳过数据库集成测试")
	}
	return dsn
}

// fixture 缓存整个测试共享的依赖。
type fixture struct {
	st      *store.Store
	admin   *pgxpool.Pool
	eng     *fare.Engine
	loc     *time.Location
	single  int64
	student int64
	senior  int64
	s1      int64
	s2      int64
	s6      int64
	s7      int64
}

func setupFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dsn := testDSN(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")

	st, err := store.Open(ctx, dsn, loc)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	eng, err := st.NewEngine(ctx)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	// 测试专用停用票种（不动种子里的 STUDENT，避免污染其他用例）。
	if _, err := admin.Exec(ctx, `
		INSERT INTO ticket_types(code, name, fare_kind, discount_bp, active)
		VALUES ('PROMO-DISABLED', '停用促销票', 'rate', 8000, FALSE)
		ON CONFLICT (code) DO NOTHING`); err != nil {
		t.Fatal(err)
	}

	mustID := func(code string, fn func(context.Context, string) (int64, error)) int64 {
		id, err := fn(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	return &fixture{
		st:      st,
		admin:   admin,
		eng:     eng,
		loc:     loc,
		single:  mustID("SINGLE", st.TicketTypeID),
		student: mustID("STUDENT", st.TicketTypeID),
		senior:  mustID("SENIOR", st.TicketTypeID),
		s1:      mustID("S1", st.StationID),
		s2:      mustID("S2", st.StationID),
		s6:      mustID("S6", st.StationID),
		s7:      mustID("S7", st.StationID),
	}
}

// card 生成带时间戳后缀的卡号，使测试可在同一库上重复执行。
func card(tag string) string {
	return "IT-" + tag + "-" + time.Now().Format("150405.000000")
}

func TestStoreEndToEnd(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	// 全价票，跨零点：2025-12-31 23:50 进、2026-01-01 00:30 出（北京时间）。
	enter := time.Date(2025, 12, 31, 23, 50, 0, 0, f.loc)
	exit := time.Date(2026, 1, 1, 0, 30, 0, 0, f.loc)

	c := card("FULL")
	if _, err := f.st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: c, TicketTypeID: f.single, Time: enter, Direction: fare.Enter, StationID: f.s1, GateCode: "G1"},
		{CardNo: c, TicketTypeID: f.single, Time: exit, Direction: fare.Exit, StationID: f.s7, GateCode: "G7"},
	}); err != nil {
		t.Fatalf("InsertGateEvents: %v", err)
	}

	trips, open, anomalies, err := f.st.ProcessCard(ctx, c, f.eng)
	if err != nil || len(open) != 0 || len(anomalies) != 0 {
		t.Fatalf("ProcessCard: err=%v open=%d anomalies=%d", err, len(open), len(anomalies))
	}
	if len(trips) != 1 {
		t.Fatalf("行程数 = %d, want 1", len(trips))
	}
	tr := trips[0]

	// S1->S7 = 32km：3 + 3 + ceil(16/6)=3 = 9 元（旧版，按进站日）。
	if tr.DistanceM != 32000 {
		t.Errorf("里程 = %d, want 32000", tr.DistanceM)
	}
	if tr.FullFareCents != 900 || tr.PaidFareCents != 900 {
		t.Errorf("全价票金额 full=%d paid=%d, want 900/900", tr.FullFareCents, tr.PaidFareCents)
	}
	if tr.TravelDate.Format("2006-01-02") != "2025-12-31" {
		t.Errorf("travel_date = %s, want 2025-12-31（按进站日，跨零点不切版本）", tr.TravelDate.Format("2006-01-02"))
	}
	if got := tr.Version.Code; got != "FARE-2024" {
		t.Errorf("规则版本 = %s, want FARE-2024", got)
	}
	if len(tr.Path) != 6 {
		t.Errorf("路径区间数 = %d, want 6（含两运营方各段）", len(tr.Path))
	}

	// 幂等：再处理一次不应产生重复行程。
	again, open2, anomalies2, err := f.st.ProcessCard(ctx, c, f.eng)
	if err != nil || len(open2) != 0 || len(anomalies2) != 0 {
		t.Fatalf("二次 ProcessCard: err=%v open=%d anomalies=%d", err, len(open2), len(anomalies2))
	}
	if len(again) != 0 {
		t.Errorf("重复入账 %d 笔，want 0（已引用事件不得重复计价）", len(again))
	}

	// 学生五折 + 敬老免费仍按原价落账：取 26km（S1->S6）= 8 元
	// （6 + ceil(10000/6000)*1 = 8 元）。
	for _, tc := range []struct {
		name     string
		ticketID int64
		wantPaid int64
		wantFull int64
	}{
		{"student", f.student, 400, 800},
		{"senior", f.senior, 0, 800},
	} {
		cc := card(tc.name)
		if _, err := f.st.InsertGateEvents(ctx, []store.GateEventInput{
			{CardNo: cc, TicketTypeID: tc.ticketID, Time: time.Date(2025, 6, 1, 9, 0, 0, 0, f.loc), Direction: fare.Enter, StationID: f.s1},
			{CardNo: cc, TicketTypeID: tc.ticketID, Time: time.Date(2025, 6, 1, 10, 0, 0, 0, f.loc), Direction: fare.Exit, StationID: f.s6},
		}); err != nil {
			t.Fatal(err)
		}
		ts, _, _, err := f.st.ProcessCard(ctx, cc, f.eng)
		if err != nil || len(ts) != 1 {
			t.Fatalf("%s: err=%v n=%d", tc.name, err, len(ts))
		}
		if ts[0].FullFareCents != tc.wantFull || ts[0].PaidFareCents != tc.wantPaid {
			t.Errorf("%s: full=%d paid=%d, want full=%d paid=%d",
				tc.name, ts[0].FullFareCents, ts[0].PaidFareCents, tc.wantFull, tc.wantPaid)
		}
	}

	// 真正的异常：只有出站，返回 ErrUnpairedEvents 且不产生行程。
	bad := card("BAD")
	if _, err := f.st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: bad, TicketTypeID: f.single, Time: time.Date(2025, 6, 2, 9, 0, 0, 0, f.loc), Direction: fare.Exit, StationID: f.s2},
	}); err != nil {
		t.Fatal(err)
	}
	_, _, badEvents, err := f.st.ProcessCard(ctx, bad, f.eng)
	if !errors.Is(err, store.ErrUnpairedEvents) || len(badEvents) != 1 {
		t.Errorf("异常配对: err=%v anomalies=%d, want ErrUnpairedEvents/1", err, len(badEvents))
	}
}

// TestStoreReturnedEventsCarryTicket 回归问题一：InsertGateEvents 的返回值
// 必须带完整票种。敬老卡事件直接 PairEvents + PricePair，
// 应与 ProcessCard 口径一致：full=800、paid=0，而不是被按全价收 8 元。
func TestStoreReturnedEventsCarryTicket(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	c := card("RET-SEN")
	events, err := f.st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: c, TicketTypeID: f.senior, Time: time.Date(2025, 6, 1, 9, 0, 0, 0, f.loc), Direction: fare.Enter, StationID: f.s1},
		{CardNo: c, TicketTypeID: f.senior, Time: time.Date(2025, 6, 1, 10, 0, 0, 0, f.loc), Direction: fare.Exit, StationID: f.s6},
	})
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Ticket.Kind != fare.KindFree {
		t.Fatalf("返回事件票种 Kind=%q, want free（回填缺失会导致敬老票被全价收取）", events[0].Ticket.Kind)
	}

	pairs, open, anomalies := fare.PairEvents(events)
	if len(pairs) != 1 || len(open) != 0 || len(anomalies) != 0 {
		t.Fatalf("配对 pairs=%d open=%d anomalies=%d", len(pairs), len(open), len(anomalies))
	}
	q, err := f.eng.PricePair(pairs[0])
	if err != nil {
		t.Fatal(err)
	}
	if q.FullFareCents != 800 || q.PaidFareCents != 0 {
		t.Errorf("直算敬老票 full=%d paid=%d, want 800/0", q.FullFareCents, q.PaidFareCents)
	}

	// 与 ProcessCard 口径必须完全一致。
	dbTrips, _, _, err := f.st.ProcessCard(ctx, c, f.eng)
	if err != nil || len(dbTrips) != 1 {
		t.Fatalf("ProcessCard: err=%v n=%d", err, len(dbTrips))
	}
	if dbTrips[0].FullFareCents != q.FullFareCents || dbTrips[0].PaidFareCents != q.PaidFareCents {
		t.Errorf("两条路径不一致: 直算 full=%d/%d vs 落库 full=%d/%d",
			q.FullFareCents, q.PaidFareCents, dbTrips[0].FullFareCents, dbTrips[0].PaidFareCents)
	}
}

// TestStoreSameStationChargesBaseFare 回归问题二：同站进出 5 分钟，
// 里程 0 但按起步价落账 full=paid=300。
func TestStoreSameStationChargesBaseFare(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	c := card("SAME")
	base := time.Date(2025, 6, 3, 8, 0, 0, 0, f.loc)
	if _, err := f.st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: c, TicketTypeID: f.single, Time: base, Direction: fare.Enter, StationID: f.s2},
		{CardNo: c, TicketTypeID: f.single, Time: base.Add(5 * time.Minute), Direction: fare.Exit, StationID: f.s2},
	}); err != nil {
		t.Fatal(err)
	}
	trips, _, _, err := f.st.ProcessCard(ctx, c, f.eng)
	if err != nil || len(trips) != 1 {
		t.Fatalf("ProcessCard: err=%v n=%d", err, len(trips))
	}
	tr := trips[0]
	if tr.DistanceM != 0 {
		t.Errorf("同站里程 = %d, want 0", tr.DistanceM)
	}
	if tr.FullFareCents != 300 || tr.PaidFareCents != 300 {
		t.Errorf("同站进出 full=%d paid=%d, want 300/300（起步价）", tr.FullFareCents, tr.PaidFareCents)
	}
	if n := len(tr.Path); n != 0 {
		t.Errorf("同站路径区间数 = %d, want 0", n)
	}
}

// TestStoreInTransitOvernight 回归问题三：当天一趟完整行程 + 23:55 的
// 在途进站，日终处理不得报错或报异常；次日凌晨出站后跨天配成同一趟，
// 归属日仍是进站当天。
func TestStoreInTransitOvernight(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()
	c := card("NIGHT")

	// 白天一趟已完成行程。
	if _, err := f.st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: c, TicketTypeID: f.single, Time: time.Date(2025, 6, 4, 8, 0, 0, 0, f.loc), Direction: fare.Enter, StationID: f.s1},
		{CardNo: c, TicketTypeID: f.single, Time: time.Date(2025, 6, 4, 8, 40, 0, 0, f.loc), Direction: fare.Exit, StationID: f.s2},
	}); err != nil {
		t.Fatal(err)
	}
	// 23:55 进站，人在车上，当晚没有出站。
	nightEnter := time.Date(2025, 6, 4, 23, 55, 0, 0, f.loc)
	if _, err := f.st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: c, TicketTypeID: f.single, Time: nightEnter, Direction: fare.Enter, StationID: f.s1},
	}); err != nil {
		t.Fatal(err)
	}

	// 日终批处理：1 笔完整行程入账，1 个在途进站，无错误无异常。
	trips, open, anomalies, err := f.st.ProcessCard(ctx, c, f.eng)
	if err != nil {
		t.Fatalf("日终在途不应报错: %v", err)
	}
	if len(trips) != 1 || trips[0].DistanceM != 4000 {
		t.Fatalf("日终行程 n=%d, want 1 笔 4km", len(trips))
	}
	if len(open) != 1 || open[0].Time.Sub(nightEnter) != 0 {
		t.Fatalf("在途进站 open=%v, want 23:55 那一笔", open)
	}
	if len(anomalies) != 0 {
		t.Fatalf("在途被误报异常 %d 笔", len(anomalies))
	}

	// 次日 00:30 出站到达：与昨晚进站配成同一趟。
	if _, err := f.st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: c, TicketTypeID: f.single, Time: time.Date(2025, 6, 5, 0, 30, 0, 0, f.loc), Direction: fare.Exit, StationID: f.s2},
	}); err != nil {
		t.Fatal(err)
	}
	trips2, open2, anomalies2, err := f.st.ProcessCard(ctx, c, f.eng)
	if err != nil || len(open2) != 0 || len(anomalies2) != 0 {
		t.Fatalf("次日配对: err=%v open=%d anomalies=%d", err, len(open2), len(anomalies2))
	}
	if len(trips2) != 1 {
		t.Fatalf("次日新增行程 n=%d, want 1", len(trips2))
	}
	night := trips2[0]
	if night.TravelDate.Format("2006-01-02") != "2025-06-04" {
		t.Errorf("跨天行程归属日 = %s, want 2025-06-04（进站日）", night.TravelDate.Format("2006-01-02"))
	}
	if night.FullFareCents != 300 {
		t.Errorf("跨天 4km 行程 full=%d, want 300", night.FullFareCents)
	}
}

// TestStoreInactiveTicketRejected 回归问题四：停用票种不得再写入新事件；
// 不存在的票种 id 同样拒绝。两条路径都不应留下 gate_events 行。
func TestStoreInactiveTicketRejected(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	var inactiveID int64
	if err := f.admin.QueryRow(ctx,
		`SELECT id FROM ticket_types WHERE code='PROMO-DISABLED'`).Scan(&inactiveID); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2025, 6, 5, 9, 0, 0, 0, f.loc)
	rejectedCard := card("INACTIVE")
	unknownCard := card("NOPE")

	_, err := f.st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: rejectedCard, TicketTypeID: inactiveID, Time: when, Direction: fare.Enter, StationID: f.s1},
	})
	if !errors.Is(err, store.ErrTicketTypeInactive) {
		t.Fatalf("停用票种写入: err=%v, want ErrTicketTypeInactive", err)
	}

	_, err = f.st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: unknownCard, TicketTypeID: 999999999, Time: when, Direction: fare.Enter, StationID: f.s1},
	})
	if !errors.Is(err, store.ErrUnknownTicketType) {
		t.Fatalf("不存在票种写入: err=%v, want ErrUnknownTicketType", err)
	}

	// 被拒事件不得落库（按本次卡号计数，与历史运行残留隔离）。
	var n int
	if err := f.admin.QueryRow(ctx,
		`SELECT count(*) FROM gate_events WHERE card_no = ANY($1)`,
		[]string{rejectedCard, unknownCard}).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("被拒事件残留 %d 行, want 0", n)
	}

	// 票种停用不影响其停用前已入账的历史行程：手工插一行旧事件，
	// 仍应能用该票种的 fare_kind/discount 正常计价。
	oldCard := card("OLD-PROMO")
	if _, err := f.admin.Exec(ctx, `
		INSERT INTO gate_events(card_no, ticket_type_id, event_time, direction, station_id)
		VALUES ($1, $2, $3, 'enter', $4), ($1, $2, $5, 'exit', $6)`,
		oldCard, inactiveID, time.Date(2025, 6, 1, 9, 0, 0, 0, f.loc), f.s1,
		time.Date(2025, 6, 1, 10, 0, 0, 0, f.loc), f.s6); err != nil {
		t.Fatal(err)
	}
	trips, _, _, perr := f.st.ProcessCard(ctx, oldCard, f.eng)
	if perr != nil || len(trips) != 1 {
		t.Fatalf("停用前历史事件应照常计价: err=%v n=%d", perr, len(trips))
	}
	// rate 8000bp（八折），26km 原价 800 → 实付 640。
	if trips[0].FullFareCents != 800 || trips[0].PaidFareCents != 640 {
		t.Errorf("历史行程 full=%d paid=%d, want 800/640", trips[0].FullFareCents, trips[0].PaidFareCents)
	}
}
