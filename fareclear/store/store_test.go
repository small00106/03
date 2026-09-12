package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

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

func TestStoreEndToEnd(t *testing.T) {
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")

	st, err := store.Open(ctx, testDSN(t), loc)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// 再跑一次确认迁移幂等。
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate 幂等: %v", err)
	}

	eng, err := st.NewEngine(ctx)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// 票种 / 站点 ID 一律按种子代码查，避免依赖 IDENTITY 起始值。
	single, err := st.TicketTypeID(ctx, "SINGLE")
	if err != nil {
		t.Fatal(err)
	}
	student, err := st.TicketTypeID(ctx, "STUDENT")
	if err != nil {
		t.Fatal(err)
	}
	senior, err := st.TicketTypeID(ctx, "SENIOR")
	if err != nil {
		t.Fatal(err)
	}
	s1, err := st.StationID(ctx, "S1")
	if err != nil {
		t.Fatal(err)
	}
	s6, err := st.StationID(ctx, "S6")
	if err != nil {
		t.Fatal(err)
	}
	s7, err := st.StationID(ctx, "S7")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := st.StationID(ctx, "S2")
	if err != nil {
		t.Fatal(err)
	}

	// 全价票，跨零点：2025-12-31 23:50 进、2026-01-01 00:30 出（北京时间）。
	enter := time.Date(2025, 12, 31, 23, 50, 0, 0, loc)
	exit := time.Date(2026, 1, 1, 0, 30, 0, 0, loc)

	card := "IT-FULL-1"
	if _, err := st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: card, TicketTypeID: single, Time: enter, Direction: fare.Enter, StationID: s1, GateCode: "G1"},
		{CardNo: card, TicketTypeID: single, Time: exit, Direction: fare.Exit, StationID: s7, GateCode: "G7"},
	}); err != nil {
		t.Fatalf("InsertGateEvents: %v", err)
	}

	trips, unmatched, err := st.ProcessCard(ctx, card, eng)
	if err != nil || len(unmatched) != 0 {
		t.Fatalf("ProcessCard: err=%v unmatched=%d", err, len(unmatched))
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
	again, _, err := st.ProcessCard(ctx, card, eng)
	if err != nil {
		t.Fatalf("二次 ProcessCard: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("重复入账 %d 笔，want 0（已引用事件不得重复计价）", len(again))
	}

	// 学生五折 + 敬老免费仍按原价落账：取 26km（S1->S6）= 8 元
	// （6 + ceil(10000/6000)*1 = 8 元）。
	for _, tc := range []struct {
		card     string
		ticketID int64
		wantPaid int64
		wantFull int64
	}{
		{"IT-STU-1", student, 400, 800},
		{"IT-SEN-1", senior, 0, 800},
	} {
		if _, err := st.InsertGateEvents(ctx, []store.GateEventInput{
			{CardNo: tc.card, TicketTypeID: tc.ticketID, Time: time.Date(2025, 6, 1, 9, 0, 0, 0, loc), Direction: fare.Enter, StationID: s1},
			{CardNo: tc.card, TicketTypeID: tc.ticketID, Time: time.Date(2025, 6, 1, 10, 0, 0, 0, loc), Direction: fare.Exit, StationID: s6},
		}); err != nil {
			t.Fatal(err)
		}
		ts, _, err := st.ProcessCard(ctx, tc.card, eng)
		if err != nil || len(ts) != 1 {
			t.Fatalf("%s: err=%v n=%d", tc.card, err, len(ts))
		}
		if ts[0].FullFareCents != tc.wantFull || ts[0].PaidFareCents != tc.wantPaid {
			t.Errorf("%s: full=%d paid=%d, want full=%d paid=%d",
				tc.card, ts[0].FullFareCents, ts[0].PaidFareCents, tc.wantFull, tc.wantPaid)
		}
	}

	// 异常事件：只有出站，返回 ErrUnpairedEvents 且不产生行程。
	if _, err := st.InsertGateEvents(ctx, []store.GateEventInput{
		{CardNo: "IT-BAD-1", TicketTypeID: single, Time: time.Date(2025, 6, 2, 9, 0, 0, 0, loc), Direction: fare.Exit, StationID: s2},
	}); err != nil {
		t.Fatal(err)
	}
	_, unmatched, err = st.ProcessCard(ctx, "IT-BAD-1", eng)
	if !errors.Is(err, store.ErrUnpairedEvents) || len(unmatched) != 1 {
		t.Errorf("异常配对: err=%v unmatched=%d, want ErrUnpairedEvents/1", err, len(unmatched))
	}
}
