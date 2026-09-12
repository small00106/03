package fare

import (
	"container/heap"
	"errors"
)

// Edge 图上的一条无向邻接边，对应 segments 表一行。
type Edge struct {
	SegmentID  int64
	LineID     int64
	OperatorID int64
	DistanceM  int
}

// Graph 以物理站为顶点、区间为无向边的邻接表。
// 换乘站是同一个物理顶点，故换乘天然零代价；不同线路在此顶点邻接，
// 最短路可以自由换线。
type Graph struct {
	adj map[int64][]neighbor
}

type neighbor struct {
	to   int64
	edge Edge
}

// ErrUnknownStation 起/终点站不在网内或无挂接区间。
var ErrUnknownStation = errors.New("fare: station not present in network")

// NewGraph 返回空图，供手工 AddEdge 构图（主要用于测试）。
func NewGraph() *Graph { return &Graph{adj: make(map[int64][]neighbor)} }

// AddEdge 加入一个物理区间（无向）。主要供测试手工构图使用。
func (g *Graph) AddEdge(from, to int64, e Edge) {
	if e.DistanceM <= 0 {
		return
	}
	g.adj[from] = append(g.adj[from], neighbor{to: to, edge: e})
	g.adj[to] = append(g.adj[to], neighbor{to: from, edge: e})
}

// SegmentInput 构图输入：一个区间的两端点与属性。
type SegmentInput struct {
	SegmentID  int64
	LineID     int64
	OperatorID int64
	FromID     int64
	ToID       int64
	DistanceM  int
}

// BuildGraph 由区间行集构造无向图。
func BuildGraph(rows []SegmentInput) *Graph {
	g := &Graph{adj: make(map[int64][]neighbor)}
	for _, r := range rows {
		if r.DistanceM <= 0 || r.FromID == r.ToID {
			continue
		}
		e := Edge{SegmentID: r.SegmentID, LineID: r.LineID, OperatorID: r.OperatorID, DistanceM: r.DistanceM}
		g.adj[r.FromID] = append(g.adj[r.FromID], neighbor{to: r.ToID, edge: e})
		g.adj[r.ToID] = append(g.adj[r.ToID], neighbor{to: r.FromID, edge: e})
	}
	return g
}

// ShortestPath 返回两物理站间里程最短的路径边序列（Dijkstra）。
// 起终点相同返回空路径、0 里程；计价层对 0 里程照收起步价
// （同站进出按最低票价计，属正常收费行程而非免费）。
func (g *Graph) ShortestPath(origin, destination int64) ([]Edge, int, error) {
	if origin == destination {
		if _, ok := g.adj[origin]; !ok {
			return nil, 0, ErrUnknownStation
		}
		return []Edge{}, 0, nil
	}
	if _, ok := g.adj[origin]; !ok {
		return nil, 0, ErrUnknownStation
	}
	if _, ok := g.adj[destination]; !ok {
		return nil, 0, ErrUnknownStation
	}

	dist := map[int64]int{origin: 0}
	prev := map[int64]prevHop{}
	pq := &pathHeap{{station: origin, dist: 0}}

	for pq.Len() > 0 {
		cur := heap.Pop(pq).(pathNode)
		if cur.dist > dist[cur.station] {
			continue
		}
		if cur.station == destination {
			break
		}
		for _, nb := range g.adj[cur.station] {
			nd := cur.dist + nb.edge.DistanceM
			if d, ok := dist[nb.to]; !ok || nd < d {
				dist[nb.to] = nd
				prev[nb.to] = prevHop{from: cur.station, edge: nb.edge}
				heap.Push(pq, pathNode{station: nb.to, dist: nd})
			}
		}
	}

	total, ok := dist[destination]
	if !ok {
		return nil, 0, errors.New("fare: no path between stations")
	}

	// 回溯边序列（顺序：origin -> destination）。
	var edges []Edge
	for s := destination; s != origin; {
		h := prev[s]
		edges = append(edges, h.edge)
		s = h.from
	}
	for i, j := 0, len(edges)-1; i < j; i, j = i+1, j-1 {
		edges[i], edges[j] = edges[j], edges[i]
	}
	return edges, total, nil
}

type prevHop struct {
	from int64
	edge Edge
}

type pathNode struct {
	station int64
	dist    int
	index   int
}

type pathHeap []pathNode

func (h pathHeap) Len() int           { return len(h) }
func (h pathHeap) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h pathHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *pathHeap) Push(x any) {
	n := len(*h)
	p := x.(pathNode)
	p.index = n
	*h = append(*h, p)
}
func (h *pathHeap) Pop() any {
	old := *h
	n := len(old)
	p := old[n-1]
	*h = old[:n-1]
	return p
}
