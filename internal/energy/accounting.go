package energy

import (
	"sort"

	"github.com/wyw14/solo-039/internal/model"
)

// RevisionAccount 是一个牌号段（revision）内的能源核算结果。
type RevisionAccount struct {
	Revision       string             `json:"revision"`
	Grade          string             `json:"grade"`
	StartAt        int64              `json:"start_at"`
	EndAt          int64              `json:"end_at"`
	Consumption    map[string]float64 `json:"consumption"` // 按公用工程种类的用量
	Production     float64            `json:"production"`  // 该牌号段产品产量
	ProductionUnit string             `json:"production_unit"`
	UnitEnergy     map[string]float64 `json:"unit_energy"` // 单位产品能耗，按种类
}

// BatchAccount 是一个批次的能源核算结果。
type BatchAccount struct {
	BatchID        string                        `json:"batch_id"`
	CampaignRef    string                        `json:"campaign_ref"`
	Grade          string                        `json:"grade"`
	StartAt        int64                         `json:"start_at"`
	EndAt          int64                         `json:"end_at"`
	Open           bool                          `json:"open"`
	Consumption    map[string]float64            `json:"consumption"` // 批次总用量（仅批次窗口内）
	PhaseUsage     map[string]map[string]float64 `json:"phase_usage"` // 阶段 -> 种类 -> 用量
	Production     float64                       `json:"production"`
	ProductionUnit string                        `json:"production_unit"`
	UnitEnergy     map[string]float64            `json:"unit_energy"`
	Revisions      []RevisionAccount             `json:"revisions"`
}

// MeterStatus 暴露一只表的分段台账，换表/回绕段可单独核对。
type MeterStatus struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Unit     string          `json:"unit"`
	Serial   string          `json:"serial"`
	Capacity float64         `json:"capacity,omitempty"`
	Readings int             `json:"readings"`
	Segments []SegmentStatus `json:"segments"`
}

// SegmentStatus 是表计分段的对外视图。
type SegmentStatus struct {
	Serial     string  `json:"serial"`
	Reason     string  `json:"reason"`
	StartAt    int64   `json:"start_at"`
	EndAt      int64   `json:"end_at"`
	StartValue float64 `json:"start_value"`
	EndValue   float64 `json:"end_value"`
	Usage      float64 `json:"usage"`
}

// Accounts 是全量能源核算结果。
type Accounts struct {
	Batches     []BatchAccount     `json:"batches"`
	Unassigned  map[string]float64 `json:"unassigned"`  // 批次窗口外的停机/空耗用量
	Consumption map[string]float64 `json:"consumption"` // 全部已计量用量
	Meters      []MeterStatus      `json:"meters"`
}

// intervals 从各表读数段重建用量区间。
// 换表段之间不建桥（跨表用量无法计量，不参与分摊）；
// 回绕段建一条带量程补算的桥接区间。
func (l *Ledger) intervals() []Interval {
	l.mu.RLock()
	defer l.mu.RUnlock()
	intervals := make([]Interval, 0)
	for _, id := range l.meterOrder {
		meter := l.meters[id]
		for idx, segment := range meter.Segments {
			if segment.Reason == SegmentRollover && idx > 0 {
				prev := meter.Segments[idx-1]
				// 仅承接同一块表（序列号相同）的回绕；换表段之间永不架桥。
				if prev.Serial == segment.Serial {
					if n := len(prev.readings); n > 0 {
						last := prev.readings[n-1]
						intervals = append(intervals, Interval{
							MeterID: id, Kind: meter.Kind, Unit: meter.Unit,
							FromAt: last.At, ToAt: segment.StartAt,
							FromVal: last.Value, ToVal: segment.StartValue,
							Rollover: true, Capacity: meter.Capacity,
						})
					}
				}
			}
			for j := 1; j < len(segment.readings); j++ {
				prev, cur := segment.readings[j-1], segment.readings[j]
				intervals = append(intervals, Interval{
					MeterID: id, Kind: meter.Kind, Unit: meter.Unit,
					FromAt: prev.At, ToAt: cur.At, FromVal: prev.Value, ToVal: cur.Value,
				})
			}
		}
	}
	sort.SliceStable(intervals, func(i, j int) bool {
		if intervals[i].FromAt != intervals[j].FromAt {
			return intervals[i].FromAt < intervals[j].FromAt
		}
		return intervals[i].MeterID < intervals[j].MeterID
	})
	return intervals
}

