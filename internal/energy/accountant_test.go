package energy

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"math"
	"testing"
)

func campaign(idSeed byte) model.Campaign {
	return model.Campaign{ID: testID(idSeed), Grade: "g", Revision: testID(idSeed + 100), Phase: model.Preparing, Epoch: 1, Fencing: 1}
}

func testID(b byte) uuid.UUID {
	var u uuid.UUID
	u[15] = b
	return u
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func steamT(meter string, value float64, at int64) Reading {
	return Reading{Meter: meter, Utility: Steam, Value: value, Unit: "t", At: at}
}

// Stopped-period consumption must never be charged to the following batch.
func TestIdleUsageNotPushedToNextBatch(t *testing.T) {
	a := NewAccountant()
	c1 := campaign(1)

	if _, err := a.BeginBatch(c1, 100); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 0, 100)); err != nil { // baseline
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 1.0, 200)); err != nil { // 1 t while running
		t.Fatal(err)
	}
	c1.Phase = model.Stopped
	if _, err := a.EndBatch(c1, 200); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 1.5, 300)); err != nil { // 0.5 t while stopped
		t.Fatal(err)
	}

	// Same campaign id restarted after shutdown: a fresh, separate batch.
	c2 := campaign(1)
	c2.Revision = testID(200)
	if _, err := a.BeginBatch(c2, 300); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 2.0, 400)); err != nil { // 0.5 t new batch
		t.Fatal(err)
	}

	reports := a.Reports()
	if len(reports) != 2 {
		t.Fatalf("want 2 batches, got %d", len(reports))
	}
	b1 := reports[0].Intensities[0].Amount
	b2 := reports[1].Intensities[0].Amount
	if !approx(b1, 1.0) {
		t.Fatalf("batch1 steam = %v, want 1.0 t", b1)
	}
	if !approx(b2, 0.5) {
		t.Fatalf("batch2 steam = %v, want 0.5 t", b2)
	}
	idle := a.Idle()["steam_t"]
	if !approx(idle, 0.5) {
		t.Fatalf("idle steam = %v, want 0.5 t", idle)
	}
}

// An interval ending exactly on the shutdown boundary belongs to the batch.
func TestIntervalEndingOnBoundaryBelongsToBatch(t *testing.T) {
	a := NewAccountant()
	c := campaign(2)
	if _, err := a.BeginBatch(c, 100); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 0, 100)); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 0.4, 200)); err != nil {
		t.Fatal(err)
	}
	c.Phase = model.Stopped
	if _, err := a.EndBatch(c, 200); err != nil {
		t.Fatal(err)
	}
	if got := a.Reports()[0].Intensities[0].Amount; !approx(got, 0.4) {
		t.Fatalf("batch steam = %v, want 0.4 t", got)
	}
	if got := a.Idle()["steam_t"]; !approx(got, 0) {
		t.Fatalf("idle steam = %v, want 0", got)
	}
}

// Consumption is split by phase in proportion to elapsed time.
func TestConsumptionAllocatedByPhase(t *testing.T) {
	a := NewAccountant()
	c := campaign(3)
	if _, err := a.BeginBatch(c, 100); err != nil {
		t.Fatal(err)
	}
	c.Phase = model.Warming
	if err := a.MarkPhase(c, 200); err != nil {
		t.Fatal(err)
	}
	c.Phase = model.Stabilizing
	if err := a.MarkPhase(c, 300); err != nil {
		t.Fatal(err)
	}

	if err := a.Ingest(steamT("S", 0, 100)); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 1.0, 200)); err != nil { // preparing
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 3.0, 400)); err != nil { // warming 1 + stabilizing 1
		t.Fatal(err)
	}

	batch := a.Reports()[0].Batch
	if !approx(batch.Steam.ByPhase[model.Preparing], 1000) {
		t.Fatalf("preparing = %v", batch.Steam.ByPhase[model.Preparing])
	}
	if !approx(batch.Steam.ByPhase[model.Warming], 1000) {
		t.Fatalf("warming = %v", batch.Steam.ByPhase[model.Warming])
	}
	if !approx(batch.Steam.ByPhase[model.Stabilizing], 1000) {
		t.Fatalf("stabilizing = %v", batch.Steam.ByPhase[model.Stabilizing])
	}
	if !approx(batch.Steam.Total, 3000) {
		t.Fatalf("total = %v", batch.Steam.Total)
	}
}

