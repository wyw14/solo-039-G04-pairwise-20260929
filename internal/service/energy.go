package service

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/energy"
)

// LoadEnergy rebuilds the energy accountant from its durable journal. It is
// safe to call even when no snapshot exists yet.
func (p *Plant) LoadEnergy() error { return p.Energy.Load() }

// MeterReadingInput is one cumulative totalizer sample supplied by a shift.
type MeterReadingInput struct {
	Meter      string  `json:"meter"`
	Utility    string  `json:"utility"`
	Value      float64 `json:"value"`
	Unit       string  `json:"unit"`
	At         int64   `json:"at"`
	RolloverAt float64 `json:"rollover_at,omitempty"`
}

// RecordMeterReading accepts a utility meter reading with its unit and time,
// allocating consumption across batch phases. Meter replacement or rollover
// opens a separate segment so stopped usage is never pushed onto a batch.
func (p *Plant) RecordMeterReading(input MeterReadingInput) error {
	reading := energy.Reading{
		Meter:      input.Meter,
		Utility:    energy.Utility(input.Utility),
		Value:      input.Value,
		Unit:       input.Unit,
		At:         input.At,
		RolloverAt: input.RolloverAt,
	}
	if err := p.Energy.Reading(reading); err != nil {
		return err
	}
	return nil
}

// ProductOutput records the qualified product amount for a batch, enabling
// per-unit-product energy intensity. An empty batchID targets the open (or
// most recently closed) batch.
func (p *Plant) ProductOutput(batchID uuid.UUID, amount float64, unit string) error {
	return p.Energy.Product(batchID, amount, unit)
}

// EnergyBatches returns every batch energy report.
func (p *Plant) EnergyBatches() []energy.BatchReport { return p.Energy.Accountant.Reports() }

// EnergyCurrent returns the open batch report, if any.
func (p *Plant) EnergyCurrent() (energy.BatchReport, bool) {
	return p.Energy.Accountant.ReportCurrent()
}

// EnergyBatch returns one batch report by id.
func (p *Plant) EnergyBatch(id uuid.UUID) (energy.BatchReport, bool) {
	return p.Energy.Accountant.ReportBatch(id)
}

// EnergySummary surfaces idle usage, meter segments and unmeasured gaps.
func (p *Plant) EnergySummary() map[string]any {
	return map[string]any{
		"idle":     p.Energy.Accountant.Idle(),
		"segments": p.Energy.Accountant.Segments(),
		"gaps":     p.Energy.Accountant.Gaps(),
	}
}
