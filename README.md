# toppingctl

Control a TOPPING DX5 II from the command line on macOS, over USB HID.

## Why

The DX5 II advertises a USB Audio Class volume control and then ignores it. macOS
sends volume changes, the device stores the value, and the audio stays where it
was:

```
element 1: scalar=0.30 dB=-57.4 dBrange=-127.0..0.0 (err 0)   # accepted, and ignored
```

So the volume keys and the menu-bar slider do nothing, while the knob and remote
work fine. TOPPING's own web app at `home.toppingaudio.com` *can* set the volume,
because it doesn't use the audio-class control at all. It talks to a private HID
control channel over the same USB cable via WebHID. This tool speaks that channel.

## Structure

```
main.go            func main, flag parsing, dispatch, and the sync loop
scale.go           type scale (slider position <-> dB)
dac/               presents the DAC; speaks dB, hides the protocol
  dac.go             Open, command ids, dB<->wire, telemetry filter
  device.go          type Device and its methods
  frame.go           type frame, encode/decode, crc
sysvolume/         presents macOS's volume for one output device
  sysvolume.go       the whole CoreAudio bridge
```

Dependencies run one way. `main` uses both packages; neither knows the other
exists, and **all the coupling between the two sides lives in the sync loop**,
which is the service.

```
main.go --> dac         open, read/set volume in dB, receive change events
main.go --> sysvolume   find device, read/set slider position, receive pokes
main.go --> scale       slider position <-> dB
```

`dac` exposes `Open`, `Close`, `Volume`, `SetVolume`, `SetMuted`, `Changes`,
`MuteChanges` and `Settings`. `sysvolume` exposes `Find`, `Close`, `Position`,
`SetPosition`, `Muted`, `SetMuted` and `Changes`. Everything else is
unexported: the frames, the crc, the wire encoding, the 65 command ids, the
guard against the reflash commands, all the cgo, and the per-channel element
numbers.

`sysvolume` is one file because cgo gives each file its own preamble and C types
do not cross file boundaries within a package, so the `Device` holding an
`AudioObjectID` has to sit with the bridge.

## Protocol

The device exposes two HID interfaces on `152a:8750`:

| Usage page | Purpose |
|---|---|
| `0x0C` (Consumer) | input only; the knob sends play/pause and track keys to the host |
| `0x01` (Generic Desktop), usage `0x00` | TOPPING's control channel, 16-byte reports in and out |

A third collection carries usage page `0xFF99`, the standard XMOS/Thesycon DFU
interface. Opening that is how you brick a DAC. This tool never touches it.

### Frame format

16 bytes, big-endian:

```
offset  value
0       0x00          report-id placeholder
1       0x22          magic
2       0x33          magic
3       protocol type 0x10 readNack, 0x11 readAck, 0x1F rAck,
                      0x20 writeNack, 0x21 writeAck, 0x2F wAck
4       totalFrameLen
5       curFrame
6-7     command (uint16)
8-11    data (uint32)
12-13   CRC-16/MODBUS over bytes [3:12], init 0xFFFF, poly 0xA001
14      0x66          footer
15      0x77          footer
```

Replies arrive in the same shape, sometimes with the leading placeholder byte
already stripped, so decoders should accept magic at either `[0:2]` or `[1:3]`.

### Volume encoding

Volume is attenuation in **half-decibel steps**, as a wire value of `0..198`:

```
wire = round(-2 * clamp(dB, -99, 0))
dB   = -wire / 2
```

So `0` is 0 dB (loudest) and `198` is −99 dB (quietest). The device rejects
anything above 198, returning the value sign-extended as a negative int32.

Two independent measurements pin this down: the device accepts 198 and rejects
199, and wire 80 displays as `-40.00 dB` on its own screen.

Beware the web client's `dx5iiVolumeDbToRegister`, which computes
`round(198 + 2*dB)`. That is **not** this mapping. It lives in the
display-renderer chunk and positions the on-screen dial, which runs the opposite
way. Using it as the protocol encoding inverts the scale.

### Reading the current volume

