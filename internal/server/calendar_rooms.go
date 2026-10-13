package server

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/production-planner/internal/calendar"
	"github.com/tylergoza/production-planner/internal/store"
)

// Room warnings across apps (CALENDAR_PLAN.md step 4). Warnings never block
// a save.

// blockBounds turns a calendar block into a half-open interval. A missing
// start time is the start of the day and a missing end time the end of the
// last day. ok is false for dates that can't be read.
func blockBounds(b calendar.Block) (start, end time.Time, ok bool) {
	day, err := time.Parse(store.DateLayout, b.StartsOn)
	if err != nil {
		return start, end, false
	}
	last := day
	if b.EndsOn != "" {
		if last, err = time.Parse(store.DateLayout, b.EndsOn); err != nil {
			return start, end, false
		}
	}
	at := func(d time.Time, clock string) (time.Time, bool) {
		c, err := time.Parse("15:04", clock)
		if err != nil {
			return d, false
		}
		return d.Add(time.Duration(c.Hour())*time.Hour + time.Duration(c.Minute())*time.Minute), true
	}
	start, _ = at(day, b.StartTime)
	end, has := at(last, b.EndTime)
	if !has {
		end = last.AddDate(0, 0, 1)
	}
	if !end.After(start) {
		end = start.Add(time.Minute)
	}
	return start, end, true
}

// blockOverlaps reports whether a block shares any time with a date. Either
// that can't be read counts as overlapping, so a warning is never lost.
func blockOverlaps(e store.Event, b calendar.Block) bool {
	es, ee, ok1 := e.Bounds()
	bs, be, ok2 := blockBounds(b)
	if !ok1 || !ok2 {
		return true
	}
	return es.Before(be) && bs.Before(ee)
}

// clash is one calendar booking shown as a warning.
type clash struct {
	Busy    bool // someone else's private booking: no title
	Title   string
	Source  string // "Event planner", "Calendar"; empty for our own
	EventID int64  // one of our own dates, linked
	When    string
	Rooms   string
	Status  string // "tentative" or "pending"; empty when firm
}

func clockShort(t string) (string, string) {
	c, err := time.Parse("15:04", t)
	if err != nil {
		return "", ""
	}
	return c.Format("3:04"), c.Format("pm")
}

// clockRange is "2:00–4:00 pm", "11:00 am–1:00 pm", "from 2:00 pm" or "all day".
func clockRange(start, end string) string {
	s, sp := clockShort(start)
	e, ep := clockShort(end)
	switch {
	case s == "":
		return "all day"
	case e == "":
		return "from " + s + " " + sp
	case sp == ep:
		return s + "–" + e + " " + ep
	}
	return s + " " + sp + "–" + e + " " + ep
}

// blockWhen is when a block happens, with its date if showDate or if it
// runs over several days.
func blockWhen(b calendar.Block, showDate bool) string {
	day := func(d string) string {
		if t, err := time.Parse(store.DateLayout, d); err == nil {
			return t.Format("Jan 2")
		}
		return d
	}
	if b.EndsOn != "" && b.EndsOn != b.StartsOn {
		out := day(b.StartsOn)
		if s, sp := clockShort(b.StartTime); s != "" {
			out += " " + s + " " + sp
		}
		out += " – " + day(b.EndsOn)
		if e, ep := clockShort(b.EndTime); e != "" {
			out += " " + e + " " + ep
		}
		return out
	}
	out := clockRange(b.StartTime, b.EndTime)
	if showDate {
		out = day(b.StartsOn) + ", " + out
	}
	return out
}

func sourceLabel(src string) string {
	if src == "events" {
		return "Event planner"
	}
	return "Calendar"
}

func newClash(b calendar.Block, showDate bool) clash {
	c := clash{Busy: b.Busy, When: blockWhen(b, showDate), Rooms: strings.Join(b.Rooms, ", "), Status: b.Status}
	if b.Busy {
		return c
	}
	c.Title = b.Title
	if b.Ours() {
		c.EventID, _ = strconv.ParseInt(b.SourceID, 10, 64)
	} else {
		c.Source = sourceLabel(b.Source)
	}
	return c
}

// isOwn is whether a block is this date's own booking.
func isOwn(b calendar.Block, eventID int64) bool {
	return b.Ours() && b.SourceID == strconv.FormatInt(eventID, 10)
}

// scheduleClashes asks the calendar, in one call, who else has each date's
// rooms during that date. Only dates from today on are checked. Blocks
// outside a date's times and the date's own booking are left out.
func (s *Server) scheduleClashes(ctx context.Context, events []store.Event, rooms map[int64][]store.Room) (map[int64][]clash, error) {
	var names []string
	seen := map[string]bool{}
	from, to := "", ""
	for _, e := range events {
		if e.Date < s.today() || len(rooms[e.ID]) == 0 {
			continue
		}
		if from == "" || e.Date < from {
			from = e.Date
		}
		if e.Date > to {
			to = e.Date
		}
		for _, r := range rooms[e.ID] {
			if k := strings.ToLower(r.PlaceName); !seen[k] {
				seen[k] = true
				names = append(names, r.PlaceName)
			}
		}
	}
	out := map[int64][]clash{}
	if len(names) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// A date "until done" late in the evening can run into the next day.
	next, _ := time.Parse(store.DateLayout, to)
	blocks, err := s.Calendar().Availability(ctx, names, from, next.AddDate(0, 0, 1).Format(store.DateLayout))
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		if e.Date < s.today() {
			continue
		}
		for _, b := range blocks {
			if isOwn(b, e.ID) || !blockOverlaps(e, b) || !sharesRoom(rooms[e.ID], b.Rooms) {
				continue
			}
			out[e.ID] = append(out[e.ID], newClash(b, false))
		}
	}
	return out, nil
}

func sharesRoom(rooms []store.Room, names []string) bool {
	for _, r := range rooms {
		for _, n := range names {
			if strings.EqualFold(r.PlaceName, n) {
				return true
			}
		}
	}
	return false
}

// storedClashes decodes the conflicts the worker kept for a date: what the
// calendar said the last time the date was sent.
func (s *Server) storedClashes(eventID int64) []clash {
	raw, err := s.store.CalendarConflicts(eventID)
	if err != nil || raw == "" {
		return nil
	}
	var blocks []calendar.Block
	if json.Unmarshal([]byte(raw), &blocks) != nil {
		return nil
	}
	var out []clash
	for _, b := range blocks {
		if !isOwn(b, eventID) {
			out = append(out, newClash(b, false))
		}
	}
	return out
}
