package ingest

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Day-first layouts are tried before month-first ones (Indian convention).
var dateTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-1-2 15:04:05",
	"2006-1-2 15:04",
	"2006-1-2 3:04:05 PM",
	"2006-1-2 3:04 PM",
	"2006/1/2 15:04:05",
	"2006/1/2 15:04",
	"2-1-2006 15:04:05",
	"2-1-2006 15:04",
	"2-1-2006 3:04:05 PM",
	"2-1-2006 3:04 PM",
	"2/1/2006 15:04:05",
	"2/1/2006 15:04",
	"2/1/2006 3:04:05 PM",
	"2/1/2006 3:04 PM",
	"2.1.2006 15:04:05",
	"2-Jan-2006 15:04:05",
	"2-Jan-2006 15:04",
	"2 Jan 2006 15:04:05",
	"2 Jan 2006 15:04",
	"2-Jan-06 15:04:05",
	"Jan 2, 2006 15:04:05",
	"2006-1-2",
	"2006/1/2",
	"2-1-2006",
	"2/1/2006",
	"2.1.2006",
	"2-Jan-2006",
	"2 Jan 2006",
	"2-Jan-06",
	"Jan 2, 2006",
	"20060102150405",
	"20060102",
}

var timeLayouts = []string{"15:04:05", "15:04", "3:04:05 PM", "3:04 PM", "3:04:05PM", "3:04PM", "150405"}

type timeParser struct {
	loc  *time.Location
	last int // index of last layout that worked, tried first next time
}

func (p *timeParser) location() *time.Location {
	if p.loc == nil {
		return time.Local
	}
	return p.loc
}

func (p *timeParser) parse(s string) (time.Time, error) {
	s = upperMeridiem(strings.TrimSpace(s))
	if t, ok := p.excel(s); ok {
		return t, nil
	}
	if p.last < len(dateTimeLayouts) {
		if t, err := time.ParseInLocation(dateTimeLayouts[p.last], s, p.location()); err == nil {
			return t, nil
		}
	}
	for i, l := range dateTimeLayouts {
		if t, err := time.ParseInLocation(l, s, p.location()); err == nil {
			p.last = i
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date/time %q", s)
}

func (p *timeParser) parseDateTime(date, clock string) (time.Time, error) {
	d, err := p.parse(date)
	if err != nil || clock == "" {
		return d, err
	}
	clock = strings.TrimSpace(clock)
	// Excel stores a time-of-day as a fraction of a day.
	if f, err := strconv.ParseFloat(clock, 64); err == nil && f >= 0 && f < 1 {
		secs := int(math.Round(f * 86400))
		return d.Add(time.Duration(secs) * time.Second), nil
	}
	for _, l := range timeLayouts {
		if c, err := time.Parse(l, strings.ToUpper(clock)); err == nil {
			return time.Date(d.Year(), d.Month(), d.Day(), c.Hour(), c.Minute(), c.Second(), 0, p.location()), nil
		}
	}
	// Some exports repeat the full timestamp in the time column.
	if t, err := p.parse(clock); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognised time %q", clock)
}

// upperMeridiem turns a trailing "am"/"pm" into the upper case Go expects.
func upperMeridiem(s string) string {
	if n := len(s); n > 2 {
		switch strings.ToLower(s[n-2:]) {
		case "am", "pm":
			return s[:n-2] + strings.ToUpper(s[n-2:])
		}
	}
	return s
}

// excel converts an Excel serial date (days since 1899-12-30).
func (p *timeParser) excel(s string) (time.Time, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 20000 || f > 80000 {
		return time.Time{}, false
	}
	days := math.Floor(f)
	secs := math.Round((f - days) * 86400)
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, p.location())
	return base.AddDate(0, 0, int(days)).Add(time.Duration(secs) * time.Second), true
}
