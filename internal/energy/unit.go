package energy

import (
	"errors"
	"math"
	"strings"
)

// Dimension groups units that can be freely converted into one another.
type Dimension string

const (
	Mass   Dimension = "mass"
	Volume Dimension = "volume"
	Energy Dimension = "energy"
)

// Utility identifies what a meter is measuring. Steam is normally metered by
// mass (t) and cooling water by volume (m3), but energy-basis meters are
// accepted as well.
type Utility string

const (
	Steam        Utility = "steam"
	CoolingWater Utility = "cooling-water"
)

type unitDef struct {
	dim    Dimension
	factor float64 // multiply a value expressed in this unit to get base units
}

// Base units: mass=kg, volume=L, energy=kJ.
var units = map[string]unitDef{
	"kg":  {Mass, 1},
	"t":   {Mass, 1000},
	"ton": {Mass, 1000},
	"lb":  {Mass, 0.45359237},

	"l":     {Volume, 1},
	"litre": {Volume, 1},
	"liter": {Volume, 1},
	"m3":    {Volume, 1000},
	"m³":    {Volume, 1000},
	"gal":   {Volume, 3.785411784},

	"kj":  {Energy, 1},
	"mj":  {Energy, 1000},
	"gj":  {Energy, 1_000_000},
	"kwh": {Energy, 3_600_000},
}

func canonicalUnit(unit string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(unit))
	if _, ok := units[key]; !ok {
		return "", errors.New("unknown unit: " + unit)
	}
	return key, nil
}

// toBase converts a reading in the given unit to the dimension base unit.
func toBase(value float64, unit string) (float64, Dimension, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, "", errors.New("meter value is not numeric")
	}
	key, err := canonicalUnit(unit)
	if err != nil {
		return 0, "", err
	}
	def := units[key]
	return value * def.factor, def.dim, nil
}

// fromBase converts a base-unit amount back into the requested display unit.
func fromBase(base float64, unit string) (float64, error) {
	key, err := canonicalUnit(unit)
	if err != nil {
		return 0, err
	}
	return base / units[key].factor, nil
}

func unitDimension(unit string) (Dimension, error) {
	key, err := canonicalUnit(unit)
	if err != nil {
		return "", err
	}
	return units[key].dim, nil
}
