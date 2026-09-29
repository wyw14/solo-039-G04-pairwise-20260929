package energy

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/wyw14/solo-039/internal/model"
)

func approx(t *testing.T, got, want float64, msg string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s: got %.6f want %.6f", msg, got, want)
	}
}

func regSteam(t *testing.T, l *Ledger, id, serial string, init, capacity float64, at int64) {
	t.Helper()
	if _, err := l.RegisterMeter(MeterRegistration{ID: id, Kind: MeterSteam, Unit: "kg", Serial: serial, InitialValue: init, Capacity: capacity, At: at}); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
}

func regWater(t *testing.T, l *Ledger, id, serial string, init, capacity float64, at int64) {
	t.Helper()
	if _, err := l.RegisterMeter(MeterRegistration{ID: id, Kind: MeterCoolingWater, Unit: "m3", Serial: serial, InitialValue: init, Capacity: capacity, At: at}); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
}

func TestMeterReplacementStartsSeparateSegment(t *testing.T) {
	l := NewLedger()
	regSteam(t, l, "s-1", "SN-A", 100, 0, 1000)

	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 110, At: 1010}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 120, At: 1020}); err != nil {
		t.Fatal(err)
	}
	// 换表：序列号变成 SN-B，新表首读 5。绝不与旧表 120 跨表相减。
	seg, err := l.RecordReading(MeterReading{MeterID: "s-1", Serial: "SN-B", Value: 5, At: 1030})
	if err != nil {
		t.Fatal(err)
	}
	if seg.Reason != SegmentReplaced || seg.Serial != "SN-B" {
		t.Fatalf("new segment should be replacement, got %s/%s", seg.Reason, seg.Serial)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 15, At: 1040}); err != nil {
		t.Fatal(err)
	}

	accounts := l.Accounts()
	if len(accounts.Meters) != 1 || len(accounts.Meters[0].Segments) != 2 {
		t.Fatalf("want 2 segments, got %d", len(accounts.Meters[0].Segments))
	}
	// 旧段 20 + 新段 10 = 30；换表时刻没有凭空产生 -115 或其他用量。
	approx(t, accounts.Consumption[MeterSteam], 30, "total steam after replacement")
	first := accounts.Meters[0].Segments[0]
	second := accounts.Meters[0].Segments[1]
	approx(t, first.Usage, 20, "old segment usage")
	approx(t, second.Usage, 10, "new segment usage")
	if first.EndValue != 120 || second.StartValue != 5 {
		t.Fatalf("segment boundaries wrong: %+v %+v", first, second)
	}
}

func TestMeterRolloverBridgesByCapacity(t *testing.T) {
	l := NewLedger()
	regSteam(t, l, "s-1", "SN-A", 980, 1000, 1000)

	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 995, At: 1010}); err != nil {
		t.Fatal(err)
	}
	seg, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 5, At: 1020})
	if err != nil {
		t.Fatal(err)
	}
	if seg.Reason != SegmentRollover {
		t.Fatalf("want rolled_over segment, got %s", seg.Reason)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 20, At: 1030}); err != nil {
		t.Fatal(err)
	}

	accounts := l.Accounts()
	// 15（同段）+ 10（回绕桥：1000-995+5）+ 15（新段）= 40
	approx(t, accounts.Consumption[MeterSteam], 40, "rollover total usage")
	if len(accounts.Meters[0].Segments) != 2 {
		t.Fatalf("want 2 segments around rollover, got %d", len(accounts.Meters[0].Segments))
	}
}

func TestDecreaseRejectedWithoutRolloverCapacity(t *testing.T) {
	l := NewLedger()
	regSteam(t, l, "s-1", "SN-A", 100, 0, 1000)
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 120, At: 1010}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 10, At: 1020}); err == nil {
		t.Fatal("reading decrease without rollover capacity must be rejected")
	}
}

func TestReadingOrderingAndUnknownMeter(t *testing.T) {
	l := NewLedger()
	regSteam(t, l, "s-1", "SN-A", 0, 0, 1000)
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 10, At: 1010}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 12, At: 900}); err == nil {
		t.Fatal("out-of-order reading must be rejected")
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "missing", Value: 1, At: 1020}); err == nil {
		t.Fatal("reading from unregistered meter must be rejected")
	}
	if _, err := l.RegisterMeter(MeterRegistration{ID: "s-1", Kind: MeterSteam, Unit: "kg", Serial: "X", At: 1000}); err == nil {
		t.Fatal("duplicate meter registration must be rejected")
	}
}

