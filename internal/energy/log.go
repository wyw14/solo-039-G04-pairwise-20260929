package energy

import (
	"bufio"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"os"
	"path/filepath"
	"sync"
)

// EntryKind tags one durable accounting event.
type EntryKind string

const (
	EntryBegin   EntryKind = "batch.begin"
	EntryPhase   EntryKind = "batch.phase"
	EntryEnd     EntryKind = "batch.end"
	EntryProduct EntryKind = "batch.product"
	EntryReading EntryKind = "meter.reading"
)

// Entry is one append-only record of the energy journal. The accountant can be
// fully rebuilt by replaying entries in order, which is how meter segments and
// batch allocations survive a process restart.
type Entry struct {
	Kind     EntryKind      `json:"kind"`
	At       int64          `json:"at"`
	BatchID  uuid.UUID      `json:"batch_id,omitempty"`
	Campaign model.Campaign `json:"campaign,omitempty"`
	Amount   float64        `json:"amount,omitempty"`
	Unit     string         `json:"unit,omitempty"`
	Reading  *Reading       `json:"reading,omitempty"`
}

// Log is an fsync'd JSONL file of accounting entries.
type Log struct {
	mu   sync.Mutex
	path string
}

func NewLog(path string) *Log { return &Log{path: path} }

func (l *Log) Path() string { return l.path }

func (l *Log) Append(entry Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(entry); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func (l *Log) Read() ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	file, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := make([]Entry, 0, 64)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

// Replay rebuilds an accountant from journal entries.
func Replay(entries []Entry) (*Accountant, error) {
	a := NewAccountant()
	for _, entry := range entries {
		var err error
		switch entry.Kind {
		case EntryBegin:
			_, err = a.BeginBatchID(entry.Campaign, entry.At, entry.BatchID)
		case EntryPhase:
			err = a.MarkPhase(entry.Campaign, entry.At)
		case EntryEnd:
			_, err = a.EndBatch(entry.Campaign, entry.At)
		case EntryProduct:
			err = a.RecordProduct(entry.BatchID, entry.Amount, entry.Unit)
		case EntryReading:
			if entry.Reading == nil {
				return nil, errors.New("reading entry is missing reading payload")
			}
			err = a.Ingest(*entry.Reading)
		default:
			return nil, errors.New("unknown energy journal entry: " + string(entry.Kind))
		}
		if err != nil {
			return nil, err
		}
	}
	return a, nil
}
