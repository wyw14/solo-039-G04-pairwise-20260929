package energy

// 能源账目事件类型。
const (
	EventMeterRegistered = "energy.meter_registered"
	EventMeterReplaced   = "energy.meter_replaced"
	EventMeterRollover   = "energy.meter_rolled_over"
	EventReading         = "energy.reading"
	EventBatchBegan      = "energy.batch_began"
	EventPhaseAdvanced   = "energy.phase_advanced"
	EventBatchEnded      = "energy.batch_ended"
	EventProduction      = "energy.production"
)

// Event 是能源核算账目的一条持久化记录（JSONL）。
// 表读数区间不直接落盘——重放时由相邻读数与表计量程重新推导，
// 保证换表/回绕段的判定只有一个事实来源。
type Event struct {
	Type    string `json:"type"`
	At      int64  `json:"at"`
	MeterID string `json:"meter_id,omitempty"`
	BatchID string `json:"batch_id,omitempty"`
	Ref     string `json:"campaign_ref,omitempty"`

	// 表计登记
	Kind         string  `json:"kind,omitempty"`
	Unit         string  `json:"unit,omitempty"`
	Serial       string  `json:"serial,omitempty"`
	InitialValue float64 `json:"initial_value,omitempty"`
	Capacity     float64 `json:"capacity,omitempty"`

	// 读数
	Value float64 `json:"value,omitempty"`
	Note  string  `json:"note,omitempty"`

	// 批次/阶段
	Grade    string `json:"grade,omitempty"`
	Revision string `json:"revision,omitempty"`
	Phase    string `json:"phase,omitempty"`

	// 产品产量
	Target string  `json:"target,omitempty"`
	Amount float64 `json:"amount,omitempty"`
}
