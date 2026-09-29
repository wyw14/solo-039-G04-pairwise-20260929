package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/wyw14/solo-039/internal/energy"
)

func TestPlantEnergyLifecycleAndShutdownIsolation(t *testing.T) {
	dir := t.TempDir()
	plant := NewPlant(dir)

	if _, err := plant.Start(context.Background(), "grade-a", "primary"); err != nil {
		t.Fatal(err)
	}
	if open := plant.EnergyLedger.OpenBatchID(); open != "run-1" {
		t.Fatalf("open energy batch = %q, want run-1", open)
	}

	// 开车后登记表，首批读数全部落在批次窗口内。
	if _, err := plant.RegisterEnergyMeter(MeterRegistrationInput{
		ID: "steam-1", Kind: energy.MeterSteam, Unit: "kg", Serial: "SN-A",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant.RegisterEnergyMeter(MeterRegistrationInput{
		ID: "cw-1", Kind: energy.MeterCoolingWater, Unit: "m3", Serial: "SN-W",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant.RecordMeterReading(MeterReadingInput{MeterID: "steam-1", Value: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant.RecordMeterReading(MeterReadingInput{MeterID: "cw-1", Value: 40}); err != nil {
		t.Fatal(err)
	}

	// 三个合格样例后进 on-spec 出料，时间轴应出现 on-spec 段。
	base := time.Now().Unix()
	for i := 1; i <= 3; i++ {
		if _, err := plant.AddSample(0.95, base+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := plant.RouteProduct("T-201"); err != nil {
		t.Fatal(err)
	}
	// 出料后再抄一轮，on-spec 阶段应分到用量。
	if _, err := plant.RecordMeterReading(MeterReadingInput{MeterID: "steam-1", Value: 130}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant.RecordMeterReading(MeterReadingInput{MeterID: "cw-1", Value: 52}); err != nil {
		t.Fatal(err)
	}

	if batchID, err := plant.RecordProduction(ProductionInput{Amount: 500, Unit: "kg"}); err != nil {
		t.Fatal(err)
	} else if batchID != "run-1" {
		t.Fatalf("production assigned to %q, want run-1", batchID)
	}

	batch1, ok := plant.EnergyLedger.BatchAccountByID("run-1")
	if !ok {
		t.Fatal("run-1 missing")
	}
	if batch1.Consumption[energy.MeterSteam] != 130 || batch1.Consumption[energy.MeterCoolingWater] != 52 {
		t.Fatalf("run-1 consumption wrong: %+v", batch1.Consumption)
	}
	if batch1.UnitEnergy[energy.MeterSteam] != 0.26 {
		t.Fatalf("run-1 unit steam = %f, want 0.26", batch1.UnitEnergy[energy.MeterSteam])
	}
	if _, hasOnSpec := batch1.PhaseUsage["on-spec"]; !hasOnSpec {
		t.Fatalf("on-spec phase missing from timeline: %+v", batch1.PhaseUsage)
	}

	if _, err := plant.Shutdown(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if open := plant.EnergyLedger.OpenBatchID(); open != "" {
		t.Fatalf("energy batch should be closed, got %q", open)
	}

	// 停机后抄到的用量：区间按边界切分，落在停机窗口的份额进 unassigned，
	// 不会整笔转嫁给下一批。
	time.Sleep(10 * time.Millisecond)
	if _, err := plant.RecordMeterReading(MeterReadingInput{MeterID: "steam-1", Value: 135}); err != nil {
		t.Fatal(err)
	}
	accounts := plant.EnergyAccounts()
	idleSteam := accounts.Unassigned[energy.MeterSteam]
	if idleSteam <= 0 {
		t.Fatalf("idle steam = %f, want > 0 unassigned after shutdown", idleSteam)
	}
	if idleSteam > 5 {
		t.Fatalf("idle steam = %f exceeds the 5 units read after routing", idleSteam)
	}
	run1Final, _ := plant.EnergyLedger.BatchAccountByID("run-1")
	// 守恒：旧表 [0,135] 的 135 个单位只能在 批次1 与 停机未分摊 之间分配。
	if got := run1Final.Consumption[energy.MeterSteam] + idleSteam; math.Abs(got-135) > 1e-6 {
		t.Fatalf("steam not conserved: batch1=%f idle=%f sum=%f", run1Final.Consumption[energy.MeterSteam], idleSteam, got)
	}

	// 换进程：新 Plant 从 energy.jsonl 回放，账目必须连续。
	plant2 := NewPlant(dir)
	if open := plant2.EnergyLedger.OpenBatchID(); open != "" {
		t.Fatalf("replayed ledger should have no open batch, got %q", open)
	}
	if _, err := plant2.Start(context.Background(), "grade-b", "primary"); err != nil {
		t.Fatal(err)
	}
	if open := plant2.EnergyLedger.OpenBatchID(); open != "run-2" {
		t.Fatalf("second run id = %q, want run-2", open)
	}
	// 换表：停机期间装了新表 SN-B，新表从 20 起步。
	if _, err := plant2.RecordMeterReading(MeterReadingInput{MeterID: "steam-1", Serial: "SN-B", Value: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant2.RecordMeterReading(MeterReadingInput{MeterID: "steam-1", Serial: "SN-B", Value: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant2.Shutdown(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	replayed := plant2.EnergyAccounts()
	if len(replayed.Batches) != 2 {
		t.Fatalf("want 2 batches after replay, got %d", len(replayed.Batches))
	}
	var run2 energy.BatchAccount
	for _, b := range replayed.Batches {
		if b.BatchID == "run-2" {
			run2 = b
		}
	}
	if run2.Consumption[energy.MeterSteam] != 10 {
		t.Fatalf("run-2 steam = %f, want only its own 10 (no carryover)", run2.Consumption[energy.MeterSteam])
	}
	if replayed.Unassigned[energy.MeterSteam] != idleSteam {
		t.Fatalf("unassigned steam after replay = %f, want %f", replayed.Unassigned[energy.MeterSteam], idleSteam)
	}
}

func TestPlantRolloverIncidentAndRejectedDecrease(t *testing.T) {
	dir := t.TempDir()
	plant := NewPlant(dir)
	if _, err := plant.Start(context.Background(), "grade-a", "primary"); err != nil {
		t.Fatal(err)
	}
	// 未声明量程：读数倒退必须拒绝并开事件单。
	if _, err := plant.RegisterEnergyMeter(MeterRegistrationInput{
		ID: "steam-2", Kind: energy.MeterSteam, Unit: "kg", Serial: "SN-C", InitialValue: 90,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant.RecordMeterReading(MeterReadingInput{MeterID: "steam-2", Value: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant.RecordMeterReading(MeterReadingInput{MeterID: "steam-2", Value: 5}); err == nil {
		t.Fatal("decrease without rollover capacity must be rejected")
	}
	if plant.Incidents.CountOpen() == 0 {
		t.Fatal("rejected reading should open an incident")
	}

	// 声明量程的表：回绕按 1000-995+5 补算。
	if _, err := plant.RegisterEnergyMeter(MeterRegistrationInput{
		ID: "steam-3", Kind: energy.MeterSteam, Unit: "kg", Serial: "SN-D",
		InitialValue: 990, Capacity: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := plant.RecordMeterReading(MeterReadingInput{MeterID: "steam-3", Value: 995}); err != nil {
		t.Fatal(err)
	}
	seg, err := plant.RecordMeterReading(MeterReadingInput{MeterID: "steam-3", Value: 5})
	if err != nil {
		t.Fatal(err)
	}
	if seg.Reason != energy.SegmentRollover {
		t.Fatalf("want rolled_over, got %s", seg.Reason)
	}

	if _, err := plant.Shutdown(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	accounts := plant.EnergyAccounts()
	// steam-2: 10；steam-3: 5（同段）+ 10（回绕桥 1000-995+5）= 15。合计 25。
	if accounts.Consumption[energy.MeterSteam] != 25 {
		t.Fatalf("steam total = %f, want 10 (s-2) + 15 (s-3) = 25", accounts.Consumption[energy.MeterSteam])
	}
}
