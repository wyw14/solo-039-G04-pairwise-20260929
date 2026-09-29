package energy

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/wyw14/solo-039/internal/model"
)

// Interval 是同一表段上相邻两次读数之间的用量区间。
// 换表不产生区间（新表首读只是锚点）；回绕保留区间并按量程补算。
type Interval struct {
	MeterID  string
	Kind     string
	Unit     string
	FromAt   int64
	ToAt     int64
	FromVal  float64
	ToVal    float64
	Rollover bool
	Capacity float64
}

func (i Interval) Usage() float64 {
	if i.Rollover {
		return i.Capacity - i.FromVal + i.ToVal
	}
	return i.ToVal - i.FromVal
}

// Ledger 是能源核算账本：表计、读数段、批次时间轴和分摊结果。
type Ledger struct {
	mu         sync.RWMutex
	meters     map[string]*Meter
	meterOrder []string
	batches    map[string]*Batch
	batchOrder []string
	openBatch  string
	sink       EventSink
}

// EventSink 接收已接受的核算事件，用于持久化（JSONL 账目）。
type EventSink interface {
	Append(event Event) error
}

func NewLedger() *Ledger {
	return &Ledger{meters: make(map[string]*Meter), batches: make(map[string]*Batch)}
}

// WithSink 挂载持久化事件出口。
func (l *Ledger) WithSink(sink EventSink) *Ledger {
	l.mu.Lock()
	l.sink = sink
	l.mu.Unlock()
	return l
}

// MeterRegistration 登记表计：现场编号、种类、单位、出厂序列号、起始读数和回绕量程。
type MeterRegistration struct {
	ID           string
	Kind         string
	Unit         string
	Serial       string
	InitialValue float64
	Capacity     float64
	At           int64
}

func (r MeterRegistration) validate() (string, string, error) {
	id := strings.ToLower(strings.TrimSpace(r.ID))
	if id == "" {
		return "", "", errors.New("meter id is required")
	}
	kind, ok := normalizeKind(r.Kind)
	if !ok {
		return "", "", fmt.Errorf("unknown utility kind %q", r.Kind)
	}
	unit := strings.TrimSpace(r.Unit)
	if unit == "" {
		return "", "", errors.New("meter unit is required")
	}
	if r.InitialValue < 0 {
		return "", "", errors.New("meter initial value cannot be negative")
	}
	if r.Capacity < 0 {
		return "", "", errors.New("meter capacity cannot be negative")
	}
	if r.Capacity > 0 && r.InitialValue >= r.Capacity {
		return "", "", errors.New("meter initial value must be below rollover capacity")
	}
	if r.At <= 0 {
		return "", "", errors.New("meter registration time is required")
	}
	return id, kind, nil
}

