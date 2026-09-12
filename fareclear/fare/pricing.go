package fare

import (
	"errors"
	"math"
	"sort"
)

// 计价结果与规则校验相关错误。
var (
	// ErrNoBands 规则版本没有任何分段。
	ErrNoBands = errors.New("fare: rule version has no bands")
	// ErrBandGap 分段之间不连续或顺序错误。
	ErrBandGap = errors.New("fare: fare bands must be ordered and contiguous")
	// ErrBadStep 累进段缺少有效步长/步价。
	ErrBadStep = errors.New("fare: stepped band needs positive step distance and fare")
)

// ValidateBands 校验分段表：按 ordinal 排序后必须从 0 起、在界点处相接。
// 相邻两段共享界点（前段为闭区间），命中时取 ordinal 最小的段，
// 因此界点归前段：例如 [0,4km] 与 [4km,16km] 在 4km 处按 3 元计。
// 第一段必须给一口价 base；其余段必须给正步长与正步价。
func ValidateBands(bands []Band) error {
	if len(bands) == 0 {
		return ErrNoBands
	}
	sorted := make([]Band, len(bands))
	copy(sorted, bands)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Ordinal < sorted[j].Ordinal })

	if sorted[0].FromDistanceM != 0 {
		return ErrBandGap
	}
	if sorted[0].BaseFareCents <= 0 {
		return errors.New("fare: first band needs a positive base fare")
	}
	for i, b := range sorted {
		if i == 0 {
			continue
		}
		if b.StepDistanceM <= 0 || b.StepFareCents <= 0 {
			return ErrBadStep
		}
		prev := sorted[i-1]
		if b.FromDistanceM != prev.ToDistanceM {
			return ErrBandGap
		}
		if i < len(sorted)-1 {
			if b.ToDistanceM <= b.FromDistanceM {
				return ErrBandGap
			}
		} else if b.ToDistanceM != 0 && b.ToDistanceM <= b.FromDistanceM {
			return ErrBandGap
		}
	}
	return nil
}

// BaseFare 按分段累进规则计算原价（单位：分），并受版本封顶约束。
//
// 初始规则的分段配置为：
//
//	ordinal 1: [0m, 4000m]            一口价 300 分
//	ordinal 2: [4000m, 16000m]        每 4000m 加 100 分
//	ordinal 3: [16000m, +∞)           每 6000m 加 100 分
//	封顶 1000 分
//
// 界点归前段（4000m 仍为 300 分，16000m 仍为 600 分）。
// 命中第 i 段时，票价 = 起步价 + 第 2..i-1 段走满的步进金额
// + 第 i 段内超出下界里程向上取整的步进金额。
func BaseFare(v RuleVersion, distanceM int) (int64, error) {
	if distanceM < 0 {
		return 0, errors.New("fare: negative distance")
	}
	bands := append([]Band(nil), v.Bands...)
	sort.Slice(bands, func(i, j int) bool { return bands[i].Ordinal < bands[j].Ordinal })
	if err := ValidateBands(bands); err != nil {
		return 0, err
	}

	// 找到第一个上界覆盖该里程的段（最后一段上界为 +∞）。
	matched := -1
	for i, b := range bands {
		if distanceM < b.FromDistanceM {
			break
		}
		if b.ToDistanceM <= 0 || distanceM <= b.ToDistanceM {
			matched = i
			break
		}
	}
	if matched < 0 {
		return 0, errors.New("fare: distance matched no band")
	}

	fare := bands[0].BaseFareCents
	for j := 1; j <= matched; j++ {
		b := bands[j]
		if j < matched {
			// 中间段走满：上界 - 下界，按步长向上取整（数据整齐时即整除）。
			fare += int64(ceilDiv(b.ToDistanceM-b.FromDistanceM, b.StepDistanceM)) * b.StepFareCents
		} else {
			fare += int64(ceilDiv(distanceM-b.FromDistanceM, b.StepDistanceM)) * b.StepFareCents
		}
	}

	if v.CapFareCents > 0 && fare > v.CapFareCents {
		fare = v.CapFareCents
	}
	return fare, nil
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return int(math.Ceil(float64(a) / float64(b)))
}

// PaidFare 在原价基础上套用票种，得到乘客实付（单位：分）。
//   - full：原价；
//   - rate：按 discount_bp（万分位）打折，半数向上取整（如 95 折出现 0.5 分时）；
//   - free：实付 0。注意调用方仍须保留 fullFare 作为清分落账金额。
func PaidFare(fullFareCents int64, t TicketType) int64 {
	switch t.Kind {
	case KindRate:
		// floor((x*bp)/10000 + 0.5)，半数向上取整。
		return (fullFareCents*int64(t.DiscountBp) + 5000) / 10000
	case KindFree:
		return 0
	default:
		return fullFareCents
	}
}