// A counter wrap with a declared full-scale is one continued measurement.
func TestRolloverOpensSegmentButKeepsUsage(t *testing.T) {
	a := NewAccountant()
	c := campaign(4)
	if _, err := a.BeginBatch(c, 100); err != nil {
		t.Fatal(err)
	}
	r1 := Reading{Meter: "S", Utility: Steam, Value: 990, Unit: "t", At: 100, RolloverAt: 1000}
	r2 := Reading{Meter: "S", Utility: Steam, Value: 5, Unit: "t", At: 200, RolloverAt: 1000}
	if err := a.Ingest(r1); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(r2); err != nil {
		t.Fatal(err)
	}
	if got := a.Reports()[0].Intensities[0].Amount; !approx(got, 15) {
		t.Fatalf("rollover usage = %v, want 15 t", got)
	}
	segs := a.Segments()
	if len(segs) != 2 || segs[1].Reason != SegmentRollover {
		t.Fatalf("segments = %+v", segs)
	}
	if len(a.Gaps()) != 0 {
		t.Fatalf("rollover must not create an unmeasured gap: %+v", a.Gaps())
	}
}

// A meter swap without full-scale info opens a replacement segment and parks
// the unmeasured stretch as a gap instead of guessing usage.
func TestReplacementOpensSegmentAndGap(t *testing.T) {
	a := NewAccountant()
	c := campaign(5)
	if _, err := a.BeginBatch(c, 100); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 100, 100)); err != nil {
		t.Fatal(err)
	}
	// New meter starts at zero; the [100,200] stretch is unmeasured.
	if err := a.Ingest(steamT("S", 0, 200)); err != nil {
		t.Fatal(err)
	}
	gaps := a.Gaps()
	if len(gaps) != 1 || gaps[0].FromAt != 100 || gaps[0].ToAt != 200 {
		t.Fatalf("gaps = %+v", gaps)
	}
	segs := a.Segments()
	if len(segs) != 2 || segs[1].Reason != SegmentReplacement {
		t.Fatalf("segments = %+v", segs)
	}
	if err := a.Ingest(steamT("S", 10, 300)); err != nil {
		t.Fatal(err)
	}
	if got := a.Reports()[0].Intensities[0].Amount; !approx(got, 10) {
		t.Fatalf("post-swap usage = %v, want 10 t (unmeasured gap excluded)", got)
	}
}

func TestUnitValidation(t *testing.T) {
	a := NewAccountant()
	c := campaign(6)
	if _, err := a.BeginBatch(c, 100); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(steamT("S", 0, 100)); err != nil {
		t.Fatal(err)
	}
	// Same meter switching to a volume unit is a dimension mismatch.
	if err := a.Ingest(Reading{Meter: "S", Utility: Steam, Value: 1, Unit: "m3", At: 200}); err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	// Unknown unit.
	if err := a.Ingest(Reading{Meter: "S", Utility: Steam, Value: 1, Unit: "frobnicates", At: 200}); err == nil {
		t.Fatal("expected unknown unit error")
	}
	// Negative totalizer.
	if err := a.Ingest(Reading{Meter: "S", Utility: Steam, Value: -1, Unit: "t", At: 200}); err == nil {
		t.Fatal("expected negative reading error")
	}
	// Out-of-order timestamp.
	if err := a.Ingest(steamT("S", 1, 90)); err == nil {
		t.Fatal("expected chronological order error")
	}
}

// Cooling water metered in m3 converts to base litres and back.
func TestCoolingWaterUnitsAndIntensity(t *testing.T) {
	a := NewAccountant()
	c := campaign(7)
	if _, err := a.BeginBatch(c, 100); err != nil {
		t.Fatal(err)
	}
	water := func(v float64, at int64) Reading {
		return Reading{Meter: "CW", Utility: CoolingWater, Value: v, Unit: "m3", At: at}
	}
	if err := a.Ingest(water(0, 100)); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(water(40, 200)); err != nil {
		t.Fatal(err)
	}
	if err := a.RecordProduct(uuid.Nil, 8, "t"); err != nil {
		t.Fatal(err)
	}
	report := a.Reports()[0]
	cw := report.Intensities[1]
	if cw.Utility != CoolingWater || !approx(cw.Amount, 40) {
		t.Fatalf("cooling water amount = %+v", cw)
	}
	if !approx(cw.PerProduct, 5) {
		t.Fatalf("cooling intensity = %v, want 5 m3/t product", cw.PerProduct)
	}
	steam := report.Intensities[0]
	if steam.PerProduct != 0 {
		t.Fatalf("unused steam intensity should be 0, got %v", steam.PerProduct)
	}
}
