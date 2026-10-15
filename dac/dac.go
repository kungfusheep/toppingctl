// Package dac controls a TOPPING DX5 II over the private HID channel its web
// client uses. The device advertises a USB Audio Class volume control and then
// ignores it, so this is the only way to move its volume engine.
//
// Callers work in dB. The frame format, the command set and the wire encoding
// stay inside this package.
package dac

import (
	"fmt"
	"math"

	"github.com/bearsh/hid"
)

const (
	vendorID  = 0x152a
	productID = 0x8750
)

// dx5 ii command ids.
const (
	cmdPowerOn     = 0x7101
	cmdVolume      = 0x7102
	cmdMute        = 0x7103
	cmdGetSettings = 0x710c
	cmdVolumeStep  = 0x711e
	cmdVuData      = 0x7130
	cmdFftData     = 0x7131
	cmdHeartbeat   = 0x7134
)

// commands that reconfigure or reflash the device. never emitted — the command
// space is one byte wide and a typo should not be able to reach them.
var forbidden = map[uint16]string{
	0x710b: "factory reset",
	0x7110: "disable controller",
	0x712e: "firmware update",
}

type errRefused struct {
	what string
	cmd  uint16
}

func (e errRefused) Error() string {
	return fmt.Sprintf("refusing to send %s (0x%04x)", e.what, e.cmd)
}

// volume travels as attenuation in half-decibel steps: the wire value is twice
// the number of dB below maximum, so 0 is 0 dB (loudest) and 198 is -99 dB
// (quietest). verified against the hardware twice over: the device rejects
// anything above 198, and wire 80 displays as -40.00 dB on its screen.
//
// note the web client's dx5iiVolumeDbToRegister (round(198 + 2*dB)) is NOT this
// mapping; it lives in the display-renderer chunk and positions the on-screen
// dial, which runs the opposite way.
const (
	DbMin = -99.0 // quietest the device will go
	DbMax = 0.0   // loudest

	wireMax = 198 // 0.5 dB per step over the 99 dB range

	// field index of volume within the 52-frame getSettings snapshot.
	settingsVolumeIndex = 9
)

func dbToWire(db float64) uint32 {
	db = math.Max(DbMin, math.Min(DbMax, db))
	return uint32(math.Round(-2 * db))
}

func wireToDb(w uint32) float64 {
	if w > wireMax {
		w = wireMax
	}
	return -float64(w) / 2
}

// the device streams vu meter and fft display data at roughly 125 Hz; it drowns
// everything else and is of no interest here.
func isTelemetry(cmd uint16) bool {
	return cmd == cmdVuData || cmd == cmdFftData
}

// Open connects to the DAC's control interface.
//
// The device exposes two HID interfaces on the same vid/pid: usage page 12 is
// the consumer page the knob uses to send media keys to the host, and usage
// page 1 is the control channel. A third collection carries the 0xff99 DFU
// interface, which is never opened.
func Open() (*Device, error) {
	if !hid.Supported() {
		return nil, fmt.Errorf("dac: hid not supported in this build")
	}
	for _, info := range hid.Enumerate(vendorID, productID) {
		if info.UsagePage != 1 || info.Usage != 0 {
			continue
		}
		h, err := info.Open()
		if err != nil {
			return nil, fmt.Errorf("dac: opening control interface: %w", err)
		}
		return newDevice(h), nil
	}
	return nil, fmt.Errorf("dac: no control interface found (is the dac connected and powered?)")
}
