package api

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/service"
	"net/http"
	"time"
)

func (s *Server) meterReading(w http.ResponseWriter, r *http.Request) {
	var input meterReadingRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if input.At == 0 {
		input.At = time.Now().Unix()
	}
	err := s.plant.RecordMeterReading(service.MeterReadingInput{
		Meter:      input.Meter,
		Utility:    input.Utility,
		Value:      input.Value,
		Unit:       input.Unit,
		At:         input.At,
		RolloverAt: input.RolloverAt,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "recorded"})
}

func (s *Server) productOutput(w http.ResponseWriter, r *http.Request) {
	var input productOutputRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var batchID uuid.UUID
	if input.BatchID != "" {
		parsed, err := uuid.Parse(input.BatchID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid batch_id"})
			return
		}
		batchID = parsed
	}
	if err := s.plant.ProductOutput(batchID, input.Amount, input.Unit); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "recorded"})
}

func (s *Server) energyBatches(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("batch_id")
	if id != "" {
		batchID, err := uuid.Parse(id)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid batch_id"})
			return
		}
		report, ok := s.plant.EnergyBatch(batchID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "batch not found"})
			return
		}
		writeJSON(w, http.StatusOK, report)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batches": s.plant.EnergyBatches(), "summary": s.plant.EnergySummary()})
}

func (s *Server) energyCurrent(w http.ResponseWriter, r *http.Request) {
	report, ok := s.plant.EnergyCurrent()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no open batch"})
		return
	}
	writeJSON(w, http.StatusOK, report)
}
