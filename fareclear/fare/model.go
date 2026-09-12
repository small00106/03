// Package fare 是 fareclear 的计价内核：线网图、里程分段累进计价、
// 票种折扣、计价规则版本选择与进出站事件配对。
// 本包不依赖数据库，所有逻辑均可在单元测试中直接构造数据验证。
package fare

import "time"

// Direction 闸机方向。
type Direction string

const (
	Enter Direction = "enter"
	Exit  Direction = "exit"
)

// TicketKind 票种计价方式。
type TicketKind string

const (
	// KindFull 全价票。
	KindFull TicketKind = "full"
	// KindRate 折扣票（如学生五折），折扣率取 DiscountBp。
	KindRate TicketKind = "rate"
	// KindFree 免费票（如敬老卡）：实付 0，但仍按原价落账。
	KindFree TicketKind = "free"
)

// 基础数据 ---------------------------------------------------------------

type Operator struct {
	ID   int64
	Code string
	Name string
}

type Line struct {
	ID         int64
	Code       string
	Name       string
	OperatorID int64
}

type Station struct {
	ID   int64
	Code string
	Name string
}

// TicketType 票种。DiscountBp 为万分位折扣率，5000 即五折。
type TicketType struct {
	ID         int64
	Code       string
	Name       string
	Kind       TicketKind
	DiscountBp int
	Active     bool
}

// 计价规则 ---------------------------------------------------------------

// Band 一个计价分段。界点归 ordinal 较小的前段：[0,4000] 与 [4000,16000]
// 在 4000m 处按第一段计价。ToDistanceM <= 0 表示上不封顶的尾段。
// 第一段（Ordinal=1）收 BaseFareCents 一口价；其后各段按落入该段的
// 里程对 StepDistanceM 向上取整，乘以 StepFareCents。
type Band struct {
	Ordinal       int
	FromDistanceM int
	ToDistanceM   int // 右开界；<=0 表示 +Inf
	BaseFareCents int64
	StepDistanceM int
	StepFareCents int64
}

// RuleVersion 某一生效区间内的计价规则版本。
// ValidFrom / ValidUntil 均为 UTC 零点日期，区间左闭右开；
// ValidUntil 为 nil 表示长期有效。
type RuleVersion struct {
	ID           int64
	Code         string
	ValidFrom    time.Time
	ValidUntil   *time.Time
	CapFareCents int64
	Bands        []Band
}

// 事件与行程 -------------------------------------------------------------

// GateEvent 闸机原始事件。
type GateEvent struct {
	ID        int64
	CardNo    string
	Ticket    TicketType
	Time      time.Time
	Direction Direction
	StationID int64
	GateCode  string
}

// PairedTrip 一对进出站事件。
type PairedTrip struct {
	Enter GateEvent
	Exit  GateEvent
}

// Quote 一次计价的结果快照。
type Quote struct {
	Version       RuleVersion
	DistanceM     int
	Path          []Edge
	FullFareCents int64 // 原价（清分口径，免费票也照算）
	PaidFareCents int64 // 乘客实付
}
