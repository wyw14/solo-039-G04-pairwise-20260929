package energy

import (
	"github.com/wyw14/solo-039/internal/model"
	"path/filepath"
	"testing"
)

// The accountant rebuilt from the journal must reproduce batch allocations,
// segments, gaps and idle usage, with stable batch ids.
func TestRecorderReplayRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "energy.jsonl")
	r1 := NewRecorder(path)
	c := campaign(9)

	batch, err := r1.Begin(c, 100)
	if err != nil {
		t.Fatal(err)
	}
	c.Phase = model.Warming
	_ = r1.Phase(c, 200)
	if err := r1.Reading(steamT("S", 0, 100)); err != nil {
		t.Fatal(err)
	}
	if err := r1.Reading(steamT("S", 2, 300)); err != nil {
		t.Fatal(err)
	}
	if err := r1.Product(batch.ID, 4, "t"); err != nil {
		t.Fatal(err)
	}
	c.Phase = model.Stopped
	if _, err := r1.End(c, 300); err != nil {
		t.Fatal(err)
	}
	if err := r1.Reading(steamT("S", 3, 400)); err != nil { // idle
		t.Fatal(err)
	}

	r2 := NewRecorder(path)
	if err := r2.Load(); err != nil {
		t.Fatal(err)
	}
	reports := r2.Accountant.Reports()
	if len(reports) != 1 {
		t.Fatalf("batches after replay = %d", len(reports))
	}
	got := reports[0]
	if got.Batch.ID != batch.ID {
		t.Fatal("batch id changed across replay")
	}
	if !approx(got.Intensities[0].Amount, 2) {
		t.Fatalf("steam after replay = %v, want 2 t", got.Intensities[0].Amount)
	}
	if !approx(got.Intensities[0].PerProduct, 0.5) {
		t.Fatalf("intensity after replay = %v, want 0.5 t/t", got.Intensities[0].PerProduct)
	}
	if !approx(r2.Accountant.Idle()["steam_t"], 1) {
		t.Fatalf("idle after replay = %v, want 1 t", r2.Accountant.Idle()["steam_t"])
	}
	if len(r2.Accountant.Segments()) != 1 {
		t.Fatal("initial segment should survive replay")
	}
}
