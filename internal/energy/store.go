package energy

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/wyw14/solo-039/internal/model"
)

// FileStore 把能源账目事件以 JSONL 追加落盘，并支持重放重建账本。
type FileStore struct {
	mu   sync.Mutex
	path string
}

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (s *FileStore) Path() string { return s.path }

// Append 实现 EventSink。
func (s *FileStore) Append(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(event); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// Read 读回全部账目事件。
func (s *FileStore) Read() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	events := make([]Event, 0, 128)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

// Replay 按时间顺序把账目事件重放到一个未挂 sink 的账本上，
// 重建表计分段与批次时间轴。重放失败返回第一条错误事件的信息。
func Replay(ledger *Ledger, events []Event) error {
	for _, event := range events {
		if err := apply(ledger, event); err != nil {
			return err
		}
	}
	return nil
}

func apply(ledger *Ledger, event Event) error {
	switch event.Type {
	case EventMeterRegistered:
		_, err := ledger.RegisterMeter(MeterRegistration{
			ID: event.MeterID, Kind: event.Kind, Unit: event.Unit, Serial: event.Serial,
			InitialValue: event.InitialValue, Capacity: event.Capacity, At: event.At,
		})
		return err
	case EventMeterReplaced:
		_, err := ledger.RecordReading(MeterReading{
			MeterID: event.MeterID, Serial: event.Serial, Value: event.Value, At: event.At, Note: event.Note,
		})
		return err
	case EventMeterRollover, EventReading:
		_, err := ledger.RecordReading(MeterReading{
			MeterID: event.MeterID, Value: event.Value, At: event.At, Note: event.Note,
		})
		return err
	case EventBatchBegan:
		return ledger.BeginBatch(event.BatchID, event.Ref, event.Grade, event.Revision, event.At)
	case EventPhaseAdvanced:
		return ledger.AdvanceBatch(model.Phase(event.Phase), event.Revision, event.Grade, event.At)
	case EventBatchEnded:
		return ledger.EndBatch(event.At)
	case EventProduction:
		return ledger.RecordProduction(ProductionInput{
			BatchID: event.BatchID, Revision: event.Revision, Target: event.Target,
			Amount: event.Amount, Unit: event.Unit, At: event.At,
		})
	default:
		return errors.New("unknown energy event: " + event.Type)
	}
}
