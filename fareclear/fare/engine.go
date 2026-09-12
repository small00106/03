package fare

import (
	"errors"
	"sort"
	"time"
)

// Engine 计价引擎：持有线网图快照、规则版本集与业务时区，
// 给定行程起讫站、票种和进站时刻即可产出计价快照。
type Engine struct {
	graph *Graph
	book  *RuleBook
	loc   *time.Location
}

// NewEngine 构造引擎；loc 为计价归属日所用业务时区，nil 取进程本地时区。
func NewEngine(g *Graph, book *RuleBook, loc *time.Location) *Engine {
	if g == nil {
		g = NewGraph()
	}
	if loc == nil {
		loc = time.Local
	}
	return &Engine{graph: g, book: book, loc: loc}
}

// Price 对一趟行程计价。版本按进站时刻的业务日（TravelDate）选取，
// 因此跨零点出站不会切换到次日生效的新版本。
func (e *Engine) Price(origin, destination int64, ticket TicketType, enterTime time.Time) (Quote, error) {
	if e.book == nil {
		return Quote{}, errors.New("fare: engine has no rule book")
	}
	path, distance, err := e.graph.ShortestPath(origin, destination)
	if err != nil {
		return Quote{}, err
	}

	travelDate := TravelDate(enterTime, e.loc)
	version, err := e.book.VersionAt(travelDate)
	if err != nil {
		return Quote{}, err
	}

	full, err := BaseFare(version, distance)
	if err != nil {
		return Quote{}, err
	}
	return Quote{
		Version:       version,
		DistanceM:     distance,
		Path:          path,
		FullFareCents: full,
		PaidFareCents: PaidFare(full, ticket),
	}, nil
}

// PricePair 对已配对的进出站行程计价，并校验票卡与时间方向。
func (e *Engine) PricePair(t PairedTrip) (Quote, error) {
	if t.Enter.CardNo != t.Exit.CardNo {
		return Quote{}, errors.New("fare: pair card mismatch")
	}
	if !t.Exit.Time.After(t.Enter.Time) {
		return Quote{}, errors.New("fare: exit not after enter")
	}
	return e.Price(t.Enter.StationID, t.Exit.StationID, t.Enter.Ticket, t.Enter.Time)
}

// PairEvents 把原始闸机事件按时间配对成行程（不同卡互不干扰）。
// 配对规则（无状态 FIFO）：
//   - 进站后第一次出站配成一对；
//   - 连续两次进站：前一次进站无出站，记为异常，以新进站为准；
//   - 没有待配对进站时收到出站：记为异常。
//
// 返回成功配对与未能配对的事件（异常），顺序均保持时间序。
func PairEvents(events []GateEvent) (trips []PairedTrip, unmatched []GateEvent) {
	sorted := make([]GateEvent, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].Time.Equal(sorted[j].Time) {
			return sorted[i].Time.Before(sorted[j].Time)
		}
		return sorted[i].ID < sorted[j].ID
	})

	pending := make(map[string]*GateEvent)
	var unmatchedOrder []GateEvent
	for i := range sorted {
		ev := sorted[i]
		switch ev.Direction {
		case Enter:
			if p := pending[ev.CardNo]; p != nil {
				unmatchedOrder = append(unmatchedOrder, *p)
			}
			p := ev
			pending[ev.CardNo] = &p
		case Exit:
			p := pending[ev.CardNo]
			if p == nil {
				unmatchedOrder = append(unmatchedOrder, ev)
				continue
			}
			trips = append(trips, PairedTrip{Enter: *p, Exit: ev})
			pending[ev.CardNo] = nil
		}
	}
	// 收集仍未闭合的进站，按事件时间输出。
	for _, p := range pending {
		if p != nil {
			unmatchedOrder = append(unmatchedOrder, *p)
		}
	}
	sort.SliceStable(unmatchedOrder, func(i, j int) bool {
		if !unmatchedOrder[i].Time.Equal(unmatchedOrder[j].Time) {
			return unmatchedOrder[i].Time.Before(unmatchedOrder[j].Time)
		}
		return unmatchedOrder[i].ID < unmatchedOrder[j].ID
	})
	sort.SliceStable(trips, func(i, j int) bool {
		return trips[i].Enter.Time.Before(trips[j].Enter.Time)
	})
	return trips, unmatchedOrder
}
