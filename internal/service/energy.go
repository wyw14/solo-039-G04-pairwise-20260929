package service

import (
	"fmt"
	"time"

	"github.com/wyw14/solo-039/internal/energy"
	"github.com/wyw14/solo-039/internal/model"
)

// MeterRegistrationInput 是登记公用工程表计的请求。
type MeterRegistrationInput struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Unit         string  `json:"unit"`
	Serial       string  `json:"serial"`
	InitialValue float64 `json:"initial_value"`
	Capacity     float64 `json:"rollover_capacity"`
	At           int64   `json:"at"`
}

// MeterReadingInput 是一次公用工程表计读数上报。
type MeterReadingInput struct {
	MeterID string  `json:"meter_id"`
	Serial  string  `json:"serial"`
	Value   float64 `json:"value"`
	At      int64   `json:"at"`
	Note    string  `json:"note"`
}

// ProductionInput 是批次产品产量登记（用于单位产品能耗）。
type ProductionInput struct {
	BatchID  string  `json:"batch_id"`
	Revision string  `json:"revision"`
	Target   string  `json:"target"`
	Amount   float64 `json:"amount"`
	Unit     string  `json:"unit"`
	At       int64   `json:"at"`
}

// RegisterEnergyMeter 登记蒸汽/冷却水表计。
func (p *Plant) RegisterEnergyMeter(input MeterRegistrationInput) (*energy.Meter, error) {
	if input.At == 0 {
		input.At = time.Now().UnixNano()
	}
	meter, err := p.EnergyLedger.RegisterMeter(energy.MeterRegistration{
		ID: input.ID, Kind: input.Kind, Unit: input.Unit, Serial: input.Serial,
		InitialValue: input.InitialValue, Capacity: input.Capacity, At: input.At,
	})
	if err != nil {
		p.Incidents.Open("energy-accounting", err.Error(), p.Campaign.Snapshot().Revision.String())
		return nil, err
	}
	return meter, nil
}

// RecordMeterReading 接收带单位和时间的累计表读数，自动处理换表/回绕切段。
func (p *Plant) RecordMeterReading(input MeterReadingInput) (*energy.Segment, error) {
	if input.At == 0 {
		input.At = time.Now().UnixNano()
	}
	segment, err := p.EnergyLedger.RecordReading(energy.MeterReading{
		MeterID: input.MeterID, Serial: input.Serial, Value: input.Value, At: input.At, Note: input.Note,
	})
	if err != nil {
		p.Incidents.Open("energy-accounting", err.Error(), p.Campaign.Snapshot().Revision.String())
		return nil, err
	}
	return segment, nil
}

// RecordProduction 登记批次产品产量，单位产品能耗据此计算。返回归属的能源批次号。
func (p *Plant) RecordProduction(input ProductionInput) (string, error) {
	if input.At == 0 {
		input.At = time.Now().UnixNano()
	}
	if input.BatchID == "" {
		input.BatchID = p.EnergyLedger.OpenBatchID()
	}
	if input.Target == "" && p.Product.Route.Opened() {
		input.Target = p.Product.Route.Target()
	}
	if err := p.EnergyLedger.RecordProduction(energy.ProductionInput{
		BatchID: input.BatchID, Revision: input.Revision, Target: input.Target,
		Amount: input.Amount, Unit: input.Unit, At: input.At,
	}); err != nil {
		p.Incidents.Open("energy-accounting", err.Error(), p.Campaign.Snapshot().Revision.String())
		return "", err
	}
	return input.BatchID, nil
}

// EnergyAccounts 返回按批次、阶段、牌号段分摊的能源核算结果。
func (p *Plant) EnergyAccounts() energy.Accounts { return p.EnergyLedger.Accounts() }

// beginEnergyRun 在开批时建立能源时间轴。失败只开事件单，不阻断开车——
// 核算故障必须可见，但不能因记账问题阻止生产。
func (p *Plant) beginEnergyRun(current model.Campaign) {
	runID := fmt.Sprintf("run-%d", p.EnergyLedger.BatchCount()+1)
	if err := p.EnergyLedger.BeginBatch(runID, current.ID.String(), current.Grade, current.Revision.String(), time.Now().UnixNano()); err != nil {
		p.Incidents.Open("energy-accounting", err.Error(), current.Revision.String())
	}
}

func (p *Plant) advanceEnergyPhase(phase model.Phase) {
	current := p.Campaign.Snapshot()
	if err := p.EnergyLedger.AdvanceBatch(phase, current.Revision.String(), current.Grade, time.Now().UnixNano()); err != nil {
		p.Incidents.Open("energy-accounting", err.Error(), current.Revision.String())
	}
}

func (p *Plant) endEnergyRun() {
	current := p.Campaign.Snapshot()
	if err := p.EnergyLedger.EndBatch(time.Now().UnixNano()); err != nil {
		p.Incidents.Open("energy-accounting", err.Error(), current.Revision.String())
	}
}
