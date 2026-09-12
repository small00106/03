package fare

import (
	"testing"
	"time"
)

// buildDemoGraph 搭出与种子数据同构的线网：
//
//	L1: 1 --4km-- 2 --4km-- 3(换乘)
//	L2:                     3 --6km-- 4 --6km-- 5
func buildDemoGraph() *Graph {
	return BuildGraph([]SegmentInput{
		{SegmentID: 1, LineID: 1, OperatorID: 101, FromID: 1, ToID: 2, DistanceM: 4000},
		{SegmentID: 2, LineID: 1, OperatorID: 101, FromID: 2, ToID: 3, DistanceM: 4000},
		{SegmentID: 3, LineID: 2, OperatorID: 102, FromID: 3, ToID: 4, DistanceM: 6000},
		{SegmentID: 4, LineID: 2, OperatorID: 102, FromID: 4, ToID: 5, DistanceM: 6000},
	})
}

func TestShortestPathTransfer(t *testing.T) {
	g := buildDemoGraph()

	path, dist, err := g.ShortestPath(1, 5)
	if err != nil {
		t.Fatalf("ShortestPath: %v", err)
	}
	if dist != 20000 {
		t.Errorf("里程 = %d, want 20000", dist)
	}
	if len(path) != 4 {
		t.Fatalf("路径区间数 = %d, want 4", len(path))
	}
	wantSeg := []int64{1, 2, 3, 4}
	for i, e := range path {
		if e.SegmentID != wantSeg[i] {
			t.Errorf("path[%d] segment = %d, want %d", i, e.SegmentID, wantSeg[i])
		}
	}
	// 换乘站 3 是同一物理顶点：路径上不产生额外换乘里程，
	// 且清分 legs 同时包含两家运营方。
	ops := map[int64]bool{}
	for _, e := range path {
		ops[e.OperatorID] = true
	}
	if !ops[101] || !ops[102] {
		t.Errorf("路径运营方 = %v, want 101 与 102 都在", ops)
	}
}

