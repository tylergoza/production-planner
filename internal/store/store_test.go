package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestParsePairs(t *testing.T) {
	got := ParsePairs("Mary - Ava Brooks\n  Joseph:  Eli \nNarrator\n\nShepherd 1\tNoah\nAct 1 – Scene 2")
	want := [][2]string{{"Mary", "Ava Brooks"}, {"Joseph", "Eli"}, {"Narrator", ""}, {"Shepherd 1", "Noah"}, {"Act 1", "Scene 2"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// chartFixture makes a production with scenes 1–4, three cast members and
// two mics.
func chartFixture(t *testing.T) (*Store, int64, []Scene, []CastMember, []Mic) {
	st := openTest(t)
	id, err := st.CreateProduction(&Production{Title: "Show"})
	if err != nil {
		t.Fatal(err)
	}
	st.AddSceneList(id, "1\n2\n3\n4")
	// A line with no separator is a title; give these numbers instead.
	scenes, _ := st.ListScenes(id)
	for i := range scenes {
		scenes[i].Number, scenes[i].Title = scenes[i].Title, ""
		st.UpdateScene(&scenes[i])
	}
	scenes, _ = st.ListScenes(id)
	st.AddCastList(id, "Mary - Ava\nJoseph - Eli\nNarrator")
	cast, _ := st.ListCast(id)
	for _, name := range []string{"Lav 1", "Lav 2"} {
		st.CreateMic(&Mic{ProductionID: id, Name: name})
	}
	mics, _ := st.ListMics(id)
	return st, id, scenes, cast, mics
}

func TestBuildGrid(t *testing.T) {
	st, id, scenes, cast, mics := chartFixture(t)
	mary, joseph, narrator := cast[0].ID, cast[1].ID, cast[2].ID
	cells := map[Cell]int64{
		// Lav 1: Mary in 1–2, nobody in 3, Mary again in 4.
		{mics[0].ID, scenes[0].ID}: mary, {mics[0].ID, scenes[1].ID}: mary, {mics[0].ID, scenes[3].ID}: mary,
		// Lav 2: Joseph in 1, Narrator in 2–3, and Mary in 4 (a clash with Lav 1).
		{mics[1].ID, scenes[0].ID}: joseph, {mics[1].ID, scenes[1].ID}: narrator, {mics[1].ID, scenes[2].ID}: narrator,
		{mics[1].ID, scenes[3].ID}: mary,
	}
	if err := st.SaveAssignments(id, cells); err != nil {
		t.Fatal(err)
	}
	cur, err := st.CurrentChart(id)
	if err != nil {
		t.Fatal(err)
	}
	g := BuildGrid(cur, nil)

	if len(g.Rows) != 2 || len(g.Rows[0].Cells) != 4 {
		t.Fatalf("grid shape: %+v", g)
	}
	lav1, lav2 := g.Rows[0].Cells, g.Rows[1].Cells
	if lav1[1].Swap || !lav1[3].Swap || lav1[2].Assigned {
		t.Errorf("Lav 1 swaps: %+v", lav1)
	}
	if !lav2[1].Swap || lav2[2].Swap || !lav2[3].Swap {
		t.Errorf("Lav 2 swaps: %+v", lav2)
	}
	if !lav1[3].Conflict || !lav2[3].Conflict || lav1[0].Conflict {
		t.Error("Mary on both mics in scene 4 should be a clash")
	}
	if len(g.Conflicts) != 1 || g.Conflicts[0] != "Scene 4: Mary (Ava) has Lav 1 and Lav 2" {
		t.Errorf("conflicts: %q", g.Conflicts)
	}

	// Swaps: into 2 (Lav 2 Joseph → Narrator), into 3 (Lav 1 off Mary),
	// into 4 (Lav 1 on Mary, Lav 2 Narrator → Mary).
	if len(g.Swaps) != 3 || g.Swaps[0].Scene.Label != "2" || g.Swaps[1].Moves[0].To != "" || len(g.Swaps[2].Moves) != 2 {
		t.Fatalf("swaps: %+v", g.Swaps)
	}
	if m := g.Swaps[0].Moves[0]; m.From != "Joseph (Eli)" || m.To != "Narrator" {
		t.Errorf("swap into 2: %+v", m)
	}

	// People, in order of who's on first, with scene ranges.
	var lines []string
	for _, p := range g.People {
		var uses []string
		for _, u := range p.Uses {
			uses = append(uses, u.Mic.Label+" "+u.Scenes)
		}
		lines = append(lines, p.Name+": "+strings.Join(uses, "; "))
	}
	want := "Mary: Lav 1 scenes 1–2, 4; Lav 2 scene 4|Joseph: Lav 2 scene 1|Narrator: Lav 2 scenes 2–3"
	if got := strings.Join(lines, "|"); got != want {
		t.Errorf("people:\n got %s\nwant %s", got, want)
	}
}

func TestChartVersions(t *testing.T) {
	st, id, scenes, cast, mics := chartFixture(t)
	status, _, err := st.ChartStatus(id)
	if err != nil || status.Label() != "Draft" {
		t.Fatalf("new chart: %v %v", status.Label(), err)
	}
	cells := map[Cell]int64{{mics[0].ID, scenes[0].ID}: cast[0].ID}
	st.SaveAssignments(id, cells)
	if v, err := st.FinalizeChart(id, "first", "Pat"); err != nil || v != 1 {
		t.Fatalf("finalize: %d %v", v, err)
	}
	if _, err := st.FinalizeChart(id, "", "Pat"); !errors.Is(err, ErrUnchanged) {
		t.Errorf("finalizing twice should be refused, got %v", err)
	}
	if status, _, _ = st.ChartStatus(id); status.Label() != "Final v1" {
		t.Errorf("got %s", status.Label())
	}

	// Renaming a character changes the printed chart.
	cast[0].Character = "Mother Mary"
	st.UpdateCastMember(&cast[0])
	status, cur, _ := st.ChartStatus(id)
	if status.Label() != "Changed since v1" {
		t.Errorf("got %s", status.Label())
	}
	if g := BuildGrid(cur, &status.Last.Snapshot); g.Changed != 1 || g.Rows[0].Cells[0].Was != "Mary (Ava)" {
		t.Errorf("changed cells: %+v", g.Rows[0].Cells[0])
	}
	if v, _ := st.FinalizeChart(id, "", "Pat"); v != 2 {
		t.Errorf("second version: %d", v)
	}
	versions, _ := st.ListChartVersions(id)
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Note != "first" || versions[1].Snapshot.Cells[CellKey(mics[0].ID, scenes[0].ID)].Name != "Mary" {
		t.Errorf("versions keep the chart as it was: %+v", versions)
	}

	// Deleting a cast member takes them off the chart.
	st.DeleteCastMember(cast[0].ID)
	if a, _ := st.Assignments(id); len(a) != 0 {
		t.Errorf("assignments left: %v", a)
	}
}

func TestMove(t *testing.T) {
	st, id, _, _, _ := chartFixture(t)
	scenes, _ := st.ListScenes(id)
	st.Move("scenes", scenes[0].ID, true) // already first: nothing happens
	st.Move("scenes", scenes[0].ID, false)
	got, _ := st.ListScenes(id)
	if got[0].ID != scenes[1].ID || got[1].ID != scenes[0].ID {
		t.Errorf("order after moving down: %v", got)
	}
	if err := st.Move("users", 1, true); err == nil {
		t.Error("only lists can be reordered")
	}
}

func TestSummary(t *testing.T) {
	st, id, _, _, _ := chartFixture(t)
	for _, e := range []Event{{Kind: "rehearsal", Date: "2026-01-01"}, {Kind: "performance", Date: "2026-03-01"}, {Kind: "dress", Date: "2026-02-27", Time: "18:00"}} {
		e.ProductionID = id
		st.CreateEvent(&e)
	}
	st.CreatePrep(&PrepItem{ProductionID: id, Department: "props", Name: "A", Status: "ready"})
	st.CreatePrep(&PrepItem{ProductionID: id, Department: "props", Name: "B"})
	st.CreateNeed(&Need{ProductionID: id, Name: "Chairs", Quantity: 10, Status: "gathered"})
	p, _ := st.GetProduction(id)
	sum, err := st.Summary(p, "2026-02-01")
	if err != nil {
		t.Fatal(err)
	}
	if sum.FirstDate != "2026-03-01" || sum.NextEvent == nil || sum.NextEvent.Kind != "dress" || sum.Prep != (Tally{1, 2}) || sum.Needs != (Tally{1, 1}) || sum.Mics != 2 {
		t.Errorf("summary: %+v next %+v", sum, sum.NextEvent)
	}
	if sum.Prep.Percent() != 50 {
		t.Errorf("percent: %d", sum.Prep.Percent())
	}
}
