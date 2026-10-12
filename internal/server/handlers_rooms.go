package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/production-planner/internal/store"
	"github.com/tylergoza/production-planner/internal/tracker"
)

// Rooms: the places a production usually uses (picked from the tracker or
// typed in) and the ones each date uses. A new date starts with the
// production's rooms.

// roomsData adds the production's rooms, and a tracker search when ?q= is
// set, for the rooms section of the production's edit page.
func (s *Server) roomsData(r *http.Request, p *store.Production, data map[string]any) error {
	rooms, err := s.store.ProductionRooms(p.ID)
	if err != nil {
		return err
	}
	t := s.Tracker()
	data["Rooms"] = rooms
	data["TrackerOn"] = t.Configured()
	if q := r.URL.Query().Get("q"); q != "" && t.Configured() {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		data["Q"] = q
		places, err := t.Rooms(ctx, q)
		if err != nil {
			data["SearchError"] = trackerErr(err)
		}
		have := map[int64]bool{}
		for _, rm := range rooms {
			have[rm.PlaceID] = true
		}
		var found []tracker.Place
		for _, pl := range places {
			if !have[pl.ID] {
				found = append(found, pl)
			}
		}
		data["Found"] = found
		data["FoundAll"] = len(places)
	}
	return nil
}

func roomsURL(id int64) string { return prodURL(id, "/edit#rooms") }

func (s *Server) handleProductionRoomCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	placeID, name := formInt(r, "place_id"), formStr(r, "place_name")
	fail := func(msg string) {
		s.setFlash(w, r, "error", msg)
		http.Redirect(w, r, roomsURL(p.ID), http.StatusSeeOther)
	}
	if placeID != 0 {
		t := s.Tracker()
		if !t.Configured() {
			fail("Connect the maintenance tracker on Connections to add its rooms.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		pl, err := t.Place(ctx, placeID)
		switch {
		case errors.Is(err, tracker.ErrNotFound):
			fail("That place isn't in the maintenance tracker.")
			return
		case err != nil:
			fail(trackerErr(err))
			return
		}
		if name = pl.Path; name == "" {
			name = pl.Name
		}
	}
	if name == "" {
		fail("Say which room.")
		return
	}
	if _, err := s.store.AddProductionRoom(p.ID, placeID, name); errors.Is(err, store.ErrRoomExists) {
		fail(name + " is already one of this production's rooms.")
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, roomsURL(p.ID), name+" added. New dates will use it.")
}

func (s *Server) handleProductionRoomDelete(w http.ResponseWriter, r *http.Request) {
	rm, err := s.store.GetProductionRoom(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p, ok := s.productionByID(w, r, rm.OwnerID)
	if !ok {
		return
	}
	if err := s.store.DeleteProductionRoom(rm.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, roomsURL(p.ID), rm.PlaceName+" removed. Dates that use it keep it.")
}

// A date's rooms ------------------------------------------------------------

// roomOption is a room a date can be given, shown as a checkbox.
type roomOption struct {
	Key     string // "placeID|name", what the checkbox sends
	Room    store.Room
	Checked bool
}

func roomKey(r store.Room) string { return strconv.FormatInt(r.PlaceID, 10) + "|" + r.PlaceName }

// roomOptions is the production's rooms followed by any other rooms the
// date has, with the date's ticked. A new date (current nil) ticks the
// production's rooms.
func roomOptions(prodRooms, current []store.Room, isNew bool) []roomOption {
	var out []roomOption
	idx := map[string]int{}
	add := func(r store.Room, checked bool) {
		r.ID, r.OwnerID = 0, 0
		k := roomKey(r)
		if i, ok := idx[k]; ok {
			out[i].Checked = out[i].Checked || checked
			return
		}
		idx[k] = len(out)
		out = append(out, roomOption{Key: k, Room: r, Checked: checked})
	}
	for _, r := range prodRooms {
		add(r, isNew)
	}
	for _, r := range current {
		add(r, true)
	}
	return out
}

// readEventRooms is the rooms ticked on a date's form, out of the options
// it was shown, plus one typed into "somewhere else". It ticks the options
// so a form sent back with errors keeps them.
func readEventRooms(r *http.Request, opts []roomOption) []store.Room {
	ticked := map[string]bool{}
	for _, k := range r.PostForm["room"] {
		ticked[k] = true
	}
	var out []store.Room
	for i := range opts {
		opts[i].Checked = ticked[opts[i].Key]
		if opts[i].Checked {
			out = append(out, opts[i].Room)
		}
	}
	if other := strings.TrimSpace(formStr(r, "room_other")); other != "" {
		out = append(out, store.Room{PlaceName: other})
	}
	return out
}
