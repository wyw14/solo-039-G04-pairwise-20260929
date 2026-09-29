package api

import (
	"net/http"
	"time"

	"github.com/wyw14/solo-039/internal/service"
)

func (s *Server) registerMeter(w http.ResponseWriter, r *http.Request) {
	var input service.MeterRegistrationInput
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if input.At == 0 {
		input.At = time.Now().UnixNano()
	}
	meter, err := s.plant.RegisterEnergyMeter(input)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id": meter.ID, "kind": meter.Kind, "unit": meter.Unit,
		"serial": meter.CurrentSN, "segments": len(meter.Segments),
	})
}

func (s *Server) meterReading(w http.ResponseWriter, r *http.Request) {
	var input service.MeterReadingInput
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if input.At == 0 {
		input.At = time.Now().UnixNano()
	}
	segment, err := s.plant.RecordMeterReading(input)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"meter_id": input.MeterID, "segment_serial": segment.Serial, "segment_reason": segment.Reason,
		"start_at": segment.StartAt, "end_at": segment.EndAt, "usage_in_segment": segment.Usage(),
	})
}

func (s *Server) production(w http.ResponseWriter, r *http.Request) {
	var input service.ProductionInput
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if input.At == 0 {
		input.At = time.Now().UnixNano()
	}
	batchID, err := s.plant.RecordProduction(input)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"recorded": input.Amount, "unit": input.Unit, "batch_id": batchID})
}

func (s *Server) energyAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.plant.EnergyAccounts())
}
