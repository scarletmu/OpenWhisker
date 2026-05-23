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
	return CronSchedule{
		minute:     minute,
		hour:       hour,
		dayOfMonth: dayOfMonth,
		month:      month,
		dayOfWeek:  dayOfWeek,
	}, nil
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
	next := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	deadline := next.AddDate(5, 0, 0)
	for !next.After(deadline) {
		if c.Matches(next) {
			return next.UTC(), nil
		}
		next = next.Add(time.Minute)
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
