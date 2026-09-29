package energy

import (
	"errors"
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"sort"
	"sync"
)

// BatchAccumulation holds allocated consumption (base units) for one utility,
// split across the campaign phases it was used in.
type BatchAccumulation struct {
	ByPhase map[model.Phase]float64 `json:"by_phase"`
	Total   float64                 `json:"total"`
}

// Batch is one production run's energy account. Every plant Start opens a new
// batch even though the campaign identifier can be reused across runs, so the
// batch carries its own ID and references the campaign it belonged to.
type Batch struct {
	ID          uuid.UUID            `json:"id"`
	CampaignID  uuid.UUID            `json:"campaign_id"`
	Grade       string               `json:"grade"`
	Revision    uuid.UUID            `json:"revision"`
	StartAt     int64                `json:"start_at"`
	EndAt       int64                `json:"end_at"`
	Closed      bool                 `json:"closed"`
	Product     float64              `json:"product"`
	ProductUnit string               `json:"product_unit"`
	Steam       BatchAccumulation    `json:"steam"`
	Cooling     BatchAccumulation    `json:"cooling_water"`
	Segments    map[string][]Segment `json:"-"`
}

// boundary marks when the active batch/phase context changed. A nil batch
// means the plant was stopped; consumption during that context is idle and is
// never attached to a campaign.
type boundary struct {
	at      int64
	batchID uuid.UUID
	phase   model.Phase
}

type Accountant struct {
	mu       sync.Mutex
	meters   map[string]*meterState
	batches  []*Batch
	byID     map[uuid.UUID]*Batch
	current  *Batch
	timeline []boundary
	idle     map[Utility]float64
	segments []Segment
	gaps     []Gap
}

func NewAccountant() *Accountant {
	return &Accountant{
		meters: make(map[string]*meterState),
		byID:   make(map[uuid.UUID]*Batch),
		idle:   map[Utility]float64{Steam: 0, CoolingWater: 0},
	}
}

// BeginBatch opens the energy account for a campaign run. Each call mints a
// fresh batch id, so a campaign restarted after a shutdown gets a separate
// account and stopped-period usage can never be folded into it.
func (a *Accountant) BeginBatch(c model.Campaign, at int64) (*Batch, error) {
	return a.BeginBatchID(c, at, uuid.New())
}

// BeginBatchID opens a batch with a caller-supplied id. It is used when
// replaying the durable journal so a batch keeps the same identity across a
// process restart.
func (a *Accountant) BeginBatchID(c model.Campaign, at int64, id uuid.UUID) (*Batch, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if at <= 0 {
		return nil, errors.New("batch start requires a time")
	}
	if id == uuid.Nil {
		return nil, errors.New("batch id is required")
	}
	if a.current != nil {
		return nil, errors.New("a batch is already open")
	}
	if _, exists := a.byID[id]; exists {
		return nil, errors.New("batch id already used")
	}
	batch := &Batch{
		ID:         id,
		CampaignID: c.ID,
		Grade:      c.Grade,
		Revision:   c.Revision,
		StartAt:    at,
		Steam:      BatchAccumulation{ByPhase: map[model.Phase]float64{}},
		Cooling:    BatchAccumulation{ByPhase: map[model.Phase]float64{}},
		Segments:   map[string][]Segment{},
	}
	a.batches = append(a.batches, batch)
	a.byID[batch.ID] = batch
	a.current = batch
	a.timeline = append(a.timeline, boundary{at: at, batchID: batch.ID, phase: c.Phase})
	return batch, nil
}

// MarkPhase records that the running batch entered a new phase. Readings are
// allocated to the phase active at their time, so the boundary time matters.
func (a *Accountant) MarkPhase(c model.Campaign, at int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil || a.current.CampaignID != c.ID {
		return errors.New("no open batch for phase marker")
	}
	last := a.timeline[len(a.timeline)-1]
	if at < last.at {
		return errors.New("phase marker is earlier than the current batch timeline")
	}
	if at == last.at && last.phase == c.Phase {
		return nil
	}
	a.current.Revision = c.Revision
	a.timeline = append(a.timeline, boundary{at: at, batchID: a.current.ID, phase: c.Phase})
	return nil
}

