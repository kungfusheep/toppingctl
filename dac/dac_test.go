package dac

import "testing"

func TestDbToWire(t *testing.T) {
	cases := []struct {
		db   float64
		wire uint32
	}{
		{0, 0},
		{-0.5, 1},
		{-40, 80}, // verified against the device screen: wire 80 reads -40.00 dB
		{-99, 198},
		{-200, 198}, // clamped
		{+10, 0},    // clamped
	}
	for _, c := range cases {
		if got := dbToWire(c.db); got != c.wire {
			t.Errorf("dbToWire(%v) = %d, want %d", c.db, got, c.wire)
		}
	}
}

func TestWireToDb(t *testing.T) {
	cases := []struct {
		wire uint32
		db   float64
	}{
		{0, 0},
		{80, -40}, // the measured pair
		{198, -99},
		{250, -99}, // above the device's accepted range, clamped
	}
	for _, c := range cases {
		if got := wireToDb(c.wire); got != c.db {
			t.Errorf("wireToDb(%d) = %v, want %v", c.wire, got, c.db)
		}
	}
}

// the device rejects anything above 198, so we must never emit more than that.
func TestWireNeverExceedsDeviceRange(t *testing.T) {
	for db := -150.0; db <= 10; db += 0.25 {
		if w := dbToWire(db); w > wireMax {
			t.Fatalf("dbToWire(%v) = %d, exceeds device max %d", db, w, wireMax)
		}
	}
}

func TestWireRoundTrip(t *testing.T) {
	for w := uint32(0); w <= wireMax; w++ {
		if got := dbToWire(wireToDb(w)); got != w {
			t.Fatalf("round trip wire %d -> %v -> %d", w, wireToDb(w), got)
		}
	}
}

// display telemetry arrives at roughly 125 Hz and must never reach a caller.
func TestTelemetryRecognised(t *testing.T) {
	for _, cmd := range []uint16{cmdVuData, cmdFftData} {
		if !isTelemetry(cmd) {
			t.Errorf("0x%04x should be telemetry", cmd)
		}
	}
	for _, cmd := range []uint16{cmdVolume, cmdMute, cmdGetSettings} {
		if isTelemetry(cmd) {
			t.Errorf("0x%04x should not be telemetry", cmd)
		}
	}
}
