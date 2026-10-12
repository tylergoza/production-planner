package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tylergoza/production-planner/internal/store"
)

// Calendar backfill ------------------------------------------------------
//
// One button on Connections sends every date from a year ago on. It only
// queues rows; the worker does the sending.

type calendarBackfillView struct {
	Waiting    int
	LastError  string
	LastSent   string
	Configured bool
	Count      int
	Message    string // for the confirm
}

func (s *Server) backfillFrom() time.Time { return s.todayTime().AddDate(-1, 0, 0) }

func lastSentText(raw string) string {
	if raw == "" {
		return "never"
	}
	return fmtTime(raw)
}

func (s *Server) calendarBackfillView() (calendarBackfillView, error) {
	var v calendarBackfillView
	st, err := s.store.CalendarOutbox()
	if err != nil {
		return v, err
	}
	from := s.backfillFrom()
	v.Waiting, v.LastError, v.LastSent = st.Waiting, st.LastError, lastSentText(st.LastSent)
	v.Configured = s.Calendar().Configured()
	if v.Configured {
		if v.Count, err = s.store.CountCalendarBackfill(from.Format(store.DateLayout)); err != nil {
			return v, err
		}
		v.Message = fmt.Sprintf("Send %s from %s on?", pluralize(v.Count, "date"), from.Format("Jan 2006"))
	}
	return v, nil
}

func (s *Server) handleCalendarBackfill(w http.ResponseWriter, r *http.Request) {
	back := "/admin/connections#calendar"
	if !s.Calendar().Configured() {
		s.setFlash(w, r, "error", "The calendar isn't connected, so there's nothing to send to.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	n, err := s.store.QueueCalendarBackfill(s.backfillFrom().Format(store.DateLayout))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.KickCalendar()
	s.setFlash(w, r, "success", fmt.Sprintf("Queued %s for the calendar.", pluralize(n, "date")))
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) handleCalendarStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.CalendarOutbox()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"waiting": st.Waiting, "last_error": st.LastError, "last_sent": lastSentText(st.LastSent),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
