package main

import (
	"math"
	"testing"
)

func defaultScale() scale { return newScale(-65, 0) }

func TestScaleEndpoints(t *testing.T) {
	s := defaultScale()
	if got := s.dbAt(0); got != -65 {
		t.Errorf("dbAt(0) = %v, want -65", got)
	}
	if got := s.dbAt(1); got != 0 {
		t.Errorf("dbAt(1) = %v, want 0", got)
	}
}

func TestScaleMonotonicAndInRange(t *testing.T) {
	s := defaultScale()
	prev := math.Inf(-1)
	for i := 0; i <= 1000; i++ {
		db := s.dbAt(float64(i) / 1000)
		if db < prev {
			t.Fatalf("not monotonic at %v: %v after %v", float64(i)/1000, db, prev)
		}
		if db < -65 || db > 0 {
			t.Fatalf("dbAt(%v) = %v, out of range", float64(i)/1000, db)
		}
		prev = db
	}
}

func TestScaleRoundTrip(t *testing.T) {
	s := defaultScale()
	for i := 0; i <= 1000; i++ {
		pos := float64(i) / 1000
		if got := s.positionFor(s.dbAt(pos)); math.Abs(got-pos) > 1e-9 {
			t.Fatalf("round trip %v -> %v dB -> %v", pos, s.dbAt(pos), got)
		}
	}
}

// the whole reason for clipping: steps must be uniform, with no cliffs.
func TestScaleStepsAreUniform(t *testing.T) {
	s := defaultScale()
	const step = 1.0 / 16
	want := s.dbPerKeyPress()
	for pos := 0.0; pos+step <= 1.0+1e-9; pos += step {
		got := s.dbAt(pos+step) - s.dbAt(pos)
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("step at %v is %v dB, want uniform %v", pos, got, want)
		}
	}
}

// clipping the bottom must actually improve on the device's full range.
func TestScaleClippingImprovesResolution(t *testing.T) {
	clipped := newScale(-65, 0).dbPerKeyPress()
	full := newScale(-99, 0).dbPerKeyPress()
	if clipped >= full {
		t.Errorf("clipped %v dB/press is no better than full range %v", clipped, full)
	}
}

func TestScaleDegenerate(t *testing.T) {
	for _, s := range []scale{newScale(0, -65), newScale(-40, -40), newScale(0, 0)} {
		for i := 0; i <= 100; i++ {
			db := s.dbAt(float64(i) / 100)
			if math.IsNaN(db) {
				t.Fatalf("%+v: dbAt produced NaN", s)
			}
			if got := s.positionFor(db); math.IsNaN(got) {
				t.Fatalf("%+v: scalarFor produced NaN", s)
			}
		}
	}
}
