package fare

import "time"

// 测试固定时区（业务时区），避免受运行环境 TZ 影响。
var testTZ = time.FixedZone("CST", 8*3600)

func mustDate(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

// timeInTZ 解析带偏移的 RFC3339 时刻。
func timeInTZ(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// rule2024 初始规则（与种子 FARE-2024 一致）：
// 4km 内 3 元；其后到 16km 每 4km 加 1 元；16km 以上每 6km 加 1 元；封顶 10 元。
func rule2024() RuleVersion {
	return RuleVersion{
		ID:           1,
		Code:         "FARE-2024",
		ValidFrom:    mustDate("2024-01-01"),
		ValidUntil:   datePtr(mustDate("2026-01-01")),
		CapFareCents: 1000,
		Bands: []Band{
			{Ordinal: 1, FromDistanceM: 0, ToDistanceM: 4000, BaseFareCents: 300},
			{Ordinal: 2, FromDistanceM: 4000, ToDistanceM: 16000, StepDistanceM: 4000, StepFareCents: 100},
			{Ordinal: 3, FromDistanceM: 16000, ToDistanceM: 0, StepDistanceM: 6000, StepFareCents: 100},
		},
	}
}

// rule2026 调整版：起步 4 元、封顶 12 元，其余步进不变。
func rule2026() RuleVersion {
	return RuleVersion{
		ID:           2,
		Code:         "FARE-2026",
		ValidFrom:    mustDate("2026-01-01"),
		CapFareCents: 1200,
		Bands: []Band{
			{Ordinal: 1, FromDistanceM: 0, ToDistanceM: 4000, BaseFareCents: 400},
			{Ordinal: 2, FromDistanceM: 4000, ToDistanceM: 16000, StepDistanceM: 4000, StepFareCents: 100},
			{Ordinal: 3, FromDistanceM: 16000, ToDistanceM: 0, StepDistanceM: 6000, StepFareCents: 100},
		},
	}
}

func datePtr(t time.Time) *time.Time { return &t }

var (
	ticketFull    = TicketType{ID: 1, Code: "SINGLE", Name: "单程票", Kind: KindFull}
	ticketStudent = TicketType{ID: 2, Code: "STUDENT", Name: "学生票", Kind: KindRate, DiscountBp: 5000}
	ticketSenior  = TicketType{ID: 3, Code: "SENIOR", Name: "敬老卡", Kind: KindFree}
)
