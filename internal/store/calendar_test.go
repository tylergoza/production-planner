package store

import (
	"errors"
	"testing"
)

// outbox is the waiting rows as event ID → op, and their seqs.
func outbox(t *testing.T, st *Store) (map[int64]string, map[int64]int64) {
	t.Helper()
	rows, err := st.DB.Query(`SELECT event_id, op, seq FROM calendar_outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ops, seqs := map[int64]string{}, map[int64]int64{}
	for rows.Next() {
		var id, seq int64
		var op string
		rows.Scan(&id, &op, &seq)
		ops[id], seqs[id] = op, seq
	}
	return ops, seqs
}

func clearOutbox(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.DB.Exec(`DELETE FROM calendar_outbox`); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxQueuesChanges(t *testing.T) {
	st := openTest(t)
	pid, _ := st.CreateProduction(&Production{Title: "Musical"})
	rooms := []Room{{PlaceID: 41, PlaceName: "Lifecenter"}}
	e := Event{ProductionID: pid, Kind: "rehearsal", Date: "2026-11-01", Time: "18:00"}
	a, err := st.CreateEvent(&e, rooms)
	if err != nil {
		t.Fatal(err)
	}
	e2 := Event{ProductionID: pid, Kind: "performance", Date: "2026-11-08", Time: "18:00"}
	b, _ := st.CreateEvent(&e2, nil)
	if ops, _ := outbox(t, st); len(ops) != 2 || ops[a] != "put" || ops[b] != "put" {
		t.Fatalf("after create: %v", ops)
	}

	// Saving with nothing changed queues nothing.
	clearOutbox(t, st)
	got, _ := st.GetEvent(a)
	if err := st.UpdateEvent(got, rooms); err != nil {
		t.Fatal(err)
	}
	if ops, _ := outbox(t, st); len(ops) != 0 {
		t.Fatalf("unchanged save queued %v", ops)
	}
	// A new end time or a different room does.
	got.EndTime = "20:00"
	st.UpdateEvent(got, rooms)
	if ops, _ := outbox(t, st); ops[a] != "put" || len(ops) != 1 {
		t.Fatalf("end time: %v", ops)
	}
	clearOutbox(t, st)
	st.UpdateEvent(got, []Room{{PlaceName: "Kitchen"}})
	if ops, _ := outbox(t, st); ops[a] != "put" || len(ops) != 1 {
		t.Fatalf("rooms: %v", ops)
	}

	// The production's title, public flag, lock and status go to every date.
	for _, change := range []func(p *Production){
		func(p *Production) { p.Title = "Christmas Musical" },
		func(p *Production) { p.Public = true },
		func(p *Production) { p.Locked = true },
		func(p *Production) { p.Status = "cancelled" },
	} {
		clearOutbox(t, st)
		p, _ := st.GetProduction(pid)
		change(p)
		if err := st.UpdateProduction(p); err != nil {
			t.Fatal(err)
		}
		if ops, _ := outbox(t, st); len(ops) != 2 || ops[a] != "put" || ops[b] != "put" {
			t.Fatalf("production change queued %v", ops)
		}
	}
	// Notes and the like don't.
	clearOutbox(t, st)
	p, _ := st.GetProduction(pid)
	p.Notes = "Bring water"
	st.UpdateProduction(p)
	if ops, _ := outbox(t, st); len(ops) != 0 {
		t.Fatalf("notes queued %v", ops)
	}

	// Deleting a date, or the whole production, sends deletes.
	st.DeleteEvent(b)
	if ops, _ := outbox(t, st); ops[b] != "delete" {
		t.Fatalf("delete date: %v", ops)
	}
	clearOutbox(t, st)
	st.DeleteProduction(pid)
	if ops, _ := outbox(t, st); len(ops) != 1 || ops[a] != "delete" {
		t.Fatalf("delete production: %v", ops)
	}
}

func TestOutboxSentAndFailed(t *testing.T) {
	st := openTest(t)
	pid, _ := st.CreateProduction(&Production{Title: "Musical"})
	e := Event{ProductionID: pid, Kind: "rehearsal", Date: "2026-11-01"}
	id, _ := st.CreateEvent(&e, nil)
	jobs, err := st.DueCalendarJobs(10)
	if err != nil || len(jobs) != 1 || jobs[0].EventID != id {
		t.Fatalf("due: %v %v", jobs, err)
	}
	// A change while sending isn't lost when the old row is cleared.
	got, _ := st.GetEvent(id)
	got.Label = "Run-through"
	st.UpdateEvent(got, nil)
	st.CalendarSent(jobs[0])
	if ops, _ := outbox(t, st); ops[id] != "put" {
		t.Fatalf("newer change lost: %v", ops)
	}
	jobs, _ = st.DueCalendarJobs(10)
	st.CalendarFailed(jobs[0], "calendar down", 60e9)
	if due, _ := st.DueCalendarJobs(10); len(due) != 0 {
		t.Fatalf("failed row due again straight away: %v", due)
	}
	status, _ := st.CalendarOutbox()
	if status.Waiting != 1 || status.LastError != "calendar down" || status.LastSent == "" {
		t.Fatalf("status %+v", status)
	}
}

func TestCalendarBackfill(t *testing.T) {
	st := openTest(t)
	pid, _ := st.CreateProduction(&Production{Title: "Musical"})
	for _, d := range []string{"2024-12-01", "2025-11-01", "2026-12-01"} {
		e := Event{ProductionID: pid, Kind: "rehearsal", Date: d}
		st.CreateEvent(&e, nil)
	}
	clearOutbox(t, st)
	if n, _ := st.CountCalendarBackfill("2025-10-12"); n != 2 {
		t.Fatalf("count %d", n)
	}
	for i := 0; i < 2; i++ {
		if n, err := st.QueueCalendarBackfill("2025-10-12"); err != nil || n != 2 {
			t.Fatalf("queued %d, %v", n, err)
		}
	}
	if ops, _ := outbox(t, st); len(ops) != 2 {
		t.Fatalf("pressing twice: %v", ops)
	}
}

func TestProductionRooms(t *testing.T) {
	st := openTest(t)
	pid, _ := st.CreateProduction(&Production{Title: "Musical"})
	if _, err := st.AddProductionRoom(pid, 41, "Main › Sanctuary"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddProductionRoom(pid, 41, "Sanctuary"); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("same place twice: %v", err)
	}
	st.AddProductionRoom(pid, 0, "Pavilion")
	if _, err := st.AddProductionRoom(pid, 0, " pavilion "); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("same name twice: %v", err)
	}
	rooms, _ := st.ProductionRooms(pid)
	if len(rooms) != 2 || rooms[0].PlaceID != 41 || rooms[1].FromTracker() {
		t.Fatalf("rooms %+v", rooms)
	}
	st.Move("production_rooms", rooms[1].ID, true)
	if rooms, _ = st.ProductionRooms(pid); rooms[0].PlaceName != "Pavilion" {
		t.Fatalf("after move %+v", rooms)
	}
}

func TestEventBounds(t *testing.T) {
	at := func(time, end string) Event { return Event{Date: "2026-11-01", Time: time, EndTime: end} }
	s, e, _ := at("18:00", "").Bounds()
	if e.Sub(s) != UntilDone {
		t.Errorf("no end time lasts %v", e.Sub(s))
	}
	s, e, _ = at("", "").Bounds()
	if e.Sub(s).Hours() != 24 {
		t.Errorf("no start time lasts %v", e.Sub(s))
	}
	if !at("18:00", "").Overlaps(at("20:00", "21:00")) {
		t.Error("until done should run past 20:00")
	}
	if at("18:00", "19:00").Overlaps(at("19:00", "21:00")) {
		t.Error("back to back isn't a clash")
	}
	if !at("", "").Overlaps(at("09:00", "10:00")) {
		t.Error("all day clashes with everything that day")
	}
}

func TestRoomConflicts(t *testing.T) {
	st := openTest(t)
	admin := &User{ID: 1, IsAdmin: true}
	a, _ := st.CreateProduction(&Production{Title: "Musical"})
	b, _ := st.CreateProduction(&Production{Title: "Drama"})
	hall := []Room{{PlaceID: 41, PlaceName: "Lifecenter"}}
	mine := Event{ProductionID: a, Kind: "dress", Date: "2026-12-01", Time: "18:00", EndTime: "20:00"}
	me, _ := st.CreateEvent(&mine, hall)
	clash := Event{ProductionID: b, Kind: "rehearsal", Date: "2026-12-01", Time: "19:00"}
	other, _ := st.CreateEvent(&clash, hall)
	later := Event{ProductionID: b, Kind: "rehearsal", Date: "2026-12-01", Time: "20:00"}
	st.CreateEvent(&later, hall)
	typed := Event{ProductionID: b, Kind: "rehearsal", Date: "2026-12-01", Time: "18:00"}
	st.CreateEvent(&typed, []Room{{PlaceName: "Lifecenter"}})

	got, err := st.RoomConflicts(a, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[me]) != 1 || got[me][0].EventID != other || got[me][0].ProductionTitle != "Drama" {
		t.Fatalf("conflicts %+v", got)
	}
	// A cancelled production doesn't take the room.
	p, _ := st.GetProduction(b)
	p.Status = "cancelled"
	st.UpdateProduction(p)
	if got, _ := st.RoomConflicts(a, admin); len(got[me]) != 0 {
		t.Fatalf("cancelled still clashes: %+v", got)
	}
}
