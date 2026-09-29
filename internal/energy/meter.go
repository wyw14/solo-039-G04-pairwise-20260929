package energy

import (
	"errors"
)

// Reading is one cumulative totalizer sample taken from a utility meter.
// Value is the meter's accumulated totalizer count (never a per-interval
// delta); Unit is the unit shown on the meter face; At is the sample time in
// Unix seconds. RolloverAt is the meter's full-scale count (display units) and
// is only set when the operator wants a counter wrap recognised rather than a
// meter replacement.
type Reading struct {
	Meter      string  `json:"meter"`
	Utility    Utility `json:"utility"`
	Value      float64 `json:"value"`
	Unit       string  `json:"unit"`
	At         int64   `json:"at"`
	RolloverAt float64 `json:"rollover_at,omitempty"`
}

func (r Reading) Validate() error {
	if r.Meter == "" {
		return errors.New("meter reading is missing meter id")
	}
	if r.Utility != Steam && r.Utility != CoolingWater {
		return errors.New("meter reading utility must be steam or cooling-water")
	}
	if r.At <= 0 {
		return errors.New("meter reading requires a sample time")
	}
	if r.Value < 0 {
		return errors.New("meter totalizer cannot be negative")
	}
	if _, _, err := toBase(r.Value, r.Unit); err != nil {
		return err
	}
	if r.RolloverAt < 0 {
		return errors.New("meter rollover full-scale cannot be negative")
	}
	return nil
}

// SegmentReason describes why a new meter segment began.
type SegmentReason string

const (
	SegmentInitial     SegmentReason = "initial"
	SegmentRunning     SegmentReason = "running"
	SegmentReplacement SegmentReason = "replacement"
	SegmentRollover    SegmentReason = "rollover"
)

// Segment is one uninterrupted stretch of monotonic totalizer readings from a
// single physical meter. Meter swaps and counter wraps always open a new
// segment so consumption can never be smuggled across the boundary.
type Segment struct {
	Seq        int           `json:"seq"`
	Meter      string        `json:"meter"`
	Utility    Utility       `json:"utility"`
	Reason     SegmentReason `json:"reason"`
	Unit       string        `json:"unit"`
	StartAt    int64         `json:"start_at"`
	EndAt      int64         `json:"end_at"`
	StartValue float64       `json:"start_value"` // base units
	EndValue   float64       `json:"end_value"`   // base units
}

// Consumption is the fully-measured usage on the segment in base units.
func (s Segment) Consumption() float64 { return s.EndValue - s.StartValue }

// Gap records the unmeasured stretch left when a meter is replaced: usage
// between the old meter's final reading and the new meter's first reading
// cannot be measured, so it is parked here instead of being guessed.
type Gap struct {
	Meter   string  `json:"meter"`
	Utility Utility `json:"utility"`
	FromAt  int64   `json:"from_at"`
	ToAt    int64   `json:"to_at"`
}

type meterState struct {
	utility Utility
	dim     Dimension
	unit    string // first-seen display unit, used for reporting
	seq     int

	current *Segment
	last    reading
	gaps    []Gap
}

type reading struct {
	value float64 // base units
	at    int64
}

// register validates a reading against meter history and returns the segment
// it belongs to together with the measured consumption since the previous
// reading. A positive interval indicates usage that still has to be allocated
// over [fromAt, toAt]; a zero interval means the reading only established a
// baseline or opened a replacement segment.
type interval struct {
	meter   string
	utility Utility
	fromAt  int64
	toAt    int64
	amount  float64 // base units
	seq     int
}

func (m *meterState) register(r Reading) (interval, *Segment, error) {
	base, dim, err := toBase(r.Value, r.Unit)
	if err != nil {
		return interval{}, nil, err
	}
	if m.utility == "" {
		m.utility = r.Utility
		m.dim = dim
		m.unit, _ = canonicalUnit(r.Unit)
	}
	if r.Utility != m.utility {
		return interval{}, nil, errors.New("meter " + r.Meter + " changed utility")
	}
	if dim != m.dim {
		return interval{}, nil, errors.New("meter " + r.Meter + " changed unit dimension")
	}

	if m.current == nil {
		m.seq++
		seg := &Segment{Seq: m.seq, Meter: r.Meter, Utility: r.Utility, Reason: SegmentInitial, Unit: m.unit, StartAt: r.At, EndAt: r.At, StartValue: base, EndValue: base}
		m.current = seg
		m.last = reading{base, r.At}
		return interval{}, seg, nil
	}

	if r.At < m.last.at {
		return interval{}, nil, errors.New("meter reading arrived out of chronological order")
	}
	if r.At == m.last.at {
		if base != m.last.value {
			return interval{}, nil, errors.New("meter reading conflicts with an earlier value at the same time")
		}
		return interval{}, nil, nil
	}

	if base >= m.last.value {
		m.current.EndAt = r.At
		m.current.EndValue = base
		amount := base - m.last.value
		prev := m.last
		m.last = reading{base, r.At}
		return interval{meter: r.Meter, utility: r.Utility, fromAt: prev.at, toAt: r.At, amount: amount, seq: m.current.Seq}, nil, nil
	}

	// The totalizer moved backwards: either a counter wrap or a replacement.
	reason := SegmentReplacement
	amount := 0.0
	fullScale := 0.0
	if r.RolloverAt > 0 {
		if fs, _, derr := toBase(r.RolloverAt, r.Unit); derr == nil {
			fullScale = fs
		}
	}
	if fullScale > 0 && m.last.value <= fullScale && base <= fullScale {
		reason = SegmentRollover
		amount = (fullScale - m.last.value) + base
	}

	m.seq++
	seg := &Segment{Seq: m.seq, Meter: r.Meter, Utility: r.Utility, Reason: reason, Unit: m.unit, StartAt: r.At, EndAt: r.At, StartValue: base, EndValue: base}

	var iv interval
	if reason == SegmentRollover {
		// The wrap usage is genuinely measured; allocate it like any other
		// interval, but it is bookkept on the new segment.
		iv = interval{meter: r.Meter, utility: r.Utility, fromAt: m.last.at, toAt: r.At, amount: amount, seq: m.seq}
	} else {
		// Replacement: the stretch covered by neither meter is unmeasured and
		// must stay out of every batch.
		m.gaps = append(m.gaps, Gap{Meter: r.Meter, Utility: r.Utility, FromAt: m.last.at, ToAt: r.At})
	}
	m.current = seg
	m.last = reading{base, r.At}
	return iv, seg, nil
}
