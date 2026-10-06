package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Mics -------------------------------------------------------------------

var MicKinds = []string{"lav", "headset", "handheld", "other"}

var micKindLabels = map[string]string{"lav": "Lav", "headset": "Headset", "handheld": "Handheld", "other": "Other"}

func MicKindLabel(k string) string { return micKindLabels[k] }

type Mic struct {
	ID            int64
	ProductionID  int64
	Position      int
	Channel       string
	Name          string
	Kind          string
	TrackerItemID int64  // the item in the maintenance tracker, 0 if none
	TrackerUnit   string // the ID on its sticker
	Notes         string
}

// Label is the channel and name, e.g. "12 · Lav 3".
func (m Mic) Label() string {
	if m.Channel != "" {
		return m.Channel + " · " + m.Name
	}
	return m.Name
}

const micCols = `id, production_id, position, channel, name, kind, COALESCE(tracker_item_id, 0), tracker_unit, notes`

func scanMic(sc interface{ Scan(...any) error }) (Mic, error) {
	var m Mic
	err := sc.Scan(&m.ID, &m.ProductionID, &m.Position, &m.Channel, &m.Name, &m.Kind, &m.TrackerItemID, &m.TrackerUnit, &m.Notes)
	return m, err
}

func (s *Store) ListMics(productionID int64) ([]Mic, error) {
	rows, err := s.DB.Query(`SELECT `+micCols+` FROM mics WHERE production_id = ? ORDER BY position, id`, productionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mic
	for rows.Next() {
		m, err := scanMic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetMic(id int64) (*Mic, error) {
	m, err := scanMic(s.DB.QueryRow(`SELECT `+micCols+` FROM mics WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &m, nil
}

func (s *Store) CreateMic(m *Mic) (int64, error) {
	if m.Kind == "" {
		m.Kind = "lav"
	}
	pos, err := s.nextPosition("mics", m.ProductionID)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO mics (production_id, position, channel, name, kind, tracker_item_id, tracker_unit, notes) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ProductionID, pos, m.Channel, m.Name, m.Kind, nullInt(m.TrackerItemID), m.TrackerUnit, m.Notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateMic(m *Mic) error {
	_, err := s.DB.Exec(`UPDATE mics SET channel = ?, name = ?, kind = ?, notes = ? WHERE id = ?`, m.Channel, m.Name, m.Kind, m.Notes, m.ID)
	return err
}

func (s *Store) DeleteMic(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM mics WHERE id = ?`, id)
	return err
}

// HasTrackerMic reports whether the production already has this unit
// from the maintenance tracker.
func (s *Store) HasTrackerMic(productionID, itemID int64, unit string) bool {
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM mics WHERE production_id = ? AND tracker_item_id = ? AND tracker_unit = ?`, productionID, itemID, unit).Scan(&n)
	return n > 0
}

// Assignments --------------------------------------------------------------

// Cell is one square of the mic chart: a mic in a scene.
type Cell struct{ MicID, SceneID int64 }

// Assignments returns who has each mic in each scene.
func (s *Store) Assignments(productionID int64) (map[Cell]int64, error) {
	rows, err := s.DB.Query(`SELECT a.mic_id, a.scene_id, a.cast_id FROM mic_assignments a JOIN mics m ON m.id = a.mic_id WHERE m.production_id = ?`, productionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Cell]int64{}
	for rows.Next() {
		var c Cell
		var castID int64
		if err := rows.Scan(&c.MicID, &c.SceneID, &castID); err != nil {
			return nil, err
		}
		out[c] = castID
	}
	return out, rows.Err()
}

// SaveAssignments replaces the whole mic chart. Cells whose mic, scene or
// cast member isn't in the production are an error.
func (s *Store) SaveAssignments(productionID int64, cells map[Cell]int64) error {
	ids := func(table string) (map[int64]bool, error) {
		rows, err := s.DB.Query(`SELECT id FROM `+table+` WHERE production_id = ?`, productionID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[int64]bool{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out[id] = true
		}
		return out, rows.Err()
	}
	mics, err := ids("mics")
	if err != nil {
		return err
	}
	scenes, err := ids("scenes")
	if err != nil {
		return err
	}
	cast, err := ids("cast_members")
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM mic_assignments WHERE mic_id IN (SELECT id FROM mics WHERE production_id = ?)`, productionID); err != nil {
		return err
	}
	for c, castID := range cells {
		if castID == 0 {
			continue
		}
		if !mics[c.MicID] || !scenes[c.SceneID] || !cast[castID] {
			return fmt.Errorf("mic %d, scene %d, cast %d: %w", c.MicID, c.SceneID, castID, ErrNotFound)
		}
		if _, err := tx.Exec(`INSERT INTO mic_assignments (mic_id, scene_id, cast_id) VALUES (?, ?, ?)`, c.MicID, c.SceneID, castID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Snapshots ----------------------------------------------------------------

// ChartSnapshot is the mic chart as printed: scenes across, mics down and
// who has each mic in each scene. Finalized versions are kept as these.
type ChartSnapshot struct {
	Scenes []Head            `json:"scenes"`
	Mics   []Head            `json:"mics"`
	Cells  map[string]Holder `json:"cells"` // key: CellKey(mic, scene)
}

// Head is a scene or mic heading.
type Head struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
	Sub   string `json:"sub,omitempty"` // scene title, mic kind
}

// Holder is who has a mic in a scene.
type Holder struct {
	CastID int64  `json:"c"`
	Name   string `json:"n"`
	Sub    string `json:"s,omitempty"` // the person, under the character
}

func CellKey(micID, sceneID int64) string {
	return strconv.FormatInt(micID, 10) + ":" + strconv.FormatInt(sceneID, 10)
}

// CurrentChart builds the snapshot of the chart as it is now.
func (s *Store) CurrentChart(productionID int64) (*ChartSnapshot, error) {
	scenes, err := s.ListScenes(productionID)
	if err != nil {
		return nil, err
	}
	mics, err := s.ListMics(productionID)
	if err != nil {
		return nil, err
	}
	cast, err := s.ListCast(productionID)
	if err != nil {
		return nil, err
	}
	assigned, err := s.Assignments(productionID)
	if err != nil {
		return nil, err
	}
	byID := map[int64]CastMember{}
	for _, c := range cast {
		byID[c.ID] = c
	}
	snap := &ChartSnapshot{Scenes: []Head{}, Mics: []Head{}, Cells: map[string]Holder{}}
	for _, sc := range scenes {
		h := Head{ID: sc.ID, Label: sc.Label()}
		if sc.Number != "" {
			h.Sub = sc.Title
		}
		snap.Scenes = append(snap.Scenes, h)
	}
	for _, m := range mics {
		snap.Mics = append(snap.Mics, Head{ID: m.ID, Label: m.Label(), Sub: MicKindLabel(m.Kind)})
	}
	for cell, castID := range assigned {
		c := byID[castID]
		snap.Cells[CellKey(cell.MicID, cell.SceneID)] = Holder{CastID: c.ID, Name: c.Name(), Sub: c.Sub()}
	}
	return snap, nil
}

// SameAs reports whether two snapshots would print the same chart.
func (a *ChartSnapshot) SameAs(b *ChartSnapshot) bool {
	if a == nil || b == nil {
		return a == b
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// Versions -----------------------------------------------------------------

type ChartVersion struct {
	ID           int64
	ProductionID int64
	Version      int
	Note         string
	FinalizedBy  string
	FinalizedAt  string
	Snapshot     ChartSnapshot
}

func (s *Store) ListChartVersions(productionID int64) ([]ChartVersion, error) {
	rows, err := s.DB.Query(`SELECT id, production_id, version, note, finalized_by, finalized_at, snapshot
		FROM mic_chart_versions WHERE production_id = ? ORDER BY version DESC`, productionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChartVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanVersion(sc interface{ Scan(...any) error }) (ChartVersion, error) {
	var v ChartVersion
	var snap string
	if err := sc.Scan(&v.ID, &v.ProductionID, &v.Version, &v.Note, &v.FinalizedBy, &v.FinalizedAt, &snap); err != nil {
		return v, err
	}
	err := json.Unmarshal([]byte(snap), &v.Snapshot)
	return v, err
}

// GetChartVersion returns one version, or the latest when version is 0.
// With no versions at all it returns ErrNotFound.
func (s *Store) GetChartVersion(productionID int64, version int) (*ChartVersion, error) {
	q := `SELECT id, production_id, version, note, finalized_by, finalized_at, snapshot FROM mic_chart_versions WHERE production_id = ?`
	args := []any{productionID}
	if version > 0 {
		q += ` AND version = ?`
		args = append(args, version)
	}
	v, err := scanVersion(s.DB.QueryRow(q+` ORDER BY version DESC LIMIT 1`, args...))
	if err != nil {
		return nil, notFound(err)
	}
	return &v, nil
}

// LatestChartVersion is the last finalized chart, or nil if there's none.
func (s *Store) LatestChartVersion(productionID int64) (*ChartVersion, error) {
	v, err := s.GetChartVersion(productionID, 0)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return v, err
}

// ErrUnchanged is returned when finalizing a chart that's the same as
// the last final version.
var ErrUnchanged = errors.New("the chart hasn't changed since the last final version")

// FinalizeChart saves the chart as it is now as the next final version.
func (s *Store) FinalizeChart(productionID int64, note, by string) (int, error) {
	cur, err := s.CurrentChart(productionID)
	if err != nil {
		return 0, err
	}
	last, err := s.LatestChartVersion(productionID)
	if err != nil {
		return 0, err
	}
	if last != nil && cur.SameAs(&last.Snapshot) {
		return 0, ErrUnchanged
	}
	body, err := json.Marshal(cur)
	if err != nil {
		return 0, err
	}
	version := 1
	if last != nil {
		version = last.Version + 1
	}
	_, err = s.DB.Exec(`INSERT INTO mic_chart_versions (production_id, version, snapshot, note, finalized_by) VALUES (?, ?, ?, ?, ?)`,
		productionID, version, string(body), strings.TrimSpace(note), by)
	return version, err
}

// ChartStatus says where the chart stands against its final versions.
type ChartStatus struct {
	Version int  // latest final version, 0 if never finalized
	Changed bool // the chart differs from that version
	Last    *ChartVersion
}

// Label is e.g. "Draft", "Final v2" or "Changed since v2".
func (c ChartStatus) Label() string {
	switch {
	case c.Version == 0:
		return "Draft"
	case c.Changed:
		return "Changed since v" + strconv.Itoa(c.Version)
	default:
		return "Final v" + strconv.Itoa(c.Version)
	}
}

// Key is a CSS-friendly name for the status: draft, final or changed.
func (c ChartStatus) Key() string {
	switch {
	case c.Version == 0:
		return "draft"
	case c.Changed:
		return "changed"
	default:
		return "final"
	}
}

func (s *Store) ChartStatus(productionID int64) (ChartStatus, *ChartSnapshot, error) {
	cur, err := s.CurrentChart(productionID)
	if err != nil {
		return ChartStatus{}, nil, err
	}
	last, err := s.LatestChartVersion(productionID)
	if err != nil || last == nil {
		return ChartStatus{}, cur, err
	}
	return ChartStatus{Version: last.Version, Changed: !cur.SameAs(&last.Snapshot), Last: last}, cur, nil
}

// Grid -----------------------------------------------------------------------

// Grid is a chart ready to show: rows of mics across the scenes, with
// mic swaps between scenes, clashes, and what changed from a base version.
type Grid struct {
	Scenes    []Head
	Rows      []GridRow
	Swaps     []SceneSwaps
	People    []PersonMics
	Conflicts []string
	Changed   int // cells that differ from the base version
}

type GridRow struct {
	Mic   Head
	Cells []GridCell
}

type GridCell struct {
	SceneID  int64
	Holder   Holder
	Assigned bool
	Swap     bool   // someone else had this mic in the scene before
	Conflict bool   // this person has another mic in this scene too
	Changed  bool   // differs from the base version
	Was      string // who had it in the base version, when changed
}

// SceneSwaps lists the mics that change hands going into a scene.
type SceneSwaps struct {
	Scene Head
	Moves []Swap
}

type Swap struct {
	Mic      Head
	From, To string // "" when nobody had it / nobody gets it
}

// PersonMics is one person's mics through the show.
type PersonMics struct {
	Name, Sub string
	Uses      []MicUse
}

type MicUse struct {
	Mic    Head
	Scenes string // e.g. "scenes 1–3, 5"
}

func holderName(h Holder) string {
	if h.Sub != "" {
		return h.Name + " (" + h.Sub + ")"
	}
	return h.Name
}

// BuildGrid lays out a snapshot. With a base version, changed cells are
// marked.
func BuildGrid(cur *ChartSnapshot, base *ChartSnapshot) Grid {
	g := Grid{Scenes: cur.Scenes}
	// Who has which mics in each scene, to find clashes.
	type pair struct {
		scene int
		cast  int64
	}
	micsOf := map[pair][]int{}
	for mi, m := range cur.Mics {
		for si, sc := range cur.Scenes {
			if h, ok := cur.Cells[CellKey(m.ID, sc.ID)]; ok {
				micsOf[pair{si, h.CastID}] = append(micsOf[pair{si, h.CastID}], mi)
			}
		}
	}
	for si, sc := range cur.Scenes {
		for mi, m := range cur.Mics {
			h, ok := cur.Cells[CellKey(m.ID, sc.ID)]
			if !ok {
				continue
			}
			if others := micsOf[pair{si, h.CastID}]; len(others) > 1 && others[0] == mi {
				var labels []string
				for _, o := range others {
					labels = append(labels, cur.Mics[o].Label)
				}
				g.Conflicts = append(g.Conflicts, fmt.Sprintf("Scene %s: %s has %s", sc.Label, holderName(h), joinAnd(labels)))
			}
		}
	}

	swaps := make([][]Swap, len(cur.Scenes))
	for _, m := range cur.Mics {
		row := GridRow{Mic: m}
		var prev Holder
		var prevOK bool
		for si, sc := range cur.Scenes {
			key := CellKey(m.ID, sc.ID)
			h, ok := cur.Cells[key]
			cell := GridCell{SceneID: sc.ID, Holder: h, Assigned: ok}
			if si > 0 && (ok != prevOK || h.CastID != prev.CastID) {
				cell.Swap = ok
				sw := Swap{Mic: m}
				if prevOK {
					sw.From = holderName(prev)
				}
				if ok {
					sw.To = holderName(h)
				}
				swaps[si] = append(swaps[si], sw)
			}
			if ok {
				cell.Conflict = len(micsOf[pair{si, h.CastID}]) > 1
			}
			if base != nil {
				was, wasOK := base.Cells[key]
				if wasOK != ok || was.CastID != h.CastID || was.Name != h.Name || was.Sub != h.Sub {
					cell.Changed = true
					if wasOK {
						cell.Was = holderName(was)
					}
					g.Changed++
				}
			}
			row.Cells = append(row.Cells, cell)
			prev, prevOK = h, ok
		}
		g.Rows = append(g.Rows, row)
	}
	for si, moves := range swaps {
		if len(moves) > 0 {
			g.Swaps = append(g.Swaps, SceneSwaps{Scene: cur.Scenes[si], Moves: moves})
		}
	}
	g.People = people(cur)
	return g
}

// people lists each person's mics and the scenes they have them in,
// in order of who's on first.
func people(cur *ChartSnapshot) []PersonMics {
	type use struct {
		mic    int
		scenes []int
	}
	var order []int64
	who := map[int64]Holder{}
	uses := map[int64][]*use{}
	for si, sc := range cur.Scenes {
		for mi, m := range cur.Mics {
			h, ok := cur.Cells[CellKey(m.ID, sc.ID)]
			if !ok {
				continue
			}
			if _, seen := who[h.CastID]; !seen {
				order = append(order, h.CastID)
				who[h.CastID] = h
			}
			var u *use
			for _, x := range uses[h.CastID] {
				if x.mic == mi {
					u = x
				}
			}
			if u == nil {
				u = &use{mic: mi}
				uses[h.CastID] = append(uses[h.CastID], u)
			}
			u.scenes = append(u.scenes, si)
		}
	}
	var out []PersonMics
	for _, id := range order {
		h := who[id]
		p := PersonMics{Name: h.Name, Sub: h.Sub}
		for _, u := range uses[id] {
			word := "scenes "
			if len(u.scenes) == 1 {
				word = "scene "
			}
			p.Uses = append(p.Uses, MicUse{Mic: cur.Mics[u.mic], Scenes: word + sceneRanges(cur.Scenes, u.scenes)})
		}
		out = append(out, p)
	}
	return out
}

// sceneRanges turns scene indexes into "1–3, 5" using their labels.
func sceneRanges(scenes []Head, idx []int) string {
	var parts []string
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && idx[j+1] == idx[j]+1 {
			j++
		}
		if j == i {
			parts = append(parts, scenes[idx[i]].Label)
		} else {
			parts = append(parts, scenes[idx[i]].Label+"–"+scenes[idx[j]].Label)
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}