func TestAllocationAcrossPhasesAndUnitEnergy(t *testing.T) {
	l := NewLedger()
	regSteam(t, l, "s-1", "SN-A", 0, 0, 1000)
	regWater(t, l, "w-1", "SN-W", 0, 0, 1000)

	if err := l.BeginBatch("run-1", "campaign-1", "grade-a", "rev-1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := l.AdvanceBatch(model.Warming, "rev-1", "grade-a", 1050); err != nil { // warming
		t.Fatal(err)
	}
	if err := l.AdvanceBatch(model.Stabilizing, "rev-1", "grade-a", 1080); err != nil { // stabilizing
		t.Fatal(err)
	}
	// [1000,1100] 区间用量 100，准备/升温/稳定 = 50%/30%/20%
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 100, At: 1100}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "w-1", Value: 40, At: 1100}); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1100); err != nil {
		t.Fatal(err)
	}

	if err := l.RecordProduction(ProductionInput{BatchID: "run-1", Revision: "rev-1", Target: "T1", Amount: 500, Unit: "kg", At: 1090}); err != nil {
		t.Fatal(err)
	}

	account, ok := l.BatchAccountByID("run-1")
	if !ok {
		t.Fatal("batch run-1 missing")
	}
	approx(t, account.Consumption[MeterSteam], 100, "batch steam")
	approx(t, account.Consumption[MeterCoolingWater], 40, "batch cooling water")
	if _, present := account.PhaseUsage["on-spec"]; present {
		t.Fatal("on-spec phase had no timeline share, must be absent")
	}
	approx(t, account.PhaseUsage["preparing"][MeterSteam], 50, "preparing share")
	approx(t, account.PhaseUsage["warming"][MeterSteam], 30, "warming share")
	approx(t, account.PhaseUsage["stabilizing"][MeterSteam], 20, "stabilizing share")
	approx(t, account.UnitEnergy[MeterSteam], 0.2, "steam per product unit")
	approx(t, account.UnitEnergy[MeterCoolingWater], 0.08, "water per product unit")
	if len(account.Revisions) != 1 {
		t.Fatalf("want 1 revision account, got %d", len(account.Revisions))
	}
	if account.Production != 500 || account.ProductionUnit != "kg" {
		t.Fatalf("production ledger wrong: %+v", account)
	}
}

func TestShutdownUsageNotCarriedToNextBatch(t *testing.T) {
	l := NewLedger()
	regSteam(t, l, "s-1", "SN-A", 0, 0, 1000)

	if err := l.BeginBatch("run-1", "c-1", "g", "r-1", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 100, At: 1100}); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1100); err != nil {
		t.Fatal(err)
	}
	// 停机窗口 [1100,1200] 的 30 个单位
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 130, At: 1200}); err != nil {
		t.Fatal(err)
	}
	// 下一批 [1200,1300] 用 70
	if err := l.BeginBatch("run-2", "c-2", "g", "r-2", 1200); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 200, At: 1300}); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1300); err != nil {
		t.Fatal(err)
	}

	accounts := l.Accounts()
	if len(accounts.Batches) != 2 {
		t.Fatalf("want 2 batches, got %d", len(accounts.Batches))
	}
	approx(t, accounts.Batches[0].Consumption[MeterSteam], 100, "first batch consumption")
	approx(t, accounts.Batches[1].Consumption[MeterSteam], 70, "second batch must not inherit shutdown usage")
	approx(t, accounts.Unassigned[MeterSteam], 30, "shutdown usage must be unassigned")
	approx(t, accounts.Consumption[MeterSteam], 200, "total metered usage")
}

func TestIntervalSpanningBatchBoundarySplitProportionally(t *testing.T) {
	l := NewLedger()
	regSteam(t, l, "s-1", "SN-A", 0, 0, 1000)
	if err := l.BeginBatch("run-1", "c-1", "g", "r-1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1100); err != nil {
		t.Fatal(err)
	}
	if err := l.BeginBatch("run-2", "c-2", "g", "r-2", 1200); err != nil {
		t.Fatal(err)
	}
	// 一条读数区间横跨 上批/停机/下批 各 100 个时间单位
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 300, At: 1300}); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1300); err != nil {
		t.Fatal(err)
	}

	accounts := l.Accounts()
	approx(t, accounts.Batches[0].Consumption[MeterSteam], 100, "batch1 share of spanning interval")
	approx(t, accounts.Batches[1].Consumption[MeterSteam], 100, "batch2 share of spanning interval")
	approx(t, accounts.Unassigned[MeterSteam], 100, "idle gap share of spanning interval")
}

func TestReplacementDuringShutdownFabricatesNoUsage(t *testing.T) {
	l := NewLedger()
	regSteam(t, l, "s-1", "SN-A", 0, 0, 1000)
	if err := l.BeginBatch("run-1", "c-1", "g", "r-1", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 100, At: 1100}); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1100); err != nil {
		t.Fatal(err)
	}
	// 停机期间换表：新表从 50 起步，仅作锚点
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Serial: "SN-B", Value: 50, At: 1200}); err != nil {
		t.Fatal(err)
	}
	if err := l.BeginBatch("run-2", "c-2", "g", "r-2", 1200); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 60, At: 1300}); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1300); err != nil {
		t.Fatal(err)
	}

	accounts := l.Accounts()
	approx(t, accounts.Batches[0].Consumption[MeterSteam], 100, "batch1")
	approx(t, accounts.Batches[1].Consumption[MeterSteam], 10, "batch2 only counts its own meter")
	if u := accounts.Unassigned[MeterSteam]; u != 0 {
		t.Fatalf("replacement anchor must not create unassigned usage, got %f", u)
	}
}

