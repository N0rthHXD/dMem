package retrieval

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TimeInterval is a normalized, half-open time range. Precision records how the
// range was derived so exact-date, month, year, and approximate matches can be
// scored differently while the source wording is preserved separately.
type TimeInterval struct {
	Start     time.Time
	End       time.Time
	Precision string
	Set       bool
}

// CandidateTime keeps event time and valid time separate. They must never be
// conflated: event_time_* is when something happened, valid_* is when a fact or
// preference holds.
type CandidateTime struct {
	EventStart, EventEnd  *time.Time
	ValidFrom, ValidUntil *time.Time
	EventPrecision        string
}

// ForQuestion chooses the time axis that matches the query intent.
func (c CandidateTime) ForQuestion(question QuestionType) (TimeInterval, bool) {
	if question == QuestionTemporal {
		if interval, ok := eventInterval(c.EventStart, c.EventEnd, c.EventPrecision); ok {
			return interval, true
		}
	}
	if interval, ok := validInterval(c.ValidFrom, c.ValidUntil); ok {
		return interval, true
	}
	if interval, ok := eventInterval(c.EventStart, c.EventEnd, c.EventPrecision); ok {
		return interval, true
	}
	return TimeInterval{}, false
}

func eventInterval(start, end *time.Time, precision string) (TimeInterval, bool) {
	if start == nil && end == nil {
		return TimeInterval{}, false
	}
	interval := TimeInterval{Set: true, Precision: precision}
	if start != nil {
		interval.Start = *start
	}
	if end != nil {
		interval.End = *end
	}
	return interval, interval.Valid()
}

func validInterval(from, until *time.Time) (TimeInterval, bool) {
	if from == nil && until == nil {
		return TimeInterval{}, false
	}
	interval := TimeInterval{Set: true, Precision: "valid"}
	if from != nil {
		interval.Start = *from
	}
	if until != nil {
		interval.End = *until
	}
	return interval, interval.Valid()
}

// Valid reports whether the interval is an ordered, real interval. A nil bound
// is treated as unbounded on that side.
func (i TimeInterval) Valid() bool {
	if !i.Set {
		return true
	}
	if !i.Start.IsZero() && !i.End.IsZero() && !i.End.After(i.Start) {
		return false
	}
	return true
}

// ImpossibleInterval reports whether explicit bounds describe an impossible
// interval. Candidates with impossible intervals are removed before top-k.
func ImpossibleInterval(start, end *time.Time) bool {
	if start == nil || end == nil {
		return false
	}
	return !end.After(*start)
}

var (
	relativeAgoTimePattern = regexp.MustCompile(`(\d+) (day|week|month|year)s? ago`)
	absoluteDatePattern    = regexp.MustCompile(`(\d{4})[-/](\d{1,2})[-/](\d{1,2})`)
	bareYearPattern        = regexp.MustCompile(`\b(19|20)\d{2}\b`)
)