type phaseSlot struct {
	batchID  string
	grade    string
	phase    model.Phase
	revision string
	start    int64
	end      int64 // 0 = 进行中
}

type allocKey struct {
	batchID  string
	phase    model.Phase
	revision string
}

type alloc struct {
	batchID  string
	grade    string
	phase    model.Phase
	revision string
	amounts  map[string]float64
}

// Accounts 依据当前表计读数与批次时间轴做全量核算。
func (l *Ledger) Accounts() Accounts {
	l.mu.RLock()
	batchOrder := append([]string(nil), l.batchOrder...)
	batches := make(map[string]*Batch, len(l.batches))
	for id, b := range l.batches {
		batches[id] = b
	}
	meterOrder := append([]string(nil), l.meterOrder...)
	meters := make(map[string]*Meter, len(l.meters))
	for id, m := range l.meters {
		meters[id] = m
	}
	l.mu.RUnlock()

	// 摊平批次时间轴为有序阶段槽。
	slots := make([]phaseSlot, 0)
	for _, id := range batchOrder {
		batch := batches[id]
		for _, phase := range batch.phases {
			slots = append(slots, phaseSlot{
				batchID: batch.ID, grade: phase.Grade, phase: phase.Phase,
				revision: phase.Revision, start: phase.Start, end: phase.End,
			})
		}
	}
	sort.SliceStable(slots, func(i, j int) bool { return slots[i].start < slots[j].start })

	allocs := make(map[allocKey]*alloc)
	unassigned := map[string]float64{}
	total := map[string]float64{}

	assign := func(k allocKey, grade string, kind string, amount float64) {
		if amount == 0 {
			return
		}
		a, ok := allocs[k]
		if !ok {
			a = &alloc{batchID: k.batchID, grade: grade, phase: k.phase, revision: k.revision, amounts: map[string]float64{}}
			allocs[k] = a
		}
		a.amounts[kind] += amount
	}

	for _, interval := range l.intervals() {
		usage := interval.Usage()
		total[interval.Kind] += usage
		if interval.FromAt >= interval.ToAt {
			// 同一时刻的读数差归到该时刻所处的阶段槽；批次外则停机未分摊。
			if slot, ok := slotAt(slots, interval.ToAt); ok {
				assign(allocKey{slot.batchID, slot.phase, slot.revision}, slot.grade, interval.Kind, usage)
			} else {
				unassigned[interval.Kind] += usage
			}
			continue
		}
		duration := float64(interval.ToAt - interval.FromAt)
		covered := 0.0
		for _, slot := range slots {
			from, to := interval.FromAt, interval.ToAt
			if slot.start > from {
				from = slot.start
			}
			end := slot.end
			if end == 0 || end > to {
				end = to
			}
			if end <= from {
				continue
			}
			share := usage * float64(end-from) / duration
			assign(allocKey{slot.batchID, slot.phase, slot.revision}, slot.grade, interval.Kind, share)
			covered += float64(end - from)
		}
		if gap := duration - covered; gap > 0 {
			unassigned[interval.Kind] += usage * gap / duration
		}
	}

	accounts := make([]BatchAccount, 0, len(batchOrder))
	for _, id := range batchOrder {
		batch := batches[id]
		accounts = append(accounts, buildBatchAccount(batch, allocs))
	}

	meterStatuses := make([]MeterStatus, 0, len(meterOrder))
	for _, id := range meterOrder {
		meter := meters[id]
		status := MeterStatus{ID: id, Kind: meter.Kind, Unit: meter.Unit, Serial: meter.CurrentSN, Capacity: meter.Capacity}
		for _, segment := range meter.Segments {
			status.Readings += len(segment.readings)
			status.Segments = append(status.Segments, SegmentStatus{
				Serial: segment.Serial, Reason: segment.Reason,
				StartAt: segment.StartAt, EndAt: segment.EndAt,
				StartValue: segment.StartValue, EndValue: segment.EndValue,
				Usage: segment.Usage(),
			})
		}
		meterStatuses = append(meterStatuses, status)
	}

	return Accounts{Batches: accounts, Unassigned: unassigned, Consumption: total, Meters: meterStatuses}
}