// EndBatch closes the running batch for the campaign. Consumption after this
// time is idle.
func (a *Accountant) EndBatch(c model.Campaign, at int64) (*Batch, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil || a.current.CampaignID != c.ID {
		return nil, errors.New("campaign has no open batch")
	}
	if at < a.current.StartAt {
		return nil, errors.New("batch end is earlier than its start")
	}
	batch := a.current
	batch.EndAt = at
	batch.Closed = true
	batch.Revision = c.Revision
	a.current = nil
	a.timeline = append(a.timeline, boundary{at: at, batchID: uuid.Nil, phase: model.Stopped})
	return batch, nil
}

// RecordProduct records the qualified product amount for a batch, used as the
// denominator for unit-product energy intensity. With an empty batch id it
// applies to the currently open batch (or the one most recently closed).
func (a *Accountant) RecordProduct(batchID uuid.UUID, amount float64, unit string) error {
	if amount < 0 {
		return errors.New("product amount cannot be negative")
	}
	if unit == "" {
		return errors.New("product unit is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	batch, err := a.resolveBatchLocked(batchID)
	if err != nil {
		return err
	}
	batch.Product = amount
	batch.ProductUnit = unit
	return nil
}

func (a *Accountant) resolveBatchLocked(id uuid.UUID) (*Batch, error) {
	if id != uuid.Nil {
		if batch, ok := a.byID[id]; ok {
			return batch, nil
		}
		return nil, errors.New("no batch with that id")
	}
	if a.current != nil {
		return a.current, nil
	}
	if len(a.batches) > 0 {
		return a.batches[len(a.batches)-1], nil
	}
	return nil, errors.New("no batch has been opened")
}

// Ingest accepts one cumulative meter reading, opens a new segment on
// replacement/rollover, and allocates the measured interval.
func (a *Accountant) Ingest(r Reading) error {
	if err := r.Validate(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m, ok := a.meters[r.Meter]
	if !ok {
		m = &meterState{utility: "", dim: ""}
		a.meters[r.Meter] = m
	}
	iv, seg, err := m.register(r)
	if err != nil {
		return err
	}
	if seg != nil {
		a.segments = append(a.segments, *seg)
		if batch := a.batchAt(seg.StartAt); batch != nil {
			batch.Segments[r.Meter] = append(batch.Segments[r.Meter], *seg)
		}
	}
	if len(m.gaps) > 0 {
		a.gaps = append(a.gaps, m.gaps...)
		m.gaps = m.gaps[:0]
	}
	if iv.amount <= 0 || iv.toAt == iv.fromAt {
		return nil
	}
	a.allocate(iv)
	return nil
}

// contextAt returns the batch and phase active at time t. A time before the
// first boundary or during a stopped stretch resolves to the zero batch, i.e.
// idle consumption that belongs to no campaign.
func (a *Accountant) contextAt(t int64) (uuid.UUID, model.Phase) {
	active := boundary{batchID: uuid.Nil, phase: model.Stopped}
	found := false
	for _, b := range a.timeline {
		if b.at <= t {
			active = b
			found = true
		} else {
			break
		}
	}
	if !found {
		return uuid.Nil, model.Stopped
	}
	return active.batchID, active.phase
}

// allocate splits one measured meter interval across the batch/phase contexts
// active during it, in proportion to elapsed time. Boundary times themselves
// belong to the context that starts there, so an interval ending exactly on a
// shutdown boundary is still charged to the running batch.
func (a *Accountant) allocate(iv interval) {
	totalSeconds := float64(iv.toAt - iv.fromAt)
	if totalSeconds <= 0 {
		return
	}

	// Boundaries strictly inside the interval are the only cut points.
	cuts := make([]int64, 0)
	for _, b := range a.timeline {
		if b.at > iv.fromAt && b.at < iv.toAt {
			cuts = append(cuts, b.at)
		}
	}

	charge := func(start, end int64) {
		if end <= start {
			return
		}
		share := iv.amount * float64(end-start) / totalSeconds
		batchID, phase := a.contextAt(start)
		if batchID == uuid.Nil {
			a.idle[iv.utility] += share
			return
		}
		batch := a.byID[batchID]
		if batch == nil {
			a.idle[iv.utility] += share
			return
		}
		acc := batch.accum(iv.utility)
		acc.ByPhase[phase] += share
		acc.Total += share
		batch.setAccum(iv.utility, acc)
	}

	start := iv.fromAt
	for _, at := range cuts {
		charge(start, at)
		start = at
	}
	charge(start, iv.toAt)
}

func (a *Accountant) batchAt(at int64) *Batch {
	id, _ := a.contextAt(at)
	if id == uuid.Nil {
		return nil
	}
	return a.byID[id]
}

func (b *Batch) accum(u Utility) BatchAccumulation {
	if u == Steam {
		return b.Steam
	}
	return b.Cooling
}

func (b *Batch) setAccum(u Utility, acc BatchAccumulation) {
	if u == Steam {
		b.Steam = acc
	} else {
		b.Cooling = acc
	}
}

// Intensity is energy used per unit of product for one utility.
type Intensity struct {
	Utility     Utility `json:"utility"`
	Unit        string  `json:"unit"`
	Amount      float64 `json:"amount"`
	PerProduct  float64 `json:"per_product"`
	Product     float64 `json:"product"`
	ProductUnit string  `json:"product_unit"`
}

func (b *Batch) intensities() []Intensity {
	out := make([]Intensity, 0, 2)
	build := func(u Utility, unit string, acc BatchAccumulation) {
		amount, _ := fromBase(acc.Total, unit)
		in := Intensity{Utility: u, Unit: unit, Amount: amount, Product: b.Product, ProductUnit: b.ProductUnit}
		if b.Product > 0 {
			in.PerProduct = amount / b.Product
		}
		out = append(out, in)
	}
	build(Steam, "t", b.Steam)
	build(CoolingWater, "m3", b.Cooling)
	return out
}

// BatchReport is the externally visible account for one batch.
type BatchReport struct {
	Batch       *Batch      `json:"batch"`
	Intensities []Intensity `json:"intensities"`
	Segments    []Segment   `json:"segments"`
}

func (a *Accountant) report(batch *Batch) BatchReport {
	segs := make([]Segment, 0)
	for _, list := range batch.Segments {
		segs = append(segs, list...)
	}
	sort.SliceStable(segs, func(i, j int) bool {
		if segs[i].Meter == segs[j].Meter {
			return segs[i].Seq < segs[j].Seq
		}
		return segs[i].Meter < segs[j].Meter
	})
	return BatchReport{Batch: batch, Intensities: batch.intensities(), Segments: segs}
}

func (a *Accountant) ReportCurrent() (BatchReport, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil {
		return BatchReport{}, false
	}
	return a.report(a.current), true
}

// CurrentBatchID reports the open batch id and whether one is active.
func (a *Accountant) CurrentBatchID() (uuid.UUID, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil {
		return uuid.Nil, false
	}
	return a.current.ID, true
}

func (a *Accountant) ReportBatch(id uuid.UUID) (BatchReport, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	batch, ok := a.byID[id]
	if !ok {
		return BatchReport{}, false
	}
	return a.report(batch), true
}

func (a *Accountant) Reports() []BatchReport {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]BatchReport, 0, len(a.batches))
	for _, batch := range a.batches {
		out = append(out, a.report(batch))
	}
	return out
}

// SegmentList returns every meter segment recorded, including replacements.
func (a *Accountant) Segments() []Segment {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Segment, len(a.segments))
	copy(out, a.segments)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Meter == out[j].Meter {
			return out[i].Seq < out[j].Seq
		}
		return out[i].Meter < out[j].Meter
	})
	return out
}

// Gaps returns unmeasured stretches caused by meter replacements.
func (a *Accountant) Gaps() []Gap {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Gap, len(a.gaps))
	copy(out, a.gaps)
	return out
}

// Idle returns consumption (in display units) that fell outside any batch.
func (a *Accountant) Idle() map[string]float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	steam, _ := fromBase(a.idle[Steam], "t")
	water, _ := fromBase(a.idle[CoolingWater], "m3")
	return map[string]float64{"steam_t": steam, "cooling_water_m3": water}
}
