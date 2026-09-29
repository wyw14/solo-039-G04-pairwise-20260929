package service

import (
	"context"
	"github.com/wyw14/solo-039/internal/energy"
	"path/filepath"
	"testing"
	"time"
)

// Two consecutive production runs, separated by a shutdown, must be accounted
// as separate batches even though the campaign identifier is reused. The
// time-weighted allocation and idle isolation themselves are covered
// deterministically in the energy package tests; here we verify lifecycle
// wiring.
func TestPlantEnergyBatchesAcrossRuns(t *testing.T) {
	plant := NewPlant(filepath.Join(t.TempDir(), "data"))
	ctx := context.Background()

	run := func(grade string) {
		if _, err := plant.Start(ctx, grade, "primary"); err != nil {
			t.Fatalf("start: %v", err)
		}
		time.Sleep(1100 * time.Millisecond)
		if err := plant.RecordMeterReading(serviceReading("S", energy.Steam, 0, "t", time.Now().Unix())); err != nil {
			t.Fatalf("baseline: %v", err)
		}
		time.Sleep(1100 * time.Millisecond)
		if err := plant.RecordMeterReading(serviceReading("S", energy.Steam, 1.0, "t", time.Now().Unix())); err != nil {
			t.Fatalf("usage: %v", err)
		}
		time.Sleep(1100 * time.Millisecond)
		if _, err := plant.Shutdown(ctx, false); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	}

	run("baseline")
	run("premium")

	batches := plant.EnergyBatches()
	if len(batches) != 2 {
		t.Fatalf("want 2 batch accounts across two runs, got %d", len(batches))
	}
	for i, report := range batches {
		if !report.Batch.Closed {
			t.Fatalf("batch %d not closed", i)
		}
		if report.Batch.ID == report.Batch.CampaignID {
			t.Fatal("batch id must be distinct from the reused campaign id")
		}
		if report.Batch.Steam.Total <= 0 {
			t.Fatalf("batch %d carries no steam charge", i)
		}
		if len(report.Segments) == 0 {
			t.Fatalf("batch %d has no meter segments", i)
		}
	}
	if batches[0].Batch.ID == batches[1].Batch.ID {
		t.Fatal("the two runs share one batch id")
	}
	if batches[0].Batch.Grade == batches[1].Batch.Grade {
		t.Fatal("grades should differ between runs")
	}
}

func serviceReading(meter string, u energy.Utility, v float64, unit string, at int64) MeterReadingInput {
	return MeterReadingInput{Meter: meter, Utility: string(u), Value: v, Unit: unit, At: at}
}
