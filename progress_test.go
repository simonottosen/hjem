package hjem

import "testing"

// StageBoligaList is re-set on every server-side street fetch, so it covers
// most of a long lookup. Gating the plan on the stage alone would re-send tens
// of kilobytes of identical coordinates to a client that kept the first copy.
func TestMapPlanStopsBeingSentAfterTheFirstFewPolls(t *testing.T) {
	p := NewProgress()
	p.SetMapPlan(&MapPlan{Lat: 55.67, Lon: 12.55, RadiusM: 250})
	p.Update(StageBoligaList, "Henter salgslister fra Boliga...", 0, 0)

	sent := 0
	for range mapPlanSends + 20 {
		if p.Snapshot().Map != nil {
			sent++
		}
	}

	if sent != mapPlanSends {
		t.Errorf("plan sent on %d snapshots, want %d", sent, mapPlanSends)
	}
}

// The spares exist for a dropped poll, so they have to survive the per-street
// progress updates that land between polls rather than being spent by them.
func TestMapPlanSurvivesStreetProgressUpdates(t *testing.T) {
	p := NewProgress()
	p.SetMapPlan(&MapPlan{Lat: 55.67, Lon: 12.55, RadiusM: 250})

	for i := range mapPlanSends {
		p.Update(StageBoligaList, "Henter salgsliste...", i, 10)
		if p.Snapshot().Map == nil {
			t.Fatalf("plan missing on snapshot %d of %d", i+1, mapPlanSends)
		}
	}
}

// Nothing outside the Boliga stages has a map to draw, and a snapshot that
// cannot use the plan must not burn one of the few sends that can.
func TestMapPlanIsNotSpentOnOtherStages(t *testing.T) {
	p := NewProgress()
	p.SetMapPlan(&MapPlan{Lat: 55.67, Lon: 12.55, RadiusM: 250})

	p.Update(StageDawa, "Søger adresse...", 0, 0)
	for range 10 {
		if snap := p.Snapshot(); snap.Map != nil {
			t.Fatalf("stage %q carries a map plan", snap.Stage)
		}
	}

	p.Update(StageBoligaList, "Henter salgslister fra Boliga...", 0, 0)
	if p.Snapshot().Map == nil {
		t.Error("plan was spent before any Boliga stage could use it")
	}
}
