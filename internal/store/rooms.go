package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Room is a place a production or one of its dates uses (a
// production_rooms or event_rooms row). PlaceName keeps the tracker's path
// so pages work when the tracker can't be reached.
type Room struct {
	ID        int64
	OwnerID   int64 // the production or date it belongs to
	PlaceID   int64 // the tracker's place, 0 for a room typed in by hand
	PlaceName string
}

// FromTracker reports whether the room is a place in the tracker.
func (r Room) FromTracker() bool { return r.PlaceID != 0 }

// sameRoom reports whether two rooms are the same place: the same tracker
// place, or the same name when either was typed in by hand.
func sameRoom(a, b Room) bool {
	if a.PlaceID != 0 && b.PlaceID != 0 {
		return a.PlaceID == b.PlaceID
	}
	return strings.EqualFold(strings.TrimSpace(a.PlaceName), strings.TrimSpace(b.PlaceName))
}

// ErrRoomExists is returned when a room is already on the production's list.
var ErrRoomExists = errors.New("that room is already one of this production's rooms")

func listRooms(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]Room, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.OwnerID, &r.PlaceID, &r.PlaceName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// A production's rooms ---------------------------------------------------------

// ProductionRooms lists the rooms a production usually uses, in order.
func (s *Store) ProductionRooms(productionID int64) ([]Room, error) {
	return listRooms(s.DB, `SELECT id, production_id, COALESCE(place_id, 0), place_name FROM production_rooms
		WHERE production_id = ? ORDER BY position, id`, productionID)
}

// GetProductionRoom loads one of a production's rooms.
func (s *Store) GetProductionRoom(id int64) (*Room, error) {
	var r Room
	err := s.DB.QueryRow(`SELECT id, production_id, COALESCE(place_id, 0), place_name FROM production_rooms WHERE id = ?`, id).
		Scan(&r.ID, &r.OwnerID, &r.PlaceID, &r.PlaceName)
	if err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

// AddProductionRoom adds a room at the end of the production's list. A
// placeID of 0 is a room that isn't in the tracker. The same room can't be
// added twice (ErrRoomExists).
func (s *Store) AddProductionRoom(productionID, placeID int64, placeName string) (int64, error) {
	room := Room{PlaceID: placeID, PlaceName: strings.TrimSpace(placeName)}
	if room.PlaceName == "" {
		return 0, errors.New("a room needs a name")
	}
	have, err := s.ProductionRooms(productionID)
	if err != nil {
		return 0, err
	}
	for _, h := range have {
		if sameRoom(h, room) {
			return 0, ErrRoomExists
		}
	}
	pos, err := s.nextPosition("production_rooms", productionID)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO production_rooms (production_id, position, place_id, place_name) VALUES (?, ?, ?, ?)`,
		productionID, pos, nullInt(placeID), room.PlaceName)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteProductionRoom takes a room off the production's list. Dates that
// already use it keep it.
func (s *Store) DeleteProductionRoom(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM production_rooms WHERE id = ?`, id)
	return err
}

// A date's rooms ---------------------------------------------------------------

// EventRooms lists the rooms one date uses, in order.
func (s *Store) EventRooms(eventID int64) ([]Room, error) {
	return listRooms(s.DB, `SELECT id, event_id, COALESCE(place_id, 0), place_name FROM event_rooms
		WHERE event_id = ? ORDER BY position, id`, eventID)
}

// ScheduleRooms maps each of a production's dates to its rooms.
func (s *Store) ScheduleRooms(productionID int64) (map[int64][]Room, error) {
	list, err := listRooms(s.DB, `SELECT r.id, r.event_id, COALESCE(r.place_id, 0), r.place_name
		FROM event_rooms r JOIN events e ON e.id = r.event_id
		WHERE e.production_id = ? ORDER BY r.event_id, r.position, r.id`, productionID)
	if err != nil {
		return nil, err
	}
	out := map[int64][]Room{}
	for _, r := range list {
		out[r.OwnerID] = append(out[r.OwnerID], r)
	}
	return out, nil
}

// setEventRooms replaces a date's rooms with rooms (duplicates dropped).
// When nothing changes it writes nothing, so the calendar isn't sent the
// date again for no reason.
func setEventRooms(tx *sql.Tx, eventID int64, rooms []Room) error {
	var want []Room
	for _, r := range rooms {
		r.PlaceName = strings.TrimSpace(r.PlaceName)
		if r.PlaceName == "" {
			continue
		}
		dup := false
		for _, w := range want {
			dup = dup || sameRoom(w, r)
		}
		if !dup {
			want = append(want, r)
		}
	}
	have, err := listRooms(tx, `SELECT id, event_id, COALESCE(place_id, 0), place_name FROM event_rooms
		WHERE event_id = ? ORDER BY position, id`, eventID)
	if err != nil {
		return err
	}
	if len(have) == len(want) {
		same := true
		for i := range have {
			same = same && have[i].PlaceID == want[i].PlaceID && have[i].PlaceName == want[i].PlaceName
		}
		if same {
			return nil
		}
	}
	if _, err := tx.Exec(`DELETE FROM event_rooms WHERE event_id = ?`, eventID); err != nil {
		return err
	}
	for i, r := range want {
		if _, err := tx.Exec(`INSERT INTO event_rooms (event_id, position, place_id, place_name) VALUES (?, ?, ?, ?)`,
			eventID, i+1, nullInt(r.PlaceID), r.PlaceName); err != nil {
			return err
		}
	}
	return nil
}

// Double-booking -------------------------------------------------------------

// DateRef is another date, as named in a room clash warning.
type DateRef struct {
	EventID         int64
	ProductionID    int64
	ProductionTitle string
	Title           string // the date's label or kind
	When            string
	Room            string
}

// RoomConflicts maps each of a production's dates to the other dates in
// this app that use one of the same tracker places at an overlapping time,
// counting only productions viewer can see that aren't done or cancelled.
// Rooms typed in by hand never clash.
func (s *Store) RoomConflicts(productionID int64, viewer *User) (map[int64][]DateRef, error) {
	cond, args := visibleTo(viewer)
	rows, err := s.DB.Query(`SELECT me.id, me.date, me.time, me.end_time, r.place_name,
			o.id, o.production_id, o.kind, o.date, o.time, o.end_time, o.label, p.title
		FROM events me
		JOIN event_rooms mine ON mine.event_id = me.id AND mine.place_id IS NOT NULL
		JOIN event_rooms r ON r.place_id = mine.place_id AND r.event_id <> me.id
		JOIN events o ON o.id = r.event_id AND o.date = me.date
		JOIN productions p ON p.id = o.production_id
		WHERE me.production_id = ? AND p.status NOT IN ('done', 'cancelled') AND `+cond+`
		ORDER BY me.id, o.time, o.id`, append([]any{productionID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]DateRef{}
	for rows.Next() {
		var me, o Event
		var room, prodTitle string
		if err := rows.Scan(&me.ID, &me.Date, &me.Time, &me.EndTime, &room,
			&o.ID, &o.ProductionID, &o.Kind, &o.Date, &o.Time, &o.EndTime, &o.Label, &prodTitle); err != nil {
			return nil, err
		}
		if !me.Overlaps(o) {
			continue
		}
		ref := DateRef{EventID: o.ID, ProductionID: o.ProductionID, ProductionTitle: prodTitle, Title: o.Title(), When: o.When(), Room: room}
		dup := false
		for _, x := range out[me.ID] {
			dup = dup || (x.EventID == ref.EventID && x.Room == ref.Room)
		}
		if !dup {
			out[me.ID] = append(out[me.ID], ref)
		}
	}
	return out, rows.Err()
}
