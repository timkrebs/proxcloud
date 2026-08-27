package lifecycle

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// warnLead is how far ahead of a scheduled shutdown the T-15m warning fires.
const warnLead = 15 * time.Minute

// parseHHMM parses a "HH:MM" 24h clock string into hour/minute. It is strict:
// exactly two colon-separated fields, hour 0..23, minute 0..59.
func parseHHMM(s string) (hh, mm int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid time %q: want HH:MM", s)
	}
	hh, err = strconv.Atoi(parts[0])
	if err != nil || hh < 0 || hh > 23 {
		return 0, 0, fmt.Errorf("invalid hour in %q", s)
	}
	mm, err = strconv.Atoi(parts[1])
	if err != nil || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("invalid minute in %q", s)
	}
	return hh, mm, nil
}

// cronExpr composes a 5-field standard cron ("min hour * * dow-list") from a
// wall-clock time and a day-of-week list (0..6, Sun..Sat), evaluated in the
// schedule's timezone by the engine's NextCron. Cron is derived here, never
// user-entered (ADR-0019).
func cronExpr(hh, mm int, days []int) string {
	ds := make([]string, len(days))
	for i, d := range days {
		ds[i] = strconv.Itoa(((d % 7) + 7) % 7)
	}
	return fmt.Sprintf("%d %d * * %s", mm, hh, strings.Join(ds, ","))
}

// warnTime returns the wall-clock time warnLead before hh:mm plus the day-of-week
// list the warning must fire on. If subtracting the lead crosses back over
// midnight (a shutdown before 00:15), the fire days shift one day earlier — a
// 00:05 Monday shutdown warns at 23:50 Sunday — so the warning lands the right
// evening, not 23h55m late on the same day.
func warnTime(hh, mm int, days []int) (int, int, []int) {
	total := hh*60 + mm - int(warnLead.Minutes())
	if total >= 0 {
		return total / 60, total % 60, days
	}
	total += 24 * 60
	shifted := make([]int, len(days))
	for i, d := range days {
		shifted[i] = ((d-1)%7 + 7) % 7
	}
	return total / 60, total % 60, shifted
}
