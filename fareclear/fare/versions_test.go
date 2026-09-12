package fare

import "testing"

func TestRuleBookVersionAt(t *testing.T) {
	book, err := NewRuleBook([]RuleVersion{rule2026(), rule2024()}) // 乱序输入也应排序
	if err != nil {
		t.Fatalf("NewRuleBook: %v", err)
	}

	cases := []struct {
		date string
		want string
	}{
		{"2023-12-31", ""}, // 规则生效前：无版本
		{"2024-01-01", "FARE-2024"},
		{"2025-12-31", "FARE-2024"}, // 末日仍属旧版
		{"2026-01-01", "FARE-2026"}, // 零点起切新版（左闭）
		{"2030-06-01", "FARE-2026"},
	}
	for _, c := range cases {
		t.Run(c.date, func(t *testing.T) {
			v, err := book.VersionAt(mustDate(c.date))
			if c.want == "" {
				if err != ErrNoRuleVersion {
					t.Fatalf("want ErrNoRuleVersion, got %v (%s)", err, v.Code)
				}
				return
			}
			if err != nil {
				t.Fatalf("VersionAt(%s): %v", c.date, err)
			}
			if v.Code != c.want {
				t.Errorf("VersionAt(%s) = %s, want %s", c.date, v.Code, c.want)
			}
		})
	}
}

func TestRuleBookRejectsOverlap(t *testing.T) {
	v1 := rule2024()
	v2 := rule2026()
	v2.ValidFrom = mustDate("2025-06-01") // 与 v1 [2024-01-01, 2026-01-01) 重叠
	if _, err := NewRuleBook([]RuleVersion{v1, v2}); err == nil {
		t.Fatal("重叠的生效区间应被拒绝")
	}
}

// TestCrossMidnightTrip 是核心回归：跨零点行程按进站当天选版本。
// 2025-12-31 23:50 进站、2026-01-01 00:20 出站——出站时刻新版已生效，
// 但行程归属 2025-12-31，必须仍按 FARE-2024（3 元起步）计价；
// 对照组同一天 23:50 进站、23:59 出站也是旧版；而 2026-01-01 00:10
// 进站的行程才用 FARE-2026（4 元起步）。
func TestCrossMidnightTrip(t *testing.T) {
	book, err := NewRuleBook([]RuleVersion{rule2024(), rule2026()})
	if err != nil {
		t.Fatal(err)
	}
	g := NewGraph()
	// S1 --2000m-- S2，落第一段一口价（2024: 3 元 / 2026: 4 元）。
	g.AddEdge(1, 2, Edge{SegmentID: 10, LineID: 20, OperatorID: 30, DistanceM: 2000})
	eng := NewEngine(g, book, testTZ)

	enter := timeInTZ("2025-12-31T23:50:00+08:00")
	exit := timeInTZ("2026-01-01T00:20:00+08:00")
	trip := PairedTrip{
		Enter: GateEvent{ID: 1, CardNo: "C1", Ticket: ticketFull, Time: enter, Direction: Enter, StationID: 1},
		Exit:  GateEvent{ID: 2, CardNo: "C1", Ticket: ticketFull, Time: exit, Direction: Exit, StationID: 2},
	}
	q, err := eng.PricePair(trip)
	if err != nil {
		t.Fatalf("PricePair: %v", err)
	}
	if q.Version.Code != "FARE-2024" {
		t.Errorf("跨零点行程用了 %s，want FARE-2024（按进站日）", q.Version.Code)
	}
	if q.FullFareCents != 300 {
		t.Errorf("跨零点行程票价 = %d, want 300", q.FullFareCents)
	}

	// 对照：2026-01-01 凌晨进站才是新版 4 元。
	enter2 := timeInTZ("2026-01-01T00:10:00+08:00")
	q2, err := eng.Price(1, 2, ticketFull, enter2)
	if err != nil {
		t.Fatal(err)
	}
	if q2.Version.Code != "FARE-2026" || q2.FullFareCents != 400 {
		t.Errorf("新版生效后行程 = %s/%d 分, want FARE-2026/400", q2.Version.Code, q2.FullFareCents)
	}

	// 极端情形：UTC 已跨年但业务时区仍在 12-31（UTC 16:30 = 北京次日 00:30
	// 的反向情形）——12-31 23:30 北京时间 = 12-31 15:30 UTC，
	// 版本选择必须看业务日而非 UTC 日。
	q3, err := eng.Price(1, 2, ticketFull, timeInTZ("2025-12-31T23:30:00+08:00"))
	if err != nil {
		t.Fatal(err)
	}
	if q3.Version.Code != "FARE-2024" {
		t.Errorf("业务时区跨日判定错误: got %s", q3.Version.Code)
	}
}

// TestHistoricalTripImmutableSemantics 从语义上锁定「历史行程快照不变」：
// 同一趟 2025 年的行程，在引擎加载了 2026 新版规则后重新计价，
// 金额与版本依旧；新版只影响新行程。
func TestHistoricalTripNotRewritten(t *testing.T) {
	book, _ := NewRuleBook([]RuleVersion{rule2024(), rule2026()})
	g := NewGraph()
	g.AddEdge(1, 2, Edge{SegmentID: 10, DistanceM: 2000})
	eng := NewEngine(g, book, testTZ)

	for i := 0; i < 3; i++ { // 重复计算，模拟后来新增版本后重放
		q, err := eng.Price(1, 2, ticketFull, timeInTZ("2025-06-01T08:00:00+08:00"))
		if err != nil {
			t.Fatal(err)
		}
		if q.Version.ID != 1 || q.FullFareCents != 300 {
			t.Fatalf("历史行程被改写: %s/%d", q.Version.Code, q.FullFareCents)
		}
	}
}
