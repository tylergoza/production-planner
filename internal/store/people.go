package store

import (
	"strings"
)

// Scenes -----------------------------------------------------------------

type Scene struct {
	ID           int64
	ProductionID int64
	Position     int
	Number       string
	Title        string
	Notes        string
}

// Label is how a scene is named in charts: its number, else its title.
func (sc Scene) Label() string {
	if sc.Number != "" {
		return sc.Number
	}
	return sc.Title
}

// FullLabel is the number and title together, e.g. "3 · The stable".
func (sc Scene) FullLabel() string {
	if sc.Number != "" && sc.Title != "" {
		return sc.Number + " · " + sc.Title
	}
	return sc.Label()
}

const sceneCols = `id, production_id, position, number, title, notes`

func scanScene(sc interface{ Scan(...any) error }) (Scene, error) {
	var s Scene
	err := sc.Scan(&s.ID, &s.ProductionID, &s.Position, &s.Number, &s.Title, &s.Notes)
	return s, err
}

func (s *Store) ListScenes(productionID int64) ([]Scene, error) {
	rows, err := s.DB.Query(`SELECT `+sceneCols+` FROM scenes WHERE production_id = ? ORDER BY position, id`, productionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Scene
	for rows.Next() {
		sc, err := scanScene(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) GetScene(id int64) (*Scene, error) {
	sc, err := scanScene(s.DB.QueryRow(`SELECT `+sceneCols+` FROM scenes WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &sc, nil
}

func (s *Store) CreateScene(sc *Scene) (int64, error) {
	pos, err := s.nextPosition("scenes", sc.ProductionID)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO scenes (production_id, position, number, title, notes) VALUES (?, ?, ?, ?, ?)`,
		sc.ProductionID, pos, sc.Number, sc.Title, sc.Notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateScene(sc *Scene) error {
	_, err := s.DB.Exec(`UPDATE scenes SET number = ?, title = ?, notes = ? WHERE id = ?`, sc.Number, sc.Title, sc.Notes, sc.ID)
	return err
}

func (s *Store) DeleteScene(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM scenes WHERE id = ?`, id)
	return err
}

// Cast -------------------------------------------------------------------

type CastMember struct {
	ID           int64
	ProductionID int64
	Position     int
	Character    string
	Person       string
	Notes        string
}

// Name is the character, or the person when there's no character.
func (c CastMember) Name() string {
	if c.Character != "" {
		return c.Character
	}
	return c.Person
}

// Sub is the person under the character's name, if both are set.
func (c CastMember) Sub() string {
	if c.Character != "" {
		return c.Person
	}
	return ""
}

// Label is the character and person together, e.g. "Mary (Jane Smith)".
func (c CastMember) Label() string {
	if sub := c.Sub(); sub != "" {
		return c.Name() + " (" + sub + ")"
	}
	return c.Name()
}

const castCols = `id, production_id, position, character, person, notes`

func scanCast(sc interface{ Scan(...any) error }) (CastMember, error) {
	var c CastMember
	err := sc.Scan(&c.ID, &c.ProductionID, &c.Position, &c.Character, &c.Person, &c.Notes)
	return c, err
}

func (s *Store) ListCast(productionID int64) ([]CastMember, error) {
	rows, err := s.DB.Query(`SELECT `+castCols+` FROM cast_members WHERE production_id = ? ORDER BY position, id`, productionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CastMember
	for rows.Next() {
		c, err := scanCast(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCastMember(id int64) (*CastMember, error) {
	c, err := scanCast(s.DB.QueryRow(`SELECT `+castCols+` FROM cast_members WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

func (s *Store) CreateCastMember(c *CastMember) (int64, error) {
	pos, err := s.nextPosition("cast_members", c.ProductionID)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO cast_members (production_id, position, character, person, notes) VALUES (?, ?, ?, ?, ?)`,
		c.ProductionID, pos, c.Character, c.Person, c.Notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateCastMember(c *CastMember) error {
	_, err := s.DB.Exec(`UPDATE cast_members SET character = ?, person = ?, notes = ? WHERE id = ?`, c.Character, c.Person, c.Notes, c.ID)
	return err
}

func (s *Store) DeleteCastMember(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM cast_members WHERE id = ?`, id)
	return err
}

// Pasted lists -----------------------------------------------------------

// ParsePairs reads pasted lines like "Mary - Jane Smith", "Mary: Jane",
// "Mary<TAB>Jane" or just "Narrator" into pairs. Blank lines are skipped.
func ParsePairs(text string) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		a, b := line, ""
		for _, sep := range []string{"\t", " - ", " – ", " — ", ": ", " | "} {
			if x, y, ok := strings.Cut(line, sep); ok {
				a, b = x, y
				break
			}
		}
		out = append(out, [2]string{strings.TrimSpace(a), strings.TrimSpace(b)})
	}
	return out
}

// AddCastList adds a pasted list of "Character - Person" lines.
func (s *Store) AddCastList(productionID int64, text string) (int, error) {
	n := 0
	for _, p := range ParsePairs(text) {
		if _, err := s.CreateCastMember(&CastMember{ProductionID: productionID, Character: p[0], Person: p[1]}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// AddSceneList adds a pasted list of "Number - Title" lines. A line with
// no separator is a title.
func (s *Store) AddSceneList(productionID int64, text string) (int, error) {
	n := 0
	for _, p := range ParsePairs(text) {
		sc := Scene{ProductionID: productionID, Number: p[0], Title: p[1]}
		if p[1] == "" {
			sc = Scene{ProductionID: productionID, Title: p[0]}
		}
		if _, err := s.CreateScene(&sc); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
