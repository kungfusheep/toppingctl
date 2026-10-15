package main

import "math"

// scale maps slider position to dB linearly across a clipped range.
//
// The device can attenuate by 99 dB, but macOS moves the slider in 1/16 steps
// per key press, so spending travel on the bottom of that range is what makes
// the control coarse. Clipping the inaudible part off the bottom buys
// resolution everywhere without the slope changes a warped curve needs — those
// produce cliffs you can feel, which is worse than being uniformly coarse.
type scale struct {
	min, max float64
}

func newScale(min, max float64) scale {
	if max < min {
		min, max = max, min
	}
	if max == min {
		max = min + 1 // keep the mapping invertible
	}
	return scale{min: min, max: max}
}

// dbAt converts a slider position in [0,1] to dB.
func (s scale) dbAt(pos float64) float64 {
	pos = math.Max(0, math.Min(1, pos))
	return s.min + (s.max-s.min)*pos
}

// positionFor is the inverse, so a change made at the knob puts the slider back
// where it belongs.
func (s scale) positionFor(db float64) float64 {
	db = math.Max(s.min, math.Min(s.max, db))
	return (db - s.min) / (s.max - s.min)
}

// dbPerKeyPress reports the step size a single macOS volume key gives, which is
// the number that decides whether the control feels usable.
func (s scale) dbPerKeyPress() float64 {
	return (s.max - s.min) / 16
}