A direct read of `0x7102` only ever acks with `data=0`, so it cannot be used to
query state. Instead, `getSettings` (`0x710C`) returns a 52-frame snapshot where
`curFrame` is the field index, and **field 9 is the volume**. The device also
pushes a `0x7102` frame unprompted whenever the knob or remote changes volume,
so state can be tracked by event rather than polled.

The device continuously streams `0x7130` (VU meter) and `0x7131` (FFT) display
telemetry at roughly 125 Hz. Decoders should discard those or they drown
everything else.

### Commands (DX5 II)

```
0x7101 power on          0x7102 volume            0x7103 mute
0x7104 input type        0x7105 output type       0x710C get settings
0x710E line mode         0x711E volume step       0x7121 volume memory
0x7134 heartbeat
```

The full table of 65 commands is in `dac/dac.go`. Three are refused at the
encoder because the command space is one byte wide and a typo should not be able
to reach them: factory reset (`0x710B`), disable controller (`0x7110`) and
firmware update (`0x712E`).

## Usage

```
toppingctl sync            # drive the dac from the volume keys, and the slider from the knob
toppingctl get             # current volume
toppingctl set -40         # set volume in dB
toppingctl mute on|off     # mute the dac
toppingctl watch           # print changes as the knob and remote make them
toppingctl settings        # dump the getSettings snapshot
```

Flags:

| flag | default | meaning |
|---|---|---|
| `-v` | off | log every change |
| `-min` | `-65` | dB at the bottom of the slider |
| `-max` | `0` | dB at the top of the slider |

Only one process can hold the HID interface at a time, so the one-shot commands
fail with `failed to open device` while `sync` is running.

### Run at login

```
go install
sed -e "s|REPLACE_WITH_BINARY_PATH|$(go env GOPATH)/bin/toppingctl|" \
    -e "s|REPLACE_WITH_LOG_PATH|$HOME/Library/Logs/toppingctl.log|" \
    com.kungfusheep.toppingctl.plist > ~/Library/LaunchAgents/com.kungfusheep.toppingctl.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.kungfusheep.toppingctl.plist
```

## How the sync works

macOS owns the slider and the volume keys. This drives the DAC from them.

- **macOS to device**: a CoreAudio property listener on the per-channel volume
  fires when you press a volume key; the slider position maps to dB and goes out
  over HID.
- **device to macOS**: the DAC pushes a `0x7102` frame when the knob or remote
  moves; that dB maps back to a slider position and is written into CoreAudio.
- **at startup** the hardware wins: its volume is read from the settings snapshot
  and macOS is set to match.
- **feedback loops** end at a comparison against the last known value. An echo
  carries a value we already hold, so it goes no further.

### The volume range

The device can attenuate by 99 dB, but macOS moves the slider in 1/16 steps per
volume key press. Spread over the full range that is about 6 dB per press. The
bottom 30 dB is inaudible, so a third of the slider covers levels nobody uses.

Clipping the range beats warping the curve. `-min` and `-max` define the dB the
slider spans, mapped linearly, so every key press is the same size:

```
dB per key press = (max - min) / 16
```

| range | dB per press |
|---|---|
| `-99 .. 0` (device full range) | 6.2 |
| `-65 .. 0` (default) | 4.1 |
| `-70 .. -25` | 2.8 |
| `-60 .. -30` | 1.9 |

Narrow the range for finer control; lower `-max` if the top of the slider is
louder than you ever want.

An earlier version used a three-segment curve with a fine band around the
listening level. It gave 2 dB per press in the band and 15 dB at the extremes,
and the slope changes at the band edges were audible as cliffs. Uniform steps
across a clipped range read better in use.

### Known limitation: mute

macOS cannot originate a mute for this device. There is no master element, so
there is no master `Mute` for the mute key to write, and `osascript` reports
`output muted: missing value`. Muting from the remote reaches macOS; the other
direction is not observable.

## Status

Reverse-engineered from the TOPPING Home WEB client, verified against a DX5 II on
firmware V2.58. Unofficial and unaffiliated. A firmware update could change the
command IDs.

## License

MIT
