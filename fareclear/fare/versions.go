package fare

import (
	"errors"
	"sort"
	"time"
)

// ErrNoRuleVersion 在指定日期没有生效的计价规则版本。
var ErrNoRuleVersion = errors.New("fare: no rule version effective on date")

// RuleBook 计价规则版本集合。版本按生效日期区间管理，行程按
// 「计价归属日」取当时生效的版本，历史行程不会被后来的规则改写。
type RuleBook struct {
	versions []RuleVersion
}

// NewRuleBook 接收版本集合并做区间不重叠校验。
func NewRuleBook(versions []RuleVersion) (*RuleBook, error) {
	vs := append([]RuleVersion(nil), versions...)
	sort.Slice(vs, func(i, j int) bool { return vs[i].ValidFrom.Before(vs[j].ValidFrom) })
	for i := range vs {
		if err := ValidateBands(vs[i].Bands); err != nil {
			return nil, err
		}
		if i == 0 {
			continue
		}
		prev := vs[i-1]
		if prev.ValidUntil == nil {
			return nil, errors.New("fare: overlapping rule versions: earlier version has no end date")
		}
		// 区间均为 [from, until)，相邻版本 until == next.from 即衔接不重叠。
		if vs[i].ValidFrom.Before(*prev.ValidUntil) {
			return nil, errors.New("fare: overlapping rule version date ranges")
		}
	}
	return &RuleBook{versions: vs}, nil
}

// VersionAt 返回某一时刻（按业务时区折算后的日期）生效的版本。
func (b *RuleBook) VersionAt(t time.Time) (RuleVersion, error) {
	d := dateOf(t)
	for _, v := range b.versions {
		from := dateOf(v.ValidFrom)
		if d.Before(from) {
			continue
		}
		if v.ValidUntil != nil {
			if !d.Before(dateOf(*v.ValidUntil)) {
				continue
			}
		}
		return v, nil
	}
	return RuleVersion{}, ErrNoRuleVersion
}

// dateOf 归一化到 UTC 日期零点，保证只按「日」比较，
// 与数据库 travel_date DATE 列口径一致。
func dateOf(t time.Time) time.Time {
	y, m, day := t.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

// TravelDate 计价归属日：进站时刻在业务时区下的日历日。
// 跨零点行程（23:50 进站、次日 00:20 出站）归属进站当天，
// 因而适用进站当天生效的规则版本。
func TravelDate(enterTime time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	t := enterTime.In(loc)
	return dateOf(t)
}