// ParseQueryTime extracts the strongest normalized time interval from a query
// and returns the source wording so answer context can preserve it verbatim.
func ParseQueryTime(query string, reference time.Time) (TimeInterval, string, bool) {
	if reference.IsZero() {
		return TimeInterval{}, "", false
	}
	reference = reference.UTC()
	lower := strings.ToLower(query)

	for _, phrase := range []struct {
		text   string
		offset int
	}{
		{"the day before yesterday", -2}, {"the day after tomorrow", 2},
		{"yesterday", -1}, {"today", 0}, {"tomorrow", 1},
	} {
		if strings.Contains(lower, phrase.text) {
			day := startOfDay(reference).AddDate(0, 0, phrase.offset)
			return TimeInterval{Start: day, End: day.AddDate(0, 0, 1), Precision: "day", Set: true}, phrase.text, true
		}
	}
	for _, phrase := range []struct {
		text   string
		unit   int
		offset int
	}{
		{"last week", weekUnit, -1}, {"this week", weekUnit, 0}, {"next week", weekUnit, 1},
		{"last month", monthUnit, -1}, {"this month", monthUnit, 0}, {"next month", monthUnit, 1},
		{"last year", yearUnit, -1}, {"this year", yearUnit, 0}, {"next year", yearUnit, 1},
	} {
		if strings.Contains(lower, phrase.text) {
			return shiftPeriod(reference, phrase.unit, phrase.offset), phrase.text, true
		}
	}
	if match := relativeAgoTimePattern.FindStringSubmatch(lower); match != nil {
		count, _ := strconv.Atoi(match[1])
		unit := match[2]
		days := count
		switch unit {
		case "week":
			days = count * 7
		case "month":
			days = count * 30
		case "year":
			days = count * 365
		}
		day := startOfDay(reference).AddDate(0, 0, -days)
		return TimeInterval{Start: day, End: day.AddDate(0, 0, 1), Precision: "approximate", Set: true}, match[0], true
	}
	if match := absoluteDatePattern.FindStringSubmatch(lower); match != nil {
		year, _ := strconv.Atoi(match[1])
		month, _ := strconv.Atoi(match[2])
		day, _ := strconv.Atoi(match[3])
		if start := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC); day >= 1 && month >= 1 && month <= 12 && start.Year() == year && int(start.Month()) == month && start.Day() == day {
			return TimeInterval{Start: start, End: start.AddDate(0, 0, 1), Precision: "day", Set: true}, match[0], true
		}
		return TimeInterval{}, match[0], false
	}
	if match := bareYearPattern.FindString(lower); match != "" {
		year, _ := strconv.Atoi(match)
		start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		return TimeInterval{Start: start, End: start.AddDate(1, 0, 0), Precision: "year", Set: true}, match, true
	}
	return TimeInterval{}, "", false
}

const (
	weekUnit = iota + 1
	monthUnit
	yearUnit
)

func shiftPeriod(reference time.Time, unit, offset int) TimeInterval {
	switch unit {
	case weekUnit:
		start := startOfWeek(reference).AddDate(0, 0, offset*7)
		return TimeInterval{Start: start, End: start.AddDate(0, 0, 7), Precision: "week", Set: true}
	case monthUnit:
		start := time.Date(reference.Year(), reference.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, offset, 0)
		return TimeInterval{Start: start, End: start.AddDate(0, 1, 0), Precision: "month", Set: true}
	default:
		start := time.Date(reference.Year()+offset, 1, 1, 0, 0, 0, 0, time.UTC)
		return TimeInterval{Start: start, End: start.AddDate(1, 0, 0), Precision: "year", Set: true}
	}
}

func startOfDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func startOfWeek(value time.Time) time.Time {
	day := startOfDay(value)
	return day.AddDate(0, 0, -int(day.Weekday()))
}

// TemporalMatch scores how well a candidate interval matches the query
// interval. compatible is false only when the candidate interval is impossible
// or when an explicit query time cannot plausibly overlap the candidate.
func TemporalMatch(query TimeInterval, candidate TimeInterval) (score float64, compatible bool) {
	if !query.Set || !candidate.Set {
		return 0, true
	}
	if !candidate.Valid() {
		return 0, false
	}
	queryStart, queryEnd := query.Start, query.End
	if queryEnd.IsZero() {
		queryEnd = queryStart
	}
	candStart, candEnd := candidate.Start, candidate.End
	if candStart.IsZero() {
		candStart = candEnd
	}
	if candEnd.IsZero() {
		candEnd = candStart
	}
	if candStart.Before(queryEnd) && queryStart.Before(candEnd) {
		return precisionWeight(candidate.Precision), true
	}
	gap := candStart.Sub(queryEnd)
	if queryStart.After(candEnd) {
		gap = queryStart.Sub(candEnd)
	}
	if gap < 0 {
		gap = -gap
	}
	days := gap.Hours() / 24
	if days > 366 {
		return 0, false
	}
	return precisionWeight(candidate.Precision) / (1 + days/30), true
}

func precisionWeight(precision string) float64 {
	switch precision {
	case "approximate":
		return 0.8
	case "year":
		return 0.85
	case "month", "week":
		return 0.9
	case "unknown":
		return 0.75
	default:
		return 1
	}
}