func TestShortestPathUndirected(t *testing.T) {
	g := buildDemoGraph()
	// 无向：反向同样可达且里程相等。
	path, dist, err := g.ShortestPath(5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dist != 20000 || len(path) != 4 {
		t.Errorf("反向路径 dist=%d len=%d, want 20000/4", dist, len(path))
	}
	if path[0].SegmentID != 4 {
		t.Errorf("反向首段 = %d, want 4", path[0].SegmentID)
	}
}

func TestShortestPathSameStation(t *testing.T) {
	g := buildDemoGraph()
	path, dist, err := g.ShortestPath(2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if dist != 0 || len(path) != 0 {
		t.Errorf("同站进出 dist=%d len=%d, want 0/0", dist, len(path))
	}
}

func TestShortestPathErrors(t *testing.T) {
	g := buildDemoGraph()
	if _, _, err := g.ShortestPath(1, 99); err != ErrUnknownStation {
		t.Errorf("未知终点: got %v, want ErrUnknownStation", err)
	}
	// 孤立子图：6 自连 7，与主网不通。
	g2 := NewGraph()
	g2.AddEdge(6, 7, Edge{SegmentID: 9, DistanceM: 1000})
	g2.AddEdge(1, 2, Edge{SegmentID: 1, DistanceM: 1000})
	if _, _, err := g2.ShortestPath(1, 7); err == nil {
		t.Error("不连通站点之间应报错")
	}
}

// TestEngineFullTrip 端到端走一遍内核：S1->S5 = 20km，
// 原价 700 分（3 + 3 + 1），学生 350，敬老 0 但原价仍 700。
func TestEngineFullTrip(t *testing.T) {
	book, _ := NewRuleBook([]RuleVersion{rule2024()})
	eng := NewEngine(buildDemoGraph(), book, testTZ)
	enter := timeInTZ("2025-03-01T09:00:00+08:00")

	q, err := eng.Price(1, 5, ticketFull, enter)
	if err != nil {
		t.Fatal(err)
	}
	if q.DistanceM != 20000 || q.FullFareCents != 700 || q.PaidFareCents != 700 {
		t.Errorf("全价: dist=%d full=%d paid=%d, want 20000/700/700", q.DistanceM, q.FullFareCents, q.PaidFareCents)
	}

	qs, err := eng.Price(1, 5, ticketStudent, enter)
	if err != nil || qs.PaidFareCents != 350 || qs.FullFareCents != 700 {
		t.Errorf("学生票: full=%d paid=%d err=%v, want 700/350", qs.FullFareCents, qs.PaidFareCents, err)
	}

	qp, err := eng.Price(1, 5, ticketSenior, enter)
	if err != nil || qp.PaidFareCents != 0 || qp.FullFareCents != 700 {
		t.Errorf("敬老卡: full=%d paid=%d err=%v, want 700/0（原价仍落账）", qp.FullFareCents, qp.PaidFareCents, err)
	}
}

func TestPairEvents(t *testing.T) {
	base := time.Date(2025, 3, 1, 8, 0, 0, 0, testTZ)
	ev := func(id int64, card string, d Direction, station int64, minute int) GateEvent {
		return GateEvent{ID: id, CardNo: card, Direction: d, StationID: station,
			Ticket: ticketFull, Time: base.Add(time.Duration(minute) * time.Minute)}
	}

	// A 卡：正常一对 + 序列末尾一次未闭合进站（在途，非异常）；
	// B 卡：缺进站的出站（异常）+ 正常一对；两卡交错给出，验证不跨卡配对。
	events := []GateEvent{
		ev(1, "A", Enter, 1, 0),
		ev(2, "B", Exit, 5, 1), // B 无进站 -> 异常
		ev(3, "A", Exit, 5, 2), // A 正常配对 (1,3)
		ev(4, "B", Enter, 1, 3),
		ev(5, "A", Enter, 1, 4), // A 23:55 式在途进站，且再无出站
		ev(6, "B", Exit, 5, 5),  // B 正常配对 (4,6)
	}
	trips, openEnters, anomalies := PairEvents(events)

	if len(trips) != 2 {
		t.Fatalf("配对数 = %d, want 2", len(trips))
	}
	if trips[0].Enter.ID != 1 || trips[0].Exit.ID != 3 {
		t.Errorf("第一对 = (%d,%d), want (1,3)", trips[0].Enter.ID, trips[0].Exit.ID)
	}
	if trips[1].Enter.ID != 4 || trips[1].Exit.ID != 6 {
		t.Errorf("第二对 = (%d,%d), want (4,6)", trips[1].Enter.ID, trips[1].Exit.ID)
	}

	// 未闭合的 A 卡进站是在途，不得被报成异常。
	if len(openEnters) != 1 || openEnters[0].ID != 5 {
		t.Fatalf("在途进站 = %v, want 仅事件 5", openEnters)
	}
	if len(anomalies) != 1 || anomalies[0].ID != 2 {
		t.Fatalf("异常事件 = %v, want 仅事件 2（无进站的出站）", anomalies)
	}
}

// TestPairEventsSupersededEnter 连续两次进站：旧进站被冲掉算异常，
// 新进站与随后出站正常配对。
func TestPairEventsSupersededEnter(t *testing.T) {
	base := time.Date(2025, 3, 1, 8, 0, 0, 0, testTZ)
	ev := func(id int64, d Direction, minute int) GateEvent {
		return GateEvent{ID: id, CardNo: "A", Direction: d, StationID: 1,
			Ticket: ticketFull, Time: base.Add(time.Duration(minute) * time.Minute)}
	}
	trips, open, anomalies := PairEvents([]GateEvent{
		ev(1, Enter, 0), // 被下一次进站冲掉
		ev(2, Enter, 5),
		ev(3, Exit, 30),
	})
	if len(trips) != 1 || trips[0].Enter.ID != 2 || trips[0].Exit.ID != 3 {
		t.Fatalf("配对 = %+v, want (2,3)", trips)
	}
	if len(open) != 0 {
		t.Errorf("在途 = %v, want 空", open)
	}
	if len(anomalies) != 1 || anomalies[0].ID != 1 {
		t.Errorf("异常 = %v, want 仅事件 1", anomalies)
	}
}

// TestPairEventsCrossMidnight 跨零点的进出仍配成同一趟行程，
// 且配对完成后无在途、无异常。
func TestPairEventsCrossMidnightPairing(t *testing.T) {
	enter := GateEvent{ID: 1, CardNo: "A", Direction: Enter, StationID: 1,
		Ticket: ticketFull, Time: timeInTZ("2025-12-31T23:55:00+08:00")}
	exit := GateEvent{ID: 2, CardNo: "A", Direction: Exit, StationID: 2,
		Ticket: ticketFull, Time: timeInTZ("2026-01-01T00:30:00+08:00")}
	trips, open, anomalies := PairEvents([]GateEvent{enter, exit})
	if len(trips) != 1 || trips[0].Enter.ID != 1 || trips[0].Exit.ID != 2 {
		t.Fatalf("跨零点未配成一对: %+v", trips)
	}
	if len(open) != 0 || len(anomalies) != 0 {
		t.Errorf("跨零点配对后 open=%v anomalies=%v, 均应为空", open, anomalies)
	}
}

// TestSameStationTripChargesBaseFare 同站进出：里程 0，但按起步价收费，
// 全价 300、敬老卡实付 0 而原价仍 300。
func TestSameStationTripChargesBaseFare(t *testing.T) {
	book, _ := NewRuleBook([]RuleVersion{rule2024()})
	g := NewGraph()
	g.AddEdge(1, 2, Edge{SegmentID: 1, DistanceM: 4000}) // 1 号站需在网内
	eng := NewEngine(g, book, testTZ)
	enter := timeInTZ("2025-03-01T10:00:00+08:00")

	q, err := eng.Price(1, 1, ticketFull, enter)
	if err != nil {
		t.Fatal(err)
	}
	if q.DistanceM != 0 || len(q.Path) != 0 {
		t.Errorf("同站里程 = %d, 路径段 = %d, want 0/0", q.DistanceM, len(q.Path))
	}
	if q.FullFareCents != 300 || q.PaidFareCents != 300 {
		t.Errorf("同站全价 full=%d paid=%d, want 300/300（收起步价）", q.FullFareCents, q.PaidFareCents)
	}

	qs, err := eng.Price(1, 1, ticketSenior, enter)
	if err != nil {
		t.Fatal(err)
	}
	if qs.FullFareCents != 300 || qs.PaidFareCents != 0 {
		t.Errorf("同站敬老 full=%d paid=%d, want 300/0", qs.FullFareCents, qs.PaidFareCents)
	}
}