func TestRevisionSplitOnGradeTransition(t *testing.T) {
	l := NewLedger()
	regSteam(t, l, "s-1", "SN-A", 0, 0, 1000)
	if err := l.BeginBatch("run-1", "c-1", "grade-a", "rev-1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := l.AdvanceBatch(model.Transitioning, "rev-2", "grade-b", 1050); err != nil { // transitioning
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 100, At: 1100}); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1100); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordProduction(ProductionInput{BatchID: "run-1", Revision: "rev-1", Amount: 100, Unit: "kg", At: 1040}); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordProduction(ProductionInput{BatchID: "run-1", Revision: "rev-2", Amount: 400, Unit: "kg", At: 1090}); err != nil {
		t.Fatal(err)
	}

	account, ok := l.BatchAccountByID("run-1")
	if !ok {
		t.Fatal("batch missing")
	}
	approx(t, account.Consumption[MeterSteam], 100, "batch total")
	approx(t, account.UnitEnergy[MeterSteam], 0.2, "batch unit energy")
	if len(account.Revisions) != 2 {
		t.Fatalf("want 2 revision accounts, got %d", len(account.Revisions))
	}
	approx(t, account.Revisions[0].Consumption[MeterSteam], 50, "rev1 consumption")
	approx(t, account.Revisions[1].Consumption[MeterSteam], 50, "rev2 consumption")
	approx(t, account.Revisions[0].UnitEnergy[MeterSteam], 0.5, "rev1 unit energy")
	approx(t, account.Revisions[1].UnitEnergy[MeterSteam], 0.125, "rev2 unit energy")
	if account.Revisions[1].Grade != "grade-b" {
		t.Fatalf("grade not carried to revision account: %s", account.Revisions[1].Grade)
	}
}

func TestProductionValidation(t *testing.T) {
	l := NewLedger()
	if err := l.BeginBatch("run-1", "c-1", "g", "r-1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1100); err != nil {
		t.Fatal(err)
	}
	cases := []ProductionInput{
		{BatchID: "run-1", Revision: "r-1", Amount: 0, Unit: "kg", At: 1050},
		{BatchID: "run-1", Revision: "r-1", Amount: 10, Unit: "", At: 1050},
		{BatchID: "run-1", Revision: "r-1", Amount: 10, Unit: "kg", At: 1200}, // 超出已结批次窗口
		{BatchID: "missing", Revision: "r-1", Amount: 10, Unit: "kg", At: 1050},
		{BatchID: "run-1", Revision: "other", Amount: 10, Unit: "kg", At: 1050},
	}
	for i, input := range cases {
		if err := l.RecordProduction(input); err == nil {
			t.Fatalf("case %d must be rejected", i)
		}
	}
}

func TestReplayRebuildsSegmentsAndAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "energy.jsonl")
	store := NewFileStore(path)
	l := NewLedger().WithSink(store)
	regSteam(t, l, "s-1", "SN-A", 980, 1000, 1000)
	regWater(t, l, "w-1", "SN-W", 0, 0, 1000)
	if err := l.BeginBatch("run-1", "c-1", "g", "r-1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := l.AdvanceBatch(model.Warming, "r-1", "g", 1050); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 995, At: 1010}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "s-1", Value: 5, At: 1060}); err != nil {
		t.Fatal(err) // 回绕发生在 warming 阶段
	}
	if _, err := l.RecordReading(MeterReading{MeterID: "w-1", Value: 8, At: 1080}); err != nil {
		t.Fatal(err)
	}
	if err := l.EndBatch(1100); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordProduction(ProductionInput{BatchID: "run-1", Revision: "r-1", Amount: 25, Unit: "kg", At: 1090}); err != nil {
		t.Fatal(err)
	}
	want := l.Accounts()

	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	rebuilt := NewLedger()
	if err := Replay(rebuilt, events); err != nil {
		t.Fatal(err)
	}
	got := rebuilt.Accounts()

	approx(t, got.Consumption[MeterSteam], want.Consumption[MeterSteam], "replayed steam total")
	approx(t, got.Consumption[MeterCoolingWater], want.Consumption[MeterCoolingWater], "replayed water total")
	approx(t, got.Unassigned[MeterSteam], want.Unassigned[MeterSteam], "replayed unassigned")
	if len(got.Batches) != 1 || got.Batches[0].BatchID != "run-1" || got.Batches[0].CampaignRef != "c-1" {
		t.Fatalf("batch not replayed: %+v", got.Batches)
	}
	approx(t, got.Batches[0].UnitEnergy[MeterSteam], want.Batches[0].UnitEnergy[MeterSteam], "replayed unit energy")
	steamMeter := got.Meters[0]
	if len(steamMeter.Segments) != 2 || steamMeter.Segments[1].Reason != SegmentRollover {
		t.Fatalf("rollover segment not replayed: %+v", steamMeter.Segments)
	}
	if rebuilt.OpenBatchID() != "" {
		t.Fatal("closed batch should remain closed after replay")
	}
}
