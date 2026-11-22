// SPDX-License-Identifier: MIT

// headset-button: on the Corsair VOID WIRELESS v2 button, double press toggles
// dictation and triple press presses Enter.
//
// The headset does its own multi-press detection and sends one event per
// gesture: single press = play/pause (164), double = next track (163), triple =
// previous track (165). Next track runs `omarchy-dictation toggle`; previous
// track is sent on as an Enter key press (to submit what was dictated).
// Everything else (play/pause, volume) passes through unchanged and at once.
//
// The headset's button device is grabbed (EVIOCGRAB), so nothing else sees the
// raw presses; keys are re-sent through a uinput device. When the receiver is
// unplugged it waits and grabs the device again. HEADSET_DEBUG=1 logs every key.
//
// Standard library only. Runs as the user service headset-button.service.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	deviceName = "Corsair CORSAIR VOID WIRELESS v2 Gaming Receiver Consumer Control"

	evSyn         = 0x00
	evKey         = 0x01
	keyNextSong   = 163 // the headset's own double press
	keyPrevSong   = 165 // the headset's triple press
	keyEnter      = 28
	keyMax        = 0x2ff
	eventSize     = 24 // struct input_event on 64-bit: timeval(16) + type(2) + code(2) + value(4)
	uinputNameLen = 80

	eviocgrab    = 0x40044590 // _IOW('E', 0x90, int)
	uiSetEvbit   = 0x40045564 // _IOW('U', 100, int)
	uiSetKeybit  = 0x40045565 // _IOW('U', 101, int)
	uiDevSetup   = 0x405c5503 // _IOW('U', 3, struct uinput_setup) (92 bytes)
	uiDevCreate  = 0x5501     // _IO('U', 1)
	uiDevDestroy = 0x5502     // _IO('U', 2)
	busVirtual   = 0x06
)

// Commands run for the headset's double press. Its triple press becomes Enter.
var actions = map[uint16]struct {
	name string
	argv []string
}{
	keyNextSong: {"double press: dictation toggle", []string{"omarchy-dictation", "toggle"}},
}

func runCmd(name string, argv ...string) {
	log.Print(name)
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		log.Printf("%s: %v", argv[0], err)
		return
	}
	go cmd.Wait()
}

type event struct {
	typ, code uint16
	value     int32
}

func ioctl(fd uintptr, req, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); errno != 0 {
		return errno
	}
	return nil
}

// eviocgbit returns the EVIOCGBIT(ev, len) request number.
func eviocgbit(ev, length uintptr) uintptr {
	return 0x80000000 | length<<16 | 'E'<<8 | (0x20 + ev)
}

// findDevice returns the /dev/input/eventN path whose name matches deviceName.
func findDevice() string {
	paths, _ := filepath.Glob("/sys/class/input/event*/device/name")
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err == nil && strings.TrimSpace(string(b)) == deviceName {
			return "/dev/input/" + filepath.Base(filepath.Dir(filepath.Dir(p)))
		}
	}
	return ""
}

// keyCodes lists the key codes the device can send.
func keyCodes(f *os.File) ([]uint16, error) {
	bits := make([]byte, keyMax/8+1)
	if err := ioctl(f.Fd(), eviocgbit(evKey, uintptr(len(bits))), uintptr(unsafe.Pointer(&bits[0]))); err != nil {
		return nil, err
	}
	var codes []uint16
	for i := 0; i <= keyMax; i++ {
		if bits[i/8]&(1<<(i%8)) != 0 {
			codes = append(codes, uint16(i))
		}
	}
	return codes, nil
}

type uinput struct{ f *os.File }

func newUinput(codes []uint16) (*uinput, error) {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fd := f.Fd()
	if err := ioctl(fd, uiSetEvbit, evKey); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_SET_EVBIT: %w", err)
	}
	for _, c := range codes {
		if err := ioctl(fd, uiSetKeybit, uintptr(c)); err != nil {
			f.Close()
			return nil, fmt.Errorf("UI_SET_KEYBIT %d: %w", c, err)
		}
	}
	// struct uinput_setup { struct input_id id; char name[80]; __u32 ff_effects_max; }
	setup := make([]byte, 8+uinputNameLen+4)
	binary.LittleEndian.PutUint16(setup[0:], busVirtual)
	copy(setup[8:8+uinputNameLen-1], "headset-button (VOID v2)")
	if err := ioctl(fd, uiDevSetup, uintptr(unsafe.Pointer(&setup[0]))); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_DEV_SETUP: %w", err)
	}
	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_DEV_CREATE: %w", err)
	}
	return &uinput{f}, nil
}

func (u *uinput) write(typ, code uint16, value int32) {
	buf := make([]byte, eventSize)
	binary.LittleEndian.PutUint16(buf[16:], typ)
	binary.LittleEndian.PutUint16(buf[18:], code)
	binary.LittleEndian.PutUint32(buf[20:], uint32(value))
	if _, err := u.f.Write(buf); err != nil {
		log.Printf("uinput write: %v", err)
	}
}

func (u *uinput) key(code uint16, value int32) {
	u.write(evKey, code, value)
	u.write(evSyn, 0, 0)
}

func (u *uinput) close() {
	ioctl(u.f.Fd(), uiDevDestroy, 0)
	u.f.Close()
}

// readEvents sends the device's events to ch until the device goes away.
func readEvents(f *os.File, ch chan<- event, done chan<- error) {
	buf := make([]byte, eventSize*64)
	for {
		n, err := f.Read(buf)
		if err != nil {
			done <- err
			return
		}
		for i := 0; i+eventSize <= n; i += eventSize {
			ch <- event{
				typ:   binary.LittleEndian.Uint16(buf[i+16:]),
				code:  binary.LittleEndian.Uint16(buf[i+18:]),
				value: int32(binary.LittleEndian.Uint32(buf[i+20:])),
			}
		}
	}
}

func run(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	codes, err := keyCodes(f)
	if err != nil {
		return fmt.Errorf("EVIOCGBIT: %w", err)
	}
	ui, err := newUinput(append(codes, keyEnter))
	if err != nil {
		return err
	}
	defer ui.close()
	if err := ioctl(f.Fd(), eviocgrab, 1); err != nil {
		return fmt.Errorf("EVIOCGRAB: %w", err)
	}
	log.Printf("grabbed %s", path)

	events := make(chan event, 64)
	done := make(chan error, 1)
	go readEvents(f, events, done)

	for {
		select {
		case err := <-done:
			return err
		case ev := <-events:
			if ev.typ != evKey {
				continue
			}
			if os.Getenv("HEADSET_DEBUG") != "" {
				log.Printf("key code=%d value=%d", ev.code, ev.value)
			}
			if ev.code == keyPrevSong {
				if ev.value == 1 {
					log.Print("triple press: Enter")
					ui.key(keyEnter, 1)
					ui.key(keyEnter, 0)
				}
				continue
			}
			act, ok := actions[ev.code]
			if !ok {
				ui.key(ev.code, ev.value)
				continue
			}
			if ev.value == 1 {
				runCmd(act.name, act.argv...)
			}
		}
	}
}

func main() {
	log.SetFlags(0)
	for {
		path := findDevice()
		if path == "" {
			time.Sleep(3 * time.Second)
			continue
		}
		if err := run(path); err != nil && !errors.Is(err, os.ErrClosed) {
			log.Printf("device lost: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}
