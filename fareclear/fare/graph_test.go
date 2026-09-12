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

	// A 卡：正常一对 + 缺出站的进站；B 卡：缺进站的出站 + 正常一对。
	// 两卡交错给出，验证不会跨卡配对。
	events := []GateEvent{
		ev(1, "A", Enter, 1, 0),
		ev(2, "B", Exit, 5, 1), // B 无进站 -> 异常
		ev(3, "A", Exit, 5, 2), // A 正常配对 (1,3)
		ev(4, "B", Enter, 1, 3),
		ev(5, "A", Enter, 1, 4), // A 再进站
		// A 再无出站 -> 异常
		ev(6, "B", Exit, 5, 5),  // B 正常配对 (4,6)
		ev(7, "A", Enter, 1, 6), // A 连续进站：5 号异常，7 号待配对
	}
	trips, unmatched := PairEvents(events)

	if len(trips) != 2 {
		t.Fatalf("配对数 = %d, want 2", len(trips))
	}
	if trips[0].Enter.ID != 1 || trips[0].Exit.ID != 3 {
		t.Errorf("第一对 = (%d,%d), want (1,3)", trips[0].Enter.ID, trips[0].Exit.ID)
	}
	if trips[1].Enter.ID != 4 || trips[1].Exit.ID != 6 {
		t.Errorf("第二对 = (%d,%d), want (4,6)", trips[1].Enter.ID, trips[1].Exit.ID)
	}

	var unmatchedIDs []int64
	for _, e := range unmatched {
		unmatchedIDs = append(unmatchedIDs, e.ID)
	}
	wantUnmatched := map[int64]bool{2: true, 5: true, 7: true}
	if len(unmatchedIDs) != len(wantUnmatched) {
		t.Fatalf("异常事件 = %v, want %v", unmatchedIDs, wantUnmatched)
	}
	for _, id := range unmatchedIDs {
		if !wantUnmatched[id] {
			t.Errorf("事件 %d 不应被判为异常", id)
		}
	}
}
