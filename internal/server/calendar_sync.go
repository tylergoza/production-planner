package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tylergoza/production-planner/internal/calendar"
	"github.com/tylergoza/production-planner/internal/store"
)

// Sending dates to the church calendar (CALENDAR_PLAN.md). Changes queue
// in the calendar_outbox table in the same transaction as the change; the
// worker here sends them every 30 seconds and straight after a save.
// Saving a date never waits on the calendar.

// Calendar is the church calendar client; check Configured before use.
func (s *Server) Calendar() *calendar.Client { return s.calendar.Load() }

// KickCalendar asks the worker to send what's waiting now rather than at
// its next tick. It never blocks.
func (s *Server) KickCalendar() {
	select {
	case s.calendarKick <- struct{}{}:
	default:
	}
}

// RunCalendarWorker sends waiting changes until ctx is done.
func (s *Server) RunCalendarWorker(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		s.sendCalendarJobs(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.calendarKick:
		}
	}
}

// calendarRetry is how long to wait after the nth failure in a row.
func calendarRetry(attempts int) time.Duration {
	switch attempts {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 30 * time.Minute
	}
	return time.Hour
}

// sendCalendarJobs sends everything that's due. If the calendar can't be
// reached it stops for now; the rest would fail the same way.
func (s *Server) sendCalendarJobs(ctx context.Context) {
	c := s.Calendar()
	if !c.Configured() {
		return
	}
	for ctx.Err() == nil {
		jobs, err := s.store.DueCalendarJobs(50)
		if err != nil {
			s.log.Error("calendar outbox", "err", err)
			return
		}
		if len(jobs) == 0 {
			return
		}
		for _, j := range jobs {
			err := s.sendCalendarJob(ctx, c, j)
			if ctx.Err() != nil {
				return // shutting down: the row stays for next time
			}
			if err == nil {
				if err := s.store.CalendarSent(j); err != nil {
					s.log.Error("calendar outbox", "err", err)
					return
				}
				continue
			}
			s.log.Warn("calendar send failed", "date", j.EventID, "op", j.Op, "attempt", j.Attempts+1, "err", err)
			if err := s.store.CalendarFailed(j, err.Error(), calendarRetry(j.Attempts+1)); err != nil {
				s.log.Error("calendar outbox", "err", err)
				return
			}
			var answered *calendar.Error
			if !errors.As(err, &answered) {
				return
			}
		}
	}
}

func (s *Server) sendCalendarJob(ctx context.Context, c *calendar.Client, j store.CalendarJob) error {
	if j.Op == "delete" {
		return c.DeleteBooking(ctx, j.EventID)
	}
	e, p, rooms, err := s.store.OutboxEvent(j.EventID)
	if errors.Is(err, store.ErrNotFound) {
		return c.DeleteBooking(ctx, j.EventID) // deleted since it was queued
	}
	if err != nil {
		return err
	}
	res, err := c.PutBooking(ctx, e.ID, s.calendarBooking(e, p, rooms))
	if err != nil {
		return err
	}
	// Keep the answer for the schedule page; a failure here never fails the send.
	kept := ""
	if len(res.Conflicts) > 0 {
		s.log.Info("calendar reports room clashes", "date", e.ID, "conflicts", len(res.Conflicts))
		if b, err := json.Marshal(res.Conflicts); err == nil {
			kept = string(b)
		}
	}
	if err := s.store.SetCalendarConflicts(e.ID, kept); err != nil {
		s.log.Warn("calendar conflicts not kept", "date", e.ID, "err", err)
	}
	return nil
}

// calendarBooking is what the calendar is sent for a date. Never notes,
// cast, mics or readiness; for a locked production not even its title,
// kind or description, only rooms and times. Performances of a public
// production are public; rehearsals, tech and dress are always private.
func (s *Server) calendarBooking(e *store.Event, p *store.Production, rooms []store.Room) calendar.Booking {
	b := calendar.Booking{StartsOn: e.Date, Visibility: "private", Status: "confirmed", Rooms: []calendar.Room{}}
	if e.Time != "" {
		// No end time means "until done": store.UntilDone after the start,
		// which may run past midnight.
		if start, end, ok := e.Bounds(); ok {
			b.StartTime = start.Format("15:04")
			b.EndTime = end.Format("15:04")
			if d := end.Format(store.DateLayout); d != e.Date {
				b.EndsOn = d
			}
		}
	}
	switch {
	case p.Locked:
		b.Visibility = "locked"
	default:
		b.Title = p.Title + ": " + e.Title()
		b.Kind = store.EventKindLabel(e.Kind)
		if p.Public && e.Kind == "performance" {
			b.Visibility = "public"
			b.Description = p.Description
		}
	}
	switch {
	case p.Status == "cancelled":
		b.Status = "cancelled"
	case e.Tentative:
		b.Status = "tentative"
	}
	if base := s.configuredBaseURL(); base != "" {
		b.URL = base + prodURL(p.ID, "/schedule")
	}
	// The calendar keeps one row per room name.
	seen := map[string]bool{}
	for _, r := range rooms {
		if k := strings.ToLower(r.PlaceName); !seen[k] {
			seen[k] = true
			b.Rooms = append(b.Rooms, calendar.Room{PlaceID: r.PlaceID, Name: r.PlaceName})
		}
	}
	return b
}
