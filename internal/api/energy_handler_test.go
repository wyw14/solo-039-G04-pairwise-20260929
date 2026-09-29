package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func do(t *testing.T, h http.Handler, method, target, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed
}

func at(v int64) string { return strconv.FormatInt(v, 10) }

// End-to-end: a batch records steam and cooling water, a product amount is
// entered, the run shuts down, and the per-unit-product intensity is reported.
func TestEnergyAccountingFlow(t *testing.T) {
	server := NewServerAt(filepath.Join(t.TempDir(), "data"))
	h := server.Handler()

	if code, body := do(t, h, http.MethodPost, "/api/campaign/start?grade=baseline&source=primary", ""); code != http.StatusAccepted {
		t.Fatalf("start: %d %v", code, body)
	}
	time.Sleep(1100 * time.Millisecond)
	now := time.Now().Unix()
	steam := `{"meter":"S-1","utility":"steam","value":0,"unit":"t","at":` + at(now) + `}`
	if code, body := do(t, h, http.MethodPost, "/api/energy/readings", steam); code != http.StatusAccepted {
		t.Fatalf("steam baseline: %d %v", code, body)
	}
	water := `{"meter":"CW-1","utility":"cooling-water","value":0,"unit":"m3","at":` + at(now) + `}`
	if code, body := do(t, h, http.MethodPost, "/api/energy/readings", water); code != http.StatusAccepted {
		t.Fatalf("water baseline: %d %v", code, body)
	}
	time.Sleep(1100 * time.Millisecond)
	later := time.Now().Unix()
	steam = `{"meter":"S-1","utility":"steam","value":2.0,"unit":"t","at":` + at(later) + `}`
	if code, body := do(t, h, http.MethodPost, "/api/energy/readings", steam); code != http.StatusAccepted {
		t.Fatalf("steam usage: %d %v", code, body)
	}
	water = `{"meter":"CW-1","utility":"cooling-water","value":20,"unit":"m3","at":` + at(later) + `}`
	if code, body := do(t, h, http.MethodPost, "/api/energy/readings", water); code != http.StatusAccepted {
		t.Fatalf("water usage: %d %v", code, body)
	}
	product := `{"amount":4,"unit":"t"}`
	if code, body := do(t, h, http.MethodPost, "/api/energy/product", product); code != http.StatusAccepted {
		t.Fatalf("product: %d %v", code, body)
	}
	time.Sleep(1100 * time.Millisecond)
	if code, body := do(t, h, http.MethodPost, "/api/shutdown", ""); code != http.StatusAccepted {
		t.Fatalf("shutdown: %d %v", code, body)
	}

	code, body := do(t, h, http.MethodGet, "/api/energy/batches", "")
	if code != http.StatusOK {
		t.Fatalf("batches: %d %v", code, body)
	}
	batches, ok := body["batches"].([]any)
	if !ok || len(batches) != 1 {
		t.Fatalf("expected one batch, got %v", body["batches"])
	}
	first := batches[0].(map[string]any)
	intensities := first["intensities"].([]any)
	steamI := intensities[0].(map[string]any)
	if steamI["utility"] != "steam" {
		t.Fatalf("expected steam first, got %v", steamI)
	}
	if toFloat(steamI["amount"]) < 2-1e-6 {
		t.Fatalf("steam amount = %v", steamI["amount"])
	}
	if toFloat(steamI["per_product"]) < 0.5-1e-6 {
		t.Fatalf("steam intensity = %v, want 0.5 t/t", steamI["per_product"])
	}

	// No open batch after shutdown.
	if code, _ := do(t, h, http.MethodGet, "/api/energy/current", ""); code != http.StatusNotFound {
		t.Fatalf("current after shutdown: %d, want 404", code)
	}
}

// A reading in an incompatible unit is rejected, not silently converted.
func TestEnergyReadingRejectsBadUnit(t *testing.T) {
	server := NewServerAt(filepath.Join(t.TempDir(), "data"))
	h := server.Handler()
	if code, _ := do(t, h, http.MethodPost, "/api/campaign/start?grade=baseline&source=primary", ""); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	time.Sleep(1100 * time.Millisecond)
	now := time.Now().Unix()
	seed := `{"meter":"S-1","utility":"steam","value":0,"unit":"t","at":` + at(now) + `}`
	if code, _ := do(t, h, http.MethodPost, "/api/energy/readings", seed); code != http.StatusAccepted {
		t.Fatalf("seed: %d", code)
	}
	bad := `{"meter":"S-1","utility":"steam","value":1,"unit":"m3","at":` + at(now+1) + `}`
	if code, _ := do(t, h, http.MethodPost, "/api/energy/readings", bad); code != http.StatusBadRequest {
		t.Fatalf("incompatible unit: code = %d, want 400", code)
	}
}

func toFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}
