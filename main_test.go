package main

import "testing"

func TestSameVolume(t *testing.T) {
	// the device quantises to 0.5 dB, so these are distinct settings
	if sameVolume(-40, -40.5) {
		t.Error("half a dB apart should be different settings")
	}
	if !sameVolume(-40, -40) {
		t.Error("identical values should be the same setting")
	}
	// float noise from the dB round trip must not read as a change
	if !sameVolume(-40, -40.00001) {
		t.Error("float noise should be the same setting")
	}
}

func TestPositionEcho(t *testing.T) {
	// float32 round-trip noise, as measured against the device
	if !positionEcho(0.539849, 0.54) {
		t.Error("float32 round-trip noise should read as an echo")
	}
	if !positionEcho(0.5, 0.5) {
		t.Error("identical values should read as an echo")
	}
	// one macOS volume key press is 1/16
	if positionEcho(0.5+1.0/16, 0.5) {
		t.Error("a key press should not read as an echo")
	}
	// a deliberate small slider drag must still get through
	if positionEcho(0.52, 0.5) {
		t.Error("a 2% slider move should not read as an echo")
	}
}
