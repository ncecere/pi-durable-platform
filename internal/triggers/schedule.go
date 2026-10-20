package triggers

import (
	"strings"
	"time"
	// Time zones come with the binary: the image has no zoneinfo.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// Schedules: a five-field cron expression (or a descriptor such as @daily)
// in an IANA time zone. @every is refused: it ignores the zone.

// Missed-fire policies.
const (
	MissedSkip    = "skip"
	MissedRunOnce = "run_once"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// schedule is a parsed cron expression in its zone.
type schedule struct {
	cron cron.Schedule
	loc  *time.Location
}

// parseSchedule checks a cron expression and a time zone.
func parseSchedule(expr, zone string) (schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" || len(expr) > 200 || strings.HasPrefix(expr, "@every") || strings.Contains(expr, "TZ=") {
		return schedule{}, errBadCron
	}
	if zone == "" || zone == "Local" || len(zone) > 64 {
		return schedule{}, errBadZone
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return schedule{}, errBadZone
	}
	c, err := cronParser.Parse(expr)
	if err != nil {
		return schedule{}, errBadCron
	}
	s := schedule{cron: c, loc: loc}
	if s.next(time.Now()).IsZero() {
		return schedule{}, errBadCron // never fires (30 February)
	}
	return s, nil
}

// next is the first fire time after t (zero: none within five years).
func (s schedule) next(t time.Time) time.Time {
	n := s.cron.Next(t.In(s.loc))
	if n.IsZero() {
		return n
	}
	return n.UTC()
}

// lastBefore is the latest fire time from first (a fire time) up to now: the
// one a missed schedule runs once for.
func (s schedule) lastBefore(first, now time.Time) time.Time {
	last := first
	for i := 0; i < 1_000_000; i++ {
		n := s.next(last)
		if n.IsZero() || n.After(now) {
			break
		}
		last = n
	}
	return last
}
