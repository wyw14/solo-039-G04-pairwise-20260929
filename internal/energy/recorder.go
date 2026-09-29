package energy

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

// Recorder pairs an Accountant with its durable Log. Every mutating call is
// applied to the accountant and then appended to the journal; on startup the
// accountant is rebuilt with Load, so accounts survive a restart.
type Recorder struct {
	Accountant *Accountant
	Log        *Log
}

func NewRecorder(path string) *Recorder {
	return &Recorder{Accountant: NewAccountant(), Log: NewLog(path)}
}

// Load replays the journal and replaces the in-memory accountant.
func (r *Recorder) Load() error {
	entries, err := r.Log.Read()
	if err != nil {
		return err
	}
	accountant, err := Replay(entries)
	if err != nil {
		return err
	}
	r.Accountant = accountant
	return nil
}

// Begin starts a batch and journals it, returning the new batch.
func (r *Recorder) Begin(c model.Campaign, at int64) (*Batch, error) {
	batch, err := r.Accountant.BeginBatch(c, at)
	if err != nil {
		return nil, err
	}
	if err := r.Log.Append(Entry{Kind: EntryBegin, At: at, BatchID: batch.ID, Campaign: c}); err != nil {
		return nil, err
	}
	return batch, nil
}

// Phase records a phase boundary and journals it.
func (r *Recorder) Phase(c model.Campaign, at int64) error {
	if err := r.Accountant.MarkPhase(c, at); err != nil {
		return err
	}
	return r.Log.Append(Entry{Kind: EntryPhase, At: at, Campaign: c})
}

// End closes the batch and journals it.
func (r *Recorder) End(c model.Campaign, at int64) (*Batch, error) {
	batch, err := r.Accountant.EndBatch(c, at)
	if err != nil {
		return nil, err
	}
	if err := r.Log.Append(Entry{Kind: EntryEnd, At: at, BatchID: batch.ID, Campaign: c}); err != nil {
		return nil, err
	}
	return batch, nil
}

// Product records the qualified product amount against a batch (zero id =
// current/most recent batch).
func (r *Recorder) Product(batchID uuid.UUID, amount float64, unit string) error {
	if err := r.Accountant.RecordProduct(batchID, amount, unit); err != nil {
		return err
	}
	return r.Log.Append(Entry{Kind: EntryProduct, BatchID: batchID, Amount: amount, Unit: unit})
}

// Reading validates, allocates and journals one meter reading.
func (r *Recorder) Reading(reading Reading) error {
	if err := r.Accountant.Ingest(reading); err != nil {
		return err
	}
	return r.Log.Append(Entry{Kind: EntryReading, At: reading.At, Reading: &reading})
}
