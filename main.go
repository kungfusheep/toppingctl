// Command toppingctl keeps macOS and a TOPPING DX5 II in step.
//
// The DAC advertises a USB Audio Class volume control and then ignores it, so
// macOS volume keys appear to do nothing. Rather than adding a second volume
// system, this makes the existing one true: the keys and the slider drive the
// DAC's own volume engine, and the knob moves the slider back.
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kungfusheep/toppingctl/dac"
	"github.com/kungfusheep/toppingctl/sysvolume"
)

// deviceName is the substring that picks the DAC out of macOS's output devices.
const deviceName = "DX5"

func main() {
	minDb := flag.Float64("min", -65, "dB at the bottom of the slider; clipping the inaudible bottom buys resolution")
	maxDb := flag.Float64("max", 0, "dB at the top of the slider; lower it to protect speakers")
	verbose := flag.Bool("v", false, "log every change")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: toppingctl [-min dB] [-max dB] [-v] {sync|get|set <db>|mute on|off|watch|settings}")
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "sync":
		err = runSync(newScale(*minDb, *maxDb), *verbose)
	case "get":
		err = get()
	case "set":
		if len(args) < 2 {
			err = fmt.Errorf("set needs a dB value, e.g. set -40")
			break
		}
		var db float64
		if db, err = strconv.ParseFloat(args[1], 64); err == nil {
			err = set(db)
		}
	case "mute":
		err = mute(len(args) > 1 && args[1] == "on")
	case "watch":
		err = watch()
	case "settings":
		err = settings()
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runSync keeps a session alive, reconnecting when the DAC sleeps or is
// unplugged, and unwinds cleanly on a signal so CoreAudio listeners are
// deregistered rather than left for process teardown to collect.
func runSync(sc scale, verbose bool) error {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		<-sigs
		log.Print("shutting down")
		close(done)
	}()

	for {
		if err := syncSession(sc, verbose, done); err != nil {
			log.Printf("session ended: %v", err)
		}
		select {
		case <-done:
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// syncSession runs until the device goes away.
//
// All the coupling between the two sides lives here: the dac package knows
// nothing of macOS and sysvolume knows nothing of the DAC. State lives in this
// one goroutine, so there are no locks - every change arrives as either a
// macOS poke or a DAC push, and both are handled below.
func syncSession(sc scale, verbose bool, done <-chan struct{}) error {
	device, err := dac.Open()
	if err != nil {
		return err
	}
	defer device.Close()

	system, err := sysvolume.Find(deviceName)
	if err != nil {
		return err
	}
	defer system.Close()

	// drop any poke left over from a previous session, so we don't immediately
	// overwrite the hardware's volume with a stale macOS value
	select {
	case <-system.Changes():
	default:
	}

	// the hardware is the authority at startup: take its volume and make macOS
	// agree, rather than the other way round
	lastDb, err := device.Volume()
	if err != nil {
		return err
	}
	lastPos := sc.positionFor(lastDb)
	if err := system.SetPosition(lastPos); err != nil {
		return err
	}
	log.Printf("synced: device at %.1f dB, macos aligned; slider spans %.0f..%.0f dB at %.1f dB per key press",
		lastDb, sc.min, sc.max, sc.dbPerKeyPress())

	lastMuted, _ := system.Muted()

	for {
		select {
		case <-done:
			return nil

		case db, open := <-device.Changes():
			if !open {
				return fmt.Errorf("device disconnected")
			}
			// an echo of something we just sent carries the volume we already
			// hold, so comparing values kills the loop without timers or
			// suppression windows
			if sameVolume(db, lastDb) {
				continue
			}
			lastDb = db
			lastPos = sc.positionFor(db)
			if verbose {
				log.Printf("device -> macos: %.1f dB", db)
			}
			if err := system.SetPosition(lastPos); err != nil {
				log.Printf("macos write failed: %v", err)
			}

		case muted, open := <-device.MuteChanges():
			if !open {
				return fmt.Errorf("device disconnected")
			}
			if muted == lastMuted {
				continue
			}
			lastMuted = muted
			if verbose {
				log.Printf("device -> macos: muted=%v", muted)
			}
			if err := system.SetMuted(muted); err != nil {
				log.Printf("macos mute write failed: %v", err)
			}

		case <-system.Changes():
			pos, err := system.Position()
			if err != nil {
				return err
			}
			// our own write coming back. without this the ceiling fights the
			// knob: above it the slider pins at the top while the device sits
			// higher, so the two legitimately disagree and comparing volumes
			// cannot tell an echo from a user action
			if positionEcho(pos, lastPos) {
				continue
			}
			lastPos = pos

			if db := sc.dbAt(pos); !sameVolume(db, lastDb) {
				lastDb = db
				if verbose {
					log.Printf("macos -> device: %.1f dB", db)
				}
				if err := device.SetVolume(db); err != nil {
					return fmt.Errorf("volume write failed: %w", err)
				}
			}
		}
	}
}

// sameVolume reports whether two dB figures land on the same device setting.
// The device quantises to 0.5 dB, so anything closer than that is the same
// setting expressed twice.
func sameVolume(a, b float64) bool {
	return math.Abs(a-b) < 0.1
}

// positionEcho reports whether a slider reading is just the value we wrote
// coming back to us. CoreAudio stores positions as float32, so a round trip
// loses a few parts in ten thousand; a real slider or key change is far larger.
func positionEcho(current, lastWritten float64) bool {
	return math.Abs(current-lastWritten) < 1e-3
}

func get() error {
	device, err := dac.Open()
	if err != nil {
		return err
	}
	defer device.Close()

	db, err := device.Volume()
	if err != nil {
		return err
	}
	fmt.Printf("%.1f dB\n", db)
	return nil
}

func set(db float64) error {
	device, err := dac.Open()
	if err != nil {
		return err
	}
	defer device.Close()

	fmt.Printf("setting %.1f dB\n", db)
	return device.SetVolume(db)
}

func mute(on bool) error {
	device, err := dac.Open()
	if err != nil {
		return err
	}
	defer device.Close()

	fmt.Printf("mute %v\n", on)
	return device.SetMuted(on)
}

// watch prints volume and mute changes as the knob and remote make them.
func watch() error {
	device, err := dac.Open()
	if err != nil {
		return err
	}
	defer device.Close()

	fmt.Println("watching for changes, ctrl-c to stop")
	for {
		select {
		case db, open := <-device.Changes():
			if !open {
				return fmt.Errorf("device disconnected")
			}
			fmt.Printf("%s  volume %.1f dB\n", time.Now().Format("15:04:05.000"), db)
		case muted, open := <-device.MuteChanges():
			if !open {
				return fmt.Errorf("device disconnected")
			}
			fmt.Printf("%s  muted %v\n", time.Now().Format("15:04:05.000"), muted)
		}
	}
}

// settings dumps the device's state snapshot, which is how the volume is read
// and the best starting point for decoding the other fields.
func settings() error {
	device, err := dac.Open()
	if err != nil {
		return err
	}
	defer device.Close()

	fields, err := device.Settings()
	if err != nil {
		return err
	}
	for i, v := range fields {
		fmt.Printf("field %-3d %-12d 0x%08x\n", i, v, v)
	}
	return nil
}
