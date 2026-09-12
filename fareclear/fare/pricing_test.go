package fare

import (
	"math"
	"testing"
)

// TestBaseFareBoundaries 逐段验证分段累进的界点归属与步进。
//
//	区间 1 [0,4km]      300 分一口价
//	区间 2 (4km,16km]   每 4km 加 100 分（界点归前段：4km 仍 300、16km 仍 600）
//	区间 3 (16km,+∞)    每 6km 加 100 分
//	封顶 1000 分
func TestBaseFareBoundaries(t *testing.T) {
	v := rule2024()
	cases := []struct {
		name string
		m    int
		want int64
	}{
		{"同站 0m 按起步价", 0, 300},
		{"1m 仍起步价", 1, 300},
		{"4km 整归第一段", 4000, 300},
		{"4km+1m 进一段步价", 4001, 400},
		{"8km 整 4 元", 8000, 400},
		{"8km+1m 5 元", 8001, 500},
		{"12km 整 5 元", 12000, 500},
		{"12km+1m 6 元", 12001, 600},
		{"16km 整归第二段 6 元", 16000, 600},
		{"16km+1m 进 6km 步价 7 元", 16001, 700},
		{"22km 整 7 元", 22000, 700},
		{"22km+1m 8 元", 22001, 800},
		{"28km 整 8 元", 28000, 800},
		{"28km+1m 9 元", 28001, 900},
		{"34km 整 9 元", 34000, 900},
		// 34km+1m：未封顶应为 3+3+ceil(18001/6000)=10 元，恰达封顶。
		{"34km+1m 触及封顶", 34001, 1000},
		// 40km：未封顶 3+3+ceil(24000/6000)=10 元，仍为封顶值。
		{"40km 仍封顶 10 元", 40000, 1000},
		{"100km 远途仍封顶", 100000, 1000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := BaseFare(v, c.m)
			if err != nil {
				t.Fatalf("BaseFare(%d) error: %v", c.m, err)
			}
			if got != c.want {
				t.Errorf("BaseFare(%dm) = %d 分, want %d 分", c.m, got, c.want)
			}
		})
	}
}

// TestBaseFareFormula 对整个里程轴抽样，与一个独立的参考公式交叉验证，
// 防止手算用例与实现犯同一个错误。
func TestBaseFareFormula(t *testing.T) {
	v := rule2024()
	reference := func(m int) int64 {
		var f float64
		switch {
		case m <= 4000:
			f = 3
		case m <= 16000:
			f = 3 + math.Ceil(float64(m-4000)/4000)
		default:
			f = 3 + 3 + math.Ceil(float64(m-16000)/6000)
		}
		if f > 10 {
			f = 10
		}
		return int64(f * 100)
	}
	for m := 0; m <= 60000; m += 137 { // 步长与界点互质，抽样不漏界点邻域
		got, err := BaseFare(v, m)
		if err != nil {
			t.Fatalf("BaseFare(%d): %v", m, err)
		}
		if want := reference(m); got != want {
			t.Errorf("m=%d: got %d, reference %d", m, got, want)
		}
	}
	// 再精确打所有界点及其 ±1m。
	for _, b := range []int{4000, 8000, 12000, 16000, 22000, 28000, 34000} {
		for _, m := range []int{b - 1, b, b + 1} {
			got, _ := BaseFare(v, m)
			if want := reference(m); got != want {
				t.Errorf("boundary m=%d: got %d, reference %d", m, got, want)
			}
		}
	}
}

func TestTicketFares(t *testing.T) {
	v := rule2024()

	// 24km 原价 800 分（6 + ceil(8/6)=2 步进）。
	full, err := BaseFare(v, 24000)
	if err != nil {
		t.Fatal(err)
	}
	if full != 800 {
		t.Fatalf("full fare = %d, want 800", full)
	}

	if got := PaidFare(full, ticketFull); got != 800 {
		t.Errorf("全价票实付 = %d, want 800", got)
	}
	if got := PaidFare(full, ticketStudent); got != 400 {
		t.Errorf("学生票五折 = %d, want 400", got)
	}
	// 学生票在奇数原价下的半价（3 元 -> 1.5 元 = 150 分，精确不涉及取整）。
	if got := PaidFare(300, ticketStudent); got != 150 {
		t.Errorf("学生票 3 元五折 = %d, want 150", got)
	}

	seniorPaid := PaidFare(full, ticketSenior)
	if seniorPaid != 0 {
		t.Errorf("敬老卡实付 = %d, want 0", seniorPaid)
	}
	// 关键：免费不等于免账——原价 800 分必须保留下来供清分。
	if full != 800 {
		t.Errorf("敬老卡原价快照 = %d, want 800（仍须按原价落账）", full)
	}
}

// TestPaidFareRounding 折扣出现半分时半数向上取整：
// 300 分打 1.5 折（150bp）= 4.5 分 → 5 分。
func TestPaidFareRounding(t *testing.T) {
	tk := TicketType{Kind: KindRate, DiscountBp: 150}
	if got := PaidFare(300, tk); got != 5 {
		t.Errorf("300 分打 1.5 折 = %d, want 5（半数向上）", got)
	}
}

// TestValidateBands 拒绝分段表常见配置错误。
func TestValidateBands(t *testing.T) {
	good := rule2024().Bands
	if err := ValidateBands(good); err != nil {
		t.Fatalf("合法分段被拒: %v", err)
	}

	bad := func(modify func([]Band) []Band) error {
		bs := append([]Band(nil), good...)
		return ValidateBands(modify(bs))
	}
	if err := bad(func(b []Band) []Band { return nil }); err != ErrNoBands {
		t.Errorf("空分段: got %v, want ErrNoBands", err)
	}
	if err := bad(func(b []Band) []Band { b[0].FromDistanceM = 1000; return b }); err != ErrBandGap {
		t.Errorf("首段不从 0 起: got %v, want ErrBandGap", err)
	}
	if err := bad(func(b []Band) []Band { b[1].FromDistanceM = 5000; return b }); err != ErrBandGap {
		t.Errorf("段间留缝: got %v, want ErrBandGap", err)
	}
	if err := bad(func(b []Band) []Band { b[2].StepDistanceM = 0; return b }); err != ErrBadStep {
		t.Errorf("累进段缺步长: got %v, want ErrBadStep", err)
	}
}

// TestCapFromBand 若里程远到步进金额超过封顶，结果就是封顶值，不会为负或溢出。
func TestCapFromBand(t *testing.T) {
	v := rule2024()
	got, err := BaseFare(v, math.MaxInt32)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1000 {
		t.Errorf("超长里程 = %d, want 1000", got)
	}
}