// slotAt 返回某时刻所处的阶段槽（用于零时间间隔归属）。
func slotAt(slots []phaseSlot, at int64) (phaseSlot, bool) {
	for _, slot := range slots {
		if at >= slot.start && (slot.end == 0 || at <= slot.end) {
			return slot, true
		}
	}
	return phaseSlot{}, false
}

func buildBatchAccount(batch *Batch, allocs map[allocKey]*alloc) BatchAccount {
	account := BatchAccount{
		BatchID: batch.ID, CampaignRef: batch.Ref, Grade: batch.Grade, StartAt: batch.StartAt, EndAt: batch.EndAt,
		Open:        batch.Open(),
		Consumption: map[string]float64{},
		PhaseUsage:  map[string]map[string]float64{},
		UnitEnergy:  map[string]float64{},
	}

	// revision -> 聚合
	type revAgg struct {
		grade   string
		start   int64
		end     int64
		amounts map[string]float64
		product float64
		unit    string
	}
	revs := map[string]*revAgg{}
	revOrder := []string{}
	for _, phase := range batch.phases {
		a, ok := allocs[allocKey{batch.ID, phase.Phase, phase.Revision}]
		if _, seen := revs[phase.Revision]; !seen {
			revs[phase.Revision] = &revAgg{grade: phase.Grade, start: phase.Start, end: phase.End, amounts: map[string]float64{}}
			revOrder = append(revOrder, phase.Revision)
		}
		agg := revs[phase.Revision]
		if phase.End == 0 || agg.end == 0 || phase.End > agg.end {
			agg.end = phase.End
		}
		if ok {
			usage := map[string]float64{}
			for kind, amount := range a.amounts {
				usage[kind] = amount
				account.Consumption[kind] += amount
			}
			for kind, amount := range usage {
				agg.amounts[kind] += amount
				phaseMap := account.PhaseUsage[string(phase.Phase)]
				if phaseMap == nil {
					phaseMap = map[string]float64{}
				}
				phaseMap[kind] += amount
				account.PhaseUsage[string(phase.Phase)] = phaseMap
			}
		}
	}

	for _, production := range batch.products {
		account.Production += production.Amount
		if account.ProductionUnit == "" {
			account.ProductionUnit = production.Unit
		}
		if agg, ok := revs[production.Revision]; ok {
			agg.product += production.Amount
			agg.unit = production.Unit
		}
	}

	for _, revision := range revOrder {
		agg := revs[revision]
		revAccount := RevisionAccount{
			Revision: revision, Grade: agg.grade, StartAt: agg.start, EndAt: agg.end,
			Consumption: agg.amounts, Production: agg.product, ProductionUnit: agg.unit,
			UnitEnergy: map[string]float64{},
		}
		if agg.product > 0 {
			for kind, amount := range agg.amounts {
				revAccount.UnitEnergy[kind] = amount / agg.product
			}
		}
		account.Revisions = append(account.Revisions, revAccount)
	}
	if account.Production > 0 {
		for kind, amount := range account.Consumption {
			account.UnitEnergy[kind] = amount / account.Production
		}
	}
	return account
}

// BatchAccountByID 返回指定批次的核算结果。
func (l *Ledger) BatchAccountByID(id string) (BatchAccount, bool) {
	for _, account := range l.Accounts().Batches {
		if account.BatchID == id {
			return account, true
		}
	}
	return BatchAccount{}, false
}
