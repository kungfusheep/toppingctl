// Package sysvolume reads and writes the volume macOS holds for one output
// device, and reports when something else changes it.
//
// Callers work in slider position: 0 to 1, exactly what the menu-bar slider and
// the volume keys move. CoreAudio, its property selectors and the per-channel
// element numbers stay inside this package.
package sysvolume

/*
#cgo LDFLAGS: -framework CoreAudio -framework CoreFoundation
#include <CoreAudio/CoreAudio.h>
#include <CoreFoundation/CoreFoundation.h>

extern void goSysVolumeChanged(AudioObjectID dev);

// callbacks arrive on a hal-owned thread; the shim does nothing but hand the
// device id to go, which coalesces and returns immediately.
static OSStatus listenerShim(AudioObjectID obj, UInt32 n,
                             const AudioObjectPropertyAddress *addrs, void *ctx) {
	goSysVolumeChanged(obj);
	return noErr;
}

static OSStatus sv_addListener(AudioObjectID dev, AudioObjectPropertySelector sel,
                               AudioObjectPropertyElement el) {
	AudioObjectPropertyAddress a = {sel, kAudioDevicePropertyScopeOutput, el};
	return AudioObjectAddPropertyListener(dev, &a, listenerShim, NULL);
}

static OSStatus sv_removeListener(AudioObjectID dev, AudioObjectPropertySelector sel,
                                  AudioObjectPropertyElement el) {
	AudioObjectPropertyAddress a = {sel, kAudioDevicePropertyScopeOutput, el};
	return AudioObjectRemovePropertyListener(dev, &a, listenerShim, NULL);
}

static OSStatus sv_devices(AudioObjectID *out, UInt32 *count) {
	AudioObjectPropertyAddress a = {kAudioHardwarePropertyDevices,
	                                kAudioObjectPropertyScopeGlobal,
	                                kAudioObjectPropertyElementMain};
	UInt32 size = 0;
	OSStatus err = AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &a, 0, NULL, &size);
	if (err != noErr) return err;
	UInt32 want = size / sizeof(AudioObjectID);
	if (want > *count) want = *count;
	size = want * sizeof(AudioObjectID);
	err = AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, 0, NULL, &size, out);
	*count = size / sizeof(AudioObjectID);
	return err;
}

static OSStatus sv_name(AudioObjectID dev, char *buf, int len) {
	AudioObjectPropertyAddress a = {kAudioObjectPropertyName,
	                                kAudioObjectPropertyScopeGlobal,
	                                kAudioObjectPropertyElementMain};
	CFStringRef s = NULL;
	UInt32 size = sizeof(s);
	OSStatus err = AudioObjectGetPropertyData(dev, &a, 0, NULL, &size, &s);
	if (err != noErr) return err;
	buf[0] = 0;
	if (s != NULL) {
		CFStringGetCString(s, buf, len, kCFStringEncodingUTF8);
		CFRelease(s);
	}
	return noErr;
}

static OSStatus sv_getFloat32(AudioObjectID dev, AudioObjectPropertySelector sel,
                              AudioObjectPropertyElement el, Float32 *out) {
	AudioObjectPropertyAddress a = {sel, kAudioDevicePropertyScopeOutput, el};
	UInt32 size = sizeof(Float32);
	return AudioObjectGetPropertyData(dev, &a, 0, NULL, &size, out);
}

static OSStatus sv_setFloat32(AudioObjectID dev, AudioObjectPropertySelector sel,
                              AudioObjectPropertyElement el, Float32 v) {
	AudioObjectPropertyAddress a = {sel, kAudioDevicePropertyScopeOutput, el};
	return AudioObjectSetPropertyData(dev, &a, 0, NULL, sizeof(Float32), &v);
}

static OSStatus sv_getUInt32(AudioObjectID dev, AudioObjectPropertySelector sel,
                             AudioObjectPropertyElement el, UInt32 *out) {
	AudioObjectPropertyAddress a = {sel, kAudioDevicePropertyScopeOutput, el};
	UInt32 size = sizeof(UInt32);
	return AudioObjectGetPropertyData(dev, &a, 0, NULL, &size, out);
}

static OSStatus sv_setUInt32(AudioObjectID dev, AudioObjectPropertySelector sel,
                             AudioObjectPropertyElement el, UInt32 v) {
	AudioObjectPropertyAddress a = {sel, kAudioDevicePropertyScopeOutput, el};
	return AudioObjectSetPropertyData(dev, &a, 0, NULL, sizeof(UInt32), &v);
}

static int sv_has(AudioObjectID dev, AudioObjectPropertySelector sel,
                  AudioObjectPropertyElement el) {
	AudioObjectPropertyAddress a = {sel, kAudioDevicePropertyScopeOutput, el};
	return AudioObjectHasProperty(dev, &a) ? 1 : 0;
}

static AudioObjectPropertySelector sel_scalar(void) { return kAudioDevicePropertyVolumeScalar; }
static AudioObjectPropertySelector sel_decibels(void) { return kAudioDevicePropertyVolumeDecibels; }
static AudioObjectPropertySelector sel_mute(void) { return kAudioDevicePropertyMute; }
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"
)

// The DX5 II exposes per-channel volume on elements 1 and 2 and no master
// element 0, which is why macOS volume appears to do nothing: the slider and
// the keys write a master that is not there. Driving the channels directly is
// what makes them land.
var elements = []C.AudioObjectPropertyElement{1, 2}

// changes is written from the hal callback thread. It carries no payload:
// callers re-read position when poked, so a dropped notification costs nothing
// as long as one arrives.
//
// There is one channel for the package rather than one per Device, because a
// process has no reason to watch two output devices at once and a shared
// channel keeps the C shim free of per-device state.
var changes = make(chan struct{}, 1)

//export goSysVolumeChanged
func goSysVolumeChanged(dev C.AudioObjectID) {
	select {
	case changes <- struct{}{}:
	default: // a poke is already pending; nothing to add
	}
}

// Device is one macOS output device's volume.
type Device struct {
	id   C.AudioObjectID
	Name string
}

// Find locates an output device whose name contains match and which exposes the
// per-channel volume this package drives.
func Find(match string) (*Device, error) {
	ids := make([]C.AudioObjectID, 64)
	count := C.UInt32(len(ids))
	if err := C.sv_devices(&ids[0], &count); err != C.noErr {
		return nil, fmt.Errorf("sysvolume: listing devices failed (%d)", int(err))
	}

	buf := make([]byte, 256)
	for _, id := range ids[:int(count)] {
		if C.sv_name(id, (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))) != C.noErr {
			continue
		}
		name := C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
		if !strings.Contains(name, match) {
			continue
		}
		if C.sv_has(id, C.sel_scalar(), elements[0]) == 0 {
			continue
		}
		d := &Device{id: id, Name: name}
		if err := d.listen(); err != nil {
			return nil, err
		}
		return d, nil
	}
	return nil, fmt.Errorf("sysvolume: no output device matching %q with volume control", match)
}

// Changes is poked whenever volume or mute changes, including by our own
// writes. Callers that need to tell their own echo apart should compare the
// position they last wrote.
func (d *Device) Changes() <-chan struct{} { return changes }

// Close deregisters the listeners.
func (d *Device) Close() error {
	for _, el := range elements {
		for _, sel := range d.selectors() {
			C.sv_removeListener(d.id, sel, el)
		}
	}
	return nil
}

// Position reads the slider position, 0 to 1.
func (d *Device) Position() (float64, error) {
	var v C.Float32
	if err := C.sv_getFloat32(d.id, C.sel_scalar(), elements[0], &v); err != C.noErr {
		return 0, fmt.Errorf("sysvolume: reading position failed (%d)", int(err))
	}
	return float64(v), nil
}

// SetPosition writes the slider position to every channel, so the slider and
// the keys agree.
func (d *Device) SetPosition(pos float64) error {
	if pos < 0 {
		pos = 0
	} else if pos > 1 {
		pos = 1
	}
	for _, el := range elements {
		if err := C.sv_setFloat32(d.id, C.sel_scalar(), el, C.Float32(pos)); err != C.noErr {
			return fmt.Errorf("sysvolume: writing position to element %d failed (%d)", int(el), int(err))
		}
	}
	return nil
}

func (d *Device) Muted() (bool, error) {
	var v C.UInt32
	if err := C.sv_getUInt32(d.id, C.sel_mute(), elements[0], &v); err != C.noErr {
		return false, fmt.Errorf("sysvolume: reading mute failed (%d)", int(err))
	}
	return v != 0, nil
}

func (d *Device) SetMuted(muted bool) error {
	var v C.UInt32
	if muted {
		v = 1
	}
	for _, el := range elements {
		if err := C.sv_setUInt32(d.id, C.sel_mute(), el, v); err != C.noErr {
			return fmt.Errorf("sysvolume: writing mute to element %d failed (%d)", int(el), int(err))
		}
	}
	return nil
}

func (d *Device) listen() error {
	for _, el := range elements {
		for _, sel := range d.selectors() {
			if err := C.sv_addListener(d.id, sel, el); err != C.noErr {
				return fmt.Errorf("sysvolume: adding listener failed (%d)", int(err))
			}
		}
	}
	return nil
}

func (d *Device) selectors() []C.AudioObjectPropertySelector {
	return []C.AudioObjectPropertySelector{C.sel_scalar(), C.sel_decibels(), C.sel_mute()}
}