// RegisterMeter 登记一只新表。序列号必填，作为后续换表判定基准。
func (l *Ledger) RegisterMeter(reg MeterRegistration) (*Meter, error) {
	id, kind, err := reg.validate()
	if err != nil {
		return nil, err
	}
	serial := strings.TrimSpace(reg.Serial)
	if serial == "" {
		return nil, errors.New("meter serial is required")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.meters[id]; exists {
		return nil, fmt.Errorf("meter %q already registered", id)
	}
	meter := &Meter{
		ID:        id,
		Kind:      kind,
		Unit:      strings.TrimSpace(reg.Unit),
		CurrentSN: serial,
		Capacity:  reg.Capacity,
		Registration: Registration{
			Serial:       serial,
			InitialValue: reg.InitialValue,
			InitialAt:    reg.At,
			RegisteredAt: reg.At,
		},
		Segments: []*Segment{{
			Kind:       kind,
			Serial:     serial,
			StartAt:    reg.At,
			StartValue: reg.InitialValue,
			EndAt:      reg.At,
			EndValue:   reg.InitialValue,
			Reason:     SegmentInstall,
			readings:   []Reading{{Value: reg.InitialValue, At: reg.At, Unit: strings.TrimSpace(reg.Unit), Serial: serial}},
		}},
	}
	l.meters[id] = meter
	l.meterOrder = append(l.meterOrder, id)
	event := Event{
		Type: EventMeterRegistered, At: reg.At, MeterID: id,
		Kind: kind, Unit: meter.Unit, Serial: serial,
		InitialValue: reg.InitialValue, Capacity: reg.Capacity,
	}
	if err := l.emit(event); err != nil {
		delete(l.meters, id)
		l.meterOrder = l.meterOrder[:len(l.meterOrder)-1]
		return nil, err
	}
	return meter, nil
}

// MeterReading 是一次上报的表计读数；Serial 为读数时表盘上的序列号，用于换表识别。
type MeterReading struct {
	MeterID string
	Serial  string
	Value   float64
	At      int64
	Note    string
}

// RecordReading 接收一次累计表读数，按需要切段（换表/回绕）并生成用量区间。
func (l *Ledger) RecordReading(input MeterReading) (*Segment, error) {
	id := strings.ToLower(strings.TrimSpace(input.MeterID))
	if input.At <= 0 {
		return nil, errors.New("reading time is required")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	meter, ok := l.meters[id]
	if !ok {
		return nil, fmt.Errorf("meter %q is not registered", id)
	}
	segment := meter.currentSegment()
	last := segment.readings[len(segment.readings)-1]
	if input.At < last.At {
		return nil, fmt.Errorf("reading time %d precedes prior reading %d", input.At, last.At)
	}
	if input.Value < 0 {
		return nil, errors.New("meter reading cannot be negative")
	}
	serial := strings.TrimSpace(input.Serial)
	if serial == "" {
		serial = meter.CurrentSN // 未抄序列号时沿用当前表
	}

	// 换表：序列号变化。旧段在末次读数处封段，新表首读仅作锚点，
	// 不跨表相减——换表期间的用量不能凭空算到任何批次上。
	if serial != meter.CurrentSN {
		closed := meter.currentSegment()
		closed.EndAt = last.At
		closed.EndValue = last.Value
		segment = &Segment{
			Kind:       meter.Kind,
			Serial:     serial,
			StartAt:    input.At,
			StartValue: input.Value,
			EndAt:      input.At,
			EndValue:   input.Value,
			Reason:     SegmentReplaced,
			readings:   []Reading{{Value: input.Value, At: input.At, Unit: meter.Unit, Serial: serial, Note: input.Note}},
		}
		meter.Segments = append(meter.Segments, segment)
		meter.CurrentSN = serial
		event := Event{Type: EventMeterReplaced, At: input.At, MeterID: id, Value: input.Value, Note: input.Note, Serial: serial}
		if err := l.emit(event); err != nil {
			meter.Segments = meter.Segments[:len(meter.Segments)-1]
			meter.CurrentSN = closed.Serial
			return nil, err
		}
		return segment, nil
	}

	if input.Value < last.Value {
		// 读数倒退：声明了量程才允许按回绕处理，否则必须走换表登记。
		if meter.Capacity <= 0 {
			return nil, fmt.Errorf("meter %q reading decreased from %.3f to %.3f; declare rollover capacity or register a replacement", id, last.Value, input.Value)
		}
		if input.Value >= meter.Capacity {
			return nil, errors.New("rollover reading must be below meter capacity")
		}
		closed := meter.currentSegment()
		closed.EndAt = last.At
		closed.EndValue = last.Value
		segment = &Segment{
			Kind:       meter.Kind,
			Serial:     serial,
			StartAt:    input.At,
			StartValue: input.Value,
			EndAt:      input.At,
			EndValue:   input.Value,
			MaxReading: meter.Capacity,
			Reason:     SegmentRollover,
			readings:   []Reading{{Value: input.Value, At: input.At, Unit: meter.Unit, Serial: serial, Note: input.Note}},
		}
		meter.Segments = append(meter.Segments, segment)
		event := Event{Type: EventMeterRollover, At: input.At, MeterID: id, Value: input.Value, Note: input.Note}
		if err := l.emit(event); err != nil {
			meter.Segments = meter.Segments[:len(meter.Segments)-1]
			return nil, err
		}
		return segment, nil
	}

	// 同表同段的正常读数：生成用量区间。
	segment.readings = append(segment.readings, Reading{Value: input.Value, At: input.At, Unit: meter.Unit, Serial: serial, Note: input.Note})
	segment.EndAt = input.At
	segment.EndValue = input.Value
	event := Event{Type: EventReading, At: input.At, MeterID: id, Value: input.Value, Note: input.Note}
	if err := l.emit(event); err != nil {
		segment.readings = segment.readings[:len(segment.readings)-1]
		segment.EndAt, segment.EndValue = last.At, last.Value
		return nil, err
	}
	return segment, nil
}

// BeginBatch 开启一个新批次的能源时间轴。id 是能源账唯一运行号，
// ref 关联生产批次（campaign ID），二者都必填。
func (l *Ledger) BeginBatch(id, ref, grade, revision string, at int64) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("batch id is required")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("batch campaign reference is required")
	}
	if at <= 0 {
		return errors.New("batch start time is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.batches[id]; exists {
		return fmt.Errorf("batch %q already exists", id)
	}
	if l.openBatch != "" {
		return fmt.Errorf("batch %q is still open", l.openBatch)
	}
	if len(l.batchOrder) > 0 {
		prev := l.batches[l.batchOrder[len(l.batchOrder)-1]]
		if at < prev.StartAt {
			return errors.New("batch starts before the previous batch")
		}
	}
	batch := &Batch{
		ID:      id,
		Ref:     ref,
		Grade:   grade,
		StartAt: at,
		phases: []BatchPhase{{
			Phase:    model.Preparing,
			Revision: revision,
			Grade:    grade,
			Start:    at,
		}},
	}
	l.batches[id] = batch
	l.batchOrder = append(l.batchOrder, id)
	l.openBatch = id
	return l.emit(Event{Type: EventBatchBegan, At: at, BatchID: id, Ref: ref, Grade: grade, Revision: revision})
}

// AdvancePhase 在当前批次时间轴上追加阶段切片（牌号切换时 revision 变化）。
func (l *Ledger) AdvanceBatch(phase model.Phase, revision, grade string, at int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.openBatch == "" {
		return errors.New("no open batch")
	}
	batch := l.batches[l.openBatch]
	current := batch.phases[len(batch.phases)-1]
	if at < current.Start {
		return errors.New("phase change precedes current phase start")
	}
	if grade == "" {
		grade = current.Grade
	}
	current.End = at
	batch.phases[len(batch.phases)-1] = current
	if phase != current.Phase || revision != current.Revision {
		batch.phases = append(batch.phases, BatchPhase{Phase: phase, Revision: revision, Grade: grade, Start: at})
	}
	batch.Grade = grade
	return l.emit(Event{Type: EventPhaseAdvanced, At: at, BatchID: batch.ID, Phase: string(phase), Revision: revision, Grade: grade})
}

// EndBatch 结束当前批次；此后表计用量计入停机未分摊，不会落到下一批。
func (l *Ledger) EndBatch(at int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.openBatch == "" {
		return errors.New("no open batch")
	}
	batch := l.batches[l.openBatch]
	current := batch.phases[len(batch.phases)-1]
	if at < current.Start {
		return errors.New("batch ends before current phase start")
	}
	current.End = at
	batch.phases[len(batch.phases)-1] = current
	batch.EndAt = at
	id := batch.ID
	l.openBatch = ""
	return l.emit(Event{Type: EventBatchEnded, At: at, BatchID: id})
}

// ProductionInput 登记批次产品产量（增量口径，单位如 kg）。
type ProductionInput struct {
	BatchID  string
	Revision string
	Target   string
	Amount   float64
	Unit     string
	At       int64
}

// RecordProduction 把产品产量登记到批次的牌号段上。
func (l *Ledger) RecordProduction(input ProductionInput) error {
	if input.Amount <= 0 {
		return errors.New("production amount must be positive")
	}
	unit := strings.TrimSpace(input.Unit)
	if unit == "" {
		return errors.New("production unit is required")
	}
	if input.At <= 0 {
		return errors.New("production time is required")
	}
	batchID := strings.TrimSpace(input.BatchID)
	l.mu.Lock()
	defer l.mu.Unlock()
	batch, ok := l.batches[batchID]
	if !ok {
		return fmt.Errorf("batch %q is not registered", batchID)
	}
	if !batch.Open() && (input.At < batch.StartAt || input.At > batch.EndAt) {
		return errors.New("production time is outside the batch window")
	}
	if batch.Open() && input.At < batch.StartAt {
		return errors.New("production time precedes the batch start")
	}
	revision := strings.TrimSpace(input.Revision)
	if revision == "" {
		revision = batch.phases[len(batch.phases)-1].Revision
	}
	if !batch.hasRevision(revision) {
		return fmt.Errorf("revision %q is not part of batch %q", revision, batchID)
	}
	batch.products = append(batch.products, Production{
		Revision: revision,
		Target:   input.Target,
		Amount:   input.Amount,
		Unit:     unit,
		At:       input.At,
	})
	return l.emit(Event{Type: EventProduction, At: input.At, BatchID: batchID, Revision: revision, Target: input.Target, Amount: input.Amount, Unit: unit})
}

func (b *Batch) hasRevision(revision string) bool {
	for _, phase := range b.phases {
		if phase.Revision == revision {
			return true
		}
	}
	return false
}

func (l *Ledger) emit(event Event) error {
	if l.sink == nil {
		return nil
	}
	return l.sink.Append(event)
}

// OpenBatchID 返回当前未结批次的 ID，无则空串。
func (l *Ledger) OpenBatchID() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.openBatch
}

// HasMeter 判断表计是否已登记。
func (l *Ledger) HasMeter(id string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.meters[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

// BatchCount 返回已登记批次数量（含已结批次）。
func (l *Ledger) BatchCount() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.batches)
}
