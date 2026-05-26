package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type CronSchedule struct {
	minute     map[int]bool
	hour       map[int]bool
	dayOfMonth map[int]bool
	month      map[int]bool
	dayOfWeek  map[int]bool
}

func ParseCron(expr string) (CronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return CronSchedule{}, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}
	minute, err := parseCronField(fields[0], 0, 59, false)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("minute: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23, false)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("hour: %w", err)
	}
	dayOfMonth, err := parseCronField(fields[2], 1, 31, false)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("day-of-month: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12, false)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("month: %w", err)
	}
	dayOfWeek, err := parseCronField(fields[4], 0, 7, true)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("day-of-week: %w", err)
	}
	schedule := CronSchedule{
		minute:     minute,
		hour:       hour,
		dayOfMonth: dayOfMonth,
		month:      month,
		dayOfWeek:  dayOfWeek,
	}
	if err := validateCronSatisfiable(schedule); err != nil {
		return CronSchedule{}, err
	}
	return schedule, nil
}

// validateCronSatisfiable rejects month/day-of-month combinations that can
// never match the Gregorian calendar (e.g. "0 0 30 2 *" — Feb 30). Without
// this, NextAfter would otherwise scan up to five years one minute at a time
// (~2.6M iterations) on every Tick.
func validateCronSatisfiable(c CronSchedule) error {
	// Build the set of valid (month, day) pairs allowed by the calendar.
	// Use a leap year so Feb 29 is permitted when month=2.
	daysPerMonth := map[int]int{
		1: 31, 2: 29, 3: 31, 4: 30, 5: 31, 6: 30,
		7: 31, 8: 31, 9: 30, 10: 31, 11: 30, 12: 31,
	}
	for month := range c.month {
		max, ok := daysPerMonth[month]
		if !ok {
			continue
		}
		for dom := range c.dayOfMonth {
			if dom >= 1 && dom <= max {
				return nil
			}
		}
	}
	return fmt.Errorf("cron expression has no satisfiable (month, day-of-month) combination")
}

func (c CronSchedule) Matches(t time.Time) bool {
	weekday := int(t.Weekday())
	return c.minute[t.Minute()] &&
		c.hour[t.Hour()] &&
		c.dayOfMonth[t.Day()] &&
		c.month[int(t.Month())] &&
		c.dayOfWeek[weekday]
}

func (c CronSchedule) CurrentWindow(now time.Time, loc *time.Location) (time.Time, bool) {
	local := now.In(loc).Truncate(time.Minute)
	if !c.Matches(local) {
		return time.Time{}, false
	}
	return local.UTC(), true
}

func (c CronSchedule) NextAfter(after time.Time, loc *time.Location) (time.Time, error) {
	start := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	// 5-year deadline preserves the previous public contract while
	// validateCronSatisfiable prevents the worst-case 2.6M-iteration spin.
	deadline := start.AddDate(5, 0, 0)
	// Day-then-minute scan: advance by whole days until we find one whose
	// (month, day-of-month, day-of-week) all match, then scan the matching
	// minutes within that day. Worst case ~5y * 366d ~= 1832 day iterations
	// + at most 24*60 minute iterations, vs ~2.6M for the old per-minute
	// loop, even on schedules that fire only a few times a year.
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	for !day.After(deadline) {
		if c.month[int(day.Month())] && c.dayOfMonth[day.Day()] && c.dayOfWeek[int(day.Weekday())] {
			candidate := day
			if day.Equal(time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)) {
				candidate = start
			}
			for candidate.Day() == day.Day() && !candidate.After(deadline) {
				if c.minute[candidate.Minute()] && c.hour[candidate.Hour()] {
					return candidate.UTC(), nil
				}
				candidate = candidate.Add(time.Minute)
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}, fmt.Errorf("no cron occurrence found within 5 years")
}

func loadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "utc") {
		return time.UTC, nil
	}
	return time.LoadLocation(name)
}

func parseCronField(field string, minValue, maxValue int, normalizeSunday bool) (map[int]bool, error) {
	values := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty token")
		}
		step := 1
		if left, right, ok := strings.Cut(part, "/"); ok {
			part = strings.TrimSpace(left)
			parsedStep, err := strconv.Atoi(strings.TrimSpace(right))
			if err != nil || parsedStep <= 0 {
				return nil, fmt.Errorf("invalid step %q", right)
			}
			step = parsedStep
		}
		start, end, err := parseCronRange(part, minValue, maxValue)
		if err != nil {
			return nil, err
		}
		for value := start; value <= end; value += step {
			normalized := value
			if normalizeSunday && normalized == 7 {
				normalized = 0
			}
			values[normalized] = true
		}
	}
	return values, nil
}

func parseCronRange(part string, minValue, maxValue int) (int, int, error) {
	switch {
	case part == "*":
		return minValue, maxValue, nil
	case strings.Contains(part, "-"):
		left, right, _ := strings.Cut(part, "-")
		start, err := parseCronInt(left, minValue, maxValue)
		if err != nil {
			return 0, 0, err
		}
		end, err := parseCronInt(right, minValue, maxValue)
		if err != nil {
			return 0, 0, err
		}
		if start > end {
			return 0, 0, fmt.Errorf("range start %d is after end %d", start, end)
		}
		return start, end, nil
	default:
		value, err := parseCronInt(part, minValue, maxValue)
		if err != nil {
			return 0, 0, err
		}
		return value, value, nil
	}
}

func parseCronInt(value string, minValue, maxValue int) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", value)
	}
	if parsed < minValue || parsed > maxValue {
		return 0, fmt.Errorf("value %d outside %d-%d", parsed, minValue, maxValue)
	}
	return parsed, nil
}
