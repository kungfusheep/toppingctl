package dac

import (
	"fmt"
	"sync"
	"time"

	"github.com/bearsh/hid"
)

// Device is an open connection to the DAC.
//
// One reader goroutine owns the HID handle. It routes volume and mute pushes to
// the channels callers watch, and query replies to an internal buffer, so a
// caller asking for the current volume never has to compete with the knob.
type Device struct {
	h *hid.Device

	volumes chan float64
	mutes   chan bool
	replies chan frame

	stop chan struct{} // closed by Close to retire the reader
	once sync.Once

	mu sync.Mutex // serialises query and reply pairs
}

func newDevice(h *hid.Device) *Device {
	d := &Device{
		h:       h,
		volumes: make(chan float64, 8),
		mutes:   make(chan bool, 8),
		replies: make(chan frame, 256),
		stop:    make(chan struct{}),
	}
	go d.read()
	return d
}

// Changes reports volume changes in dB made at the knob or remote. It closes
// when the device goes away, which is how callers notice a disconnect.
func (d *Device) Changes() <-chan float64 { return d.volumes }

// MuteChanges reports mute changes made at the remote.
func (d *Device) MuteChanges() <-chan bool { return d.mutes }

func (d *Device) Close() error {
	d.once.Do(func() { close(d.stop) })
	return d.h.Close()
}

// Volume reads the current volume in dB.
//
// A direct read of the volume command only ever acks with zero, so the only
// reliable source is the settings snapshot.
func (d *Device) Volume() (float64, error) {
	f, err := d.query(settingsVolumeIndex)
	if err != nil {
		return 0, err
	}
	return wireToDb(f.data), nil
}

// SetVolume sets the volume in dB, clamped to what the device accepts.
func (d *Device) SetVolume(db float64) error {
	return d.send(frame{
		protocolType:  writeNack,
		totalFrameLen: 1,
		cmd:           cmdVolume,
		data:          dbToWire(db),
	})
}

func (d *Device) SetMuted(muted bool) error {
	data := uint32(0)
	if muted {
		data = 1
	}
	return d.send(frame{
		protocolType:  writeNack,
		totalFrameLen: 1,
		cmd:           cmdMute,
		data:          data,
	})
}

// Settings returns the device's state snapshot, one uint32 per field, indexed
// by the frame number the device reports. Field 9 is the volume.
func (d *Device) Settings() ([]uint32, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.drain()
	if err := d.send(frame{protocolType: readNack, totalFrameLen: 1, cmd: cmdGetSettings}); err != nil {
		return nil, err
	}

	var fields []uint32
	seen := 0
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f := <-d.replies:
			if f.cmd != cmdGetSettings {
				continue
			}
			if len(fields) < int(f.totalFrameLen) {
				fields = append(fields, make([]uint32, int(f.totalFrameLen)-len(fields))...)
			}
			if int(f.curFrame) < len(fields) {
				fields[f.curFrame] = f.data
				seen++
			}
			if seen >= len(fields) && len(fields) > 0 {
				return fields, nil
			}
		case <-deadline:
			if len(fields) > 0 {
				return fields, nil // partial snapshot beats nothing
			}
			return nil, fmt.Errorf("dac: no settings snapshot")
		case <-d.stop:
			return nil, fmt.Errorf("dac: closed")
		}
	}
}

// query sends a settings request and waits for one field of the snapshot.
func (d *Device) query(field byte) (frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.drain()
	if err := d.send(frame{protocolType: readNack, totalFrameLen: 1, cmd: cmdGetSettings}); err != nil {
		return frame{}, err
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case f := <-d.replies:
			if f.cmd == cmdGetSettings && f.curFrame == field {
				return f, nil
			}
		case <-deadline:
			return frame{}, fmt.Errorf("dac: no reply to settings query")
		case <-d.stop:
			return frame{}, fmt.Errorf("dac: closed")
		}
	}
}

// drain clears stale replies so a query cannot match an earlier snapshot.
func (d *Device) drain() {
	for {
		select {
		case <-d.replies:
		default:
			return
		}
	}
}

// send writes a frame. the device wants the bare 16-byte frame, whose own first
// byte is the report-id placeholder; prefixing another report-id byte the way
// hidapi normally expects gets no reply at all.
func (d *Device) send(f frame) error {
	b, err := f.encode()
	if err != nil {
		return err
	}
	if _, err := d.h.Write(b); err != nil {
		return fmt.Errorf("dac: write: %w", err)
	}
	return nil
}

// read owns the HID handle for the life of the device.
func (d *Device) read() {
	defer close(d.volumes)
	defer close(d.mutes)

	buf := make([]byte, 64)
	for {
		select {
		case <-d.stop:
			return
		default:
		}

		n, err := d.h.ReadTimeout(buf, 200)
		if err != nil {
			return // device gone; closing the channels tells the caller
		}
		if n == 0 {
			continue // timeout, nothing waiting
		}
		f, ok := decode(buf[:n])
		if !ok || isTelemetry(f.cmd) {
			continue
		}

		switch f.cmd {
		case cmdVolume:
			d.offerVolume(wireToDb(f.data))
		case cmdMute:
			d.offerMute(f.data == 1)
		default:
			select {
			case d.replies <- f:
			default: // no query outstanding
			}
		}
	}
}

// offerVolume hands a change to the caller, dropping it if they are not
// keeping up. a stale volume is worth nothing, and blocking the reader would
// stall queries.
func (d *Device) offerVolume(db float64) {
	select {
	case d.volumes <- db:
	case <-d.stop:
	default:
	}
}

func (d *Device) offerMute(muted bool) {
	select {
	case d.mutes <- muted:
	case <-d.stop:
	default:
	}
}
