package main

import (
	"time"

	"github.com/tylergoza/production-planner/internal/store"
)

// seedDemo adds a sample Christmas musical: dates, scenes, cast, mics and
// a mic chart, things to get ready, and things needed.
func seedDemo(st *store.Store) error {
	today := time.Now()
	day := func(days int) string { return today.AddDate(0, 0, days).Format(store.DateLayout) }

	p := store.Production{
		Title: "The Night the Stars Sang", Kind: "Christmas musical", Status: "rehearsing",
		Venue: "Sanctuary", Director: "Pat Morgan",
		Description: "Children's and youth Christmas musical, about 45 minutes.",
	}
	id, err := st.CreateProduction(&p)
	if err != nil {
		return err
	}

	for _, e := range []store.Event{
		{Kind: "rehearsal", Date: day(-7), Time: "16:00"},
		{Kind: "rehearsal", Date: day(0), Time: "16:00"},
		{Kind: "rehearsal", Date: day(7), Time: "16:00"},
		{Kind: "tech", Date: day(12), Time: "18:30", Notes: "Lights, sound and slides only; no costumes."},
		{Kind: "dress", Date: day(14), Time: "17:00"},
		{Kind: "performance", Date: day(16), Time: "18:00", Label: "Sunday evening"},
		{Kind: "performance", Date: day(17), Time: "10:30", Label: "Morning service"},
	} {
		e.ProductionID = id
		if _, err := st.CreateEvent(&e); err != nil {
			return err
		}
	}

	if _, err := st.AddSceneList(id, "1 - The shepherds' field\n2 - Gabriel visits Mary\n3 - The road to Bethlehem\n4 - No room at the inn\n5 - The stable\n6 - The stars sing (finale)"); err != nil {
		return err
	}
	if _, err := st.AddCastList(id, "Narrator - Jordan Lee\nMary - Ava Brooks\nJoseph - Eli Carter\nGabriel - Maya Patel\nInnkeeper - Sam Rivera\nShepherd 1 - Noah Kim\nShepherd 2 - Lily Chen\nSoloist - Grace Wilson"); err != nil {
		return err
	}
	for i, name := range []string{"Lav 1", "Lav 2", "Lav 3", "Lav 4", "Headset 1", "Handheld A"} {
		kind := "lav"
		switch {
		case name == "Headset 1":
			kind = "headset"
		case name == "Handheld A":
			kind = "handheld"
		}
		m := store.Mic{ProductionID: id, Channel: string(rune('1' + i)), Name: name, Kind: kind}
		if _, err := st.CreateMic(&m); err != nil {
			return err
		}
	}

	scenes, _ := st.ListScenes(id)
	cast, _ := st.ListCast(id)
	mics, _ := st.ListMics(id)
	who := map[string]int64{}
	for _, c := range cast {
		who[c.Character] = c.ID
	}
	// Who has each mic, scene by scene ("" = nobody).
	plan := [][]string{
		{"Narrator", "Narrator", "Narrator", "Narrator", "Narrator", "Narrator"}, // Lav 1
		{"Shepherd 1", "Mary", "Mary", "Mary", "Mary", "Mary"},                   // Lav 2: swap after scene 1
		{"Shepherd 2", "", "Joseph", "Joseph", "Joseph", "Joseph"},               // Lav 3
		{"", "", "", "Innkeeper", "Shepherd 1", "Shepherd 1"},                    // Lav 4
		{"Gabriel", "Gabriel", "", "", "Gabriel", "Gabriel"},                     // Headset 1
		{"", "", "", "", "", "Soloist"},                                          // Handheld A
	}
	cells := map[store.Cell]int64{}
	for mi, row := range plan {
		for si, character := range row {
			if character != "" {
				cells[store.Cell{MicID: mics[mi].ID, SceneID: scenes[si].ID}] = who[character]
			}
		}
	}
	if err := st.SaveAssignments(id, cells); err != nil {
		return err
	}
	if _, err := st.FinalizeChart(id, "First run-through", "Demo"); err != nil {
		return err
	}
	// A change after the final version, to show what changed since.
	delete(cells, store.Cell{MicID: mics[3].ID, SceneID: scenes[5].ID})
	if err := st.SaveAssignments(id, cells); err != nil {
		return err
	}

	for _, it := range []store.PrepItem{
		{Department: "props", Name: "Shepherd staffs (2)", Status: "ready", Owner: "Sam"},
		{Department: "props", Name: "Manger and baby doll", Status: "in_progress", Owner: "Sam", DueOn: day(10)},
		{Department: "costumes", Name: "Angel robes and halos", Status: "in_progress", Owner: "Dana", DueOn: day(12)},
		{Department: "costumes", Name: "Shepherd costumes", Status: "todo", Owner: "Dana", DueOn: day(12)},
		{Department: "music", Name: "Backing tracks loaded and checked", Status: "ready", Owner: "Chris"},
		{Department: "sound", Name: "Donkey and night-sounds effects", Status: "blocked", Owner: "Chris", Notes: "Need a better donkey clip."},
		{Department: "lighting", Name: "Star effect for the finale", Status: "todo", Owner: "Riley", DueOn: day(-1)},
		{Department: "video", Name: "Lyric slides", Status: "in_progress", Owner: "Morgan", DueOn: day(11)},
		{Department: "mics", Name: "Fresh batteries for every mic", Status: "todo", Owner: "Chris", DueOn: day(14)},
	} {
		it.ProductionID, it.UpdatedBy = id, "Demo"
		if _, err := st.CreatePrep(&it); err != nil {
			return err
		}
	}

	for _, n := range []store.Need{
		{Name: "Folding chairs for the choir", Quantity: 24, Department: "set"},
		{Name: "AA batteries", Quantity: 24, Department: "mics"},
		{Name: "Gaffer tape", Quantity: 2, Department: "set", Status: "gathered"},
		{Name: "Hay bales", Quantity: 4, Department: "set", Notes: "Ask the Millers; they lent them last year."},
	} {
		n.ProductionID, n.UpdatedBy = id, "Demo"
		if _, err := st.CreateNeed(&n); err != nil {
			return err
		}
	}
	return nil
}
