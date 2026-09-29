package energy

import (
	"strings"

	"github.com/wyw14/solo-039/internal/model"
)

// MeterKind 标识公用工程种类。
const (
	MeterSteam        = "steam"
	MeterCoolingWater = "cooling_water"
)

// 表计换段原因。
const (
	SegmentInstall  = "install"     // 新表投用
	SegmentReplaced = "replaced"    // 换表：序列号变化
	SegmentRollover = "rolled_over" // 回绕：读数超过表量程后归零
)

// Reading 是带单位和时间的一只公用工程表计读数（累计值）。
type Reading struct {
	Value  float64
	At     int64
	Unit   string
	Serial string
	Note   string
}

// Segment 是一只表计读数轴上连续单调的一段。
// 换表（序列号变化）或回绕（读数归零重走）都会强制另起一段，
// 两段的用量从不跨段相减。
type Segment struct {
	Kind       string
	Serial     string
	StartAt    int64
	StartValue float64
	EndAt      int64
	EndValue   float64
	// MaxReading>0 时该段是回绕后的新段，跨段区间按量程补算。
	MaxReading float64
	Reason     string
	readings   []Reading
}

// Usage 返回该段自身观测到的用量（同段末值减首值，回绕段不含跨段补算）。
func (s *Segment) Usage() float64 {
	if len(s.readings) == 0 {
		return 0
	}
	return s.readings[len(s.readings)-1].Value - s.readings[0].Value
}

func (s *Segment) ReadingCount() int { return len(s.readings) }

// Meter 是一只公用工程表计（蒸汽或冷却水），由现场编号唯一标识。
type Meter struct {
	ID           string
	Kind         string
	Unit         string
	CurrentSN    string
	Capacity     float64 // 回绕量程，0 表示不回绕
	Segments     []*Segment
	Registration Registration
}

// Registration 保存表计登记信息，换表时新表的起始读数可作为新段锚点。
type Registration struct {
	Serial       string
	InitialValue float64
	InitialAt    int64
	RegisteredAt int64
}

func (m *Meter) currentSegment() *Segment {
	if len(m.Segments) == 0 {
		return nil
	}
	return m.Segments[len(m.Segments)-1]
}

// BatchPhase 是批次时间轴上的一个阶段切片；牌号切换时 Revision 变化。
type BatchPhase struct {
	Phase    model.Phase
	Revision string
	Grade    string
	Start    int64
	End      int64 // 0 表示仍在进行
}

func (p BatchPhase) Open() bool { return p.End == 0 }

// Batch 是一个生产批次及其阶段时间轴。
type Batch struct {
	ID       string // 能源账运行号，每次开批唯一
	Ref      string // 关联的生产批次（campaign ID）
	Grade    string
	StartAt  int64
	EndAt    int64 // 0 表示进行中
	phases   []BatchPhase
	products []Production
}

func (b *Batch) Open() bool { return b.EndAt == 0 }

// Production 是登记给批次（某牌号段）的产品产量，产量为增量口径。
type Production struct {
	Revision string
	Target   string
	Amount   float64
	Unit     string
	At       int64
}

func normalizeKind(kind string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case MeterSteam, "steam_kg", "steam_t", "steam_ton", "steam_tonnes":
		return MeterSteam, true
	case MeterCoolingWater, "cooling-water", "coolingwater", "water", "cw", "cooling":
		return MeterCoolingWater, true
	default:
		return "", false
	}
}
