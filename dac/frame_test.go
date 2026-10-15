package dac

import "testing"

// the published check value for crc16/modbus, so the variant is pinned
// independently of anything the device tells us.
func TestCRC16ModbusCheckValue(t *testing.T) {
	if got := crc16([]byte("123456789")); got != 0x4b37 {
		t.Fatalf("crc16 = 0x%04x, want 0x4b37", got)
	}
}

func TestEncodeShape(t *testing.T) {
	b, err := frame{protocolType: writeNack, totalFrameLen: 1, cmd: cmdVolume, data: 40}.encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != frameLen {
		t.Fatalf("len = %d, want %d", len(b), frameLen)
	}
	want := []byte{0x00, 0x22, 0x33, 0x20, 0x01, 0x00, 0x71, 0x02, 0, 0, 0, 0x28}
	for i, w := range want {
		if b[i] != w {
			t.Errorf("byte %d = 0x%02x, want 0x%02x", i, b[i], w)
		}
	}
	if b[14] != footer0 || b[15] != footer1 {
		t.Errorf("footer = %02x %02x", b[14], b[15])
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := frame{protocolType: writeNack, totalFrameLen: 1, curFrame: 0, cmd: cmdVolume, data: 40}
	b, err := in.encode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decode(b)
	if !ok {
		t.Fatal("decode failed")
	}
	if got != in {
		t.Errorf("round trip got %+v, want %+v", got, in)
	}
}

// the device also emits frames with the leading placeholder byte stripped.
func TestDecodeStrippedFraming(t *testing.T) {
	b, _ := frame{protocolType: rAck, totalFrameLen: 1, cmd: cmdVolume, data: 40}.encode()
	got, ok := decode(b[1:])
	if !ok {
		t.Fatal("decode of stripped frame failed")
	}
	if got.cmd != cmdVolume || got.data != 40 {
		t.Errorf("got %+v", got)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		make([]byte, 16), // all zeros, as the device emits when idle
		{0x22, 0x33},     // too short
		append([]byte{0x22, 0x33}, make([]byte, 20)...), // magic but no footer
	} {
		if _, ok := decode(b); ok {
			t.Errorf("decoded garbage %x", b)
		}
	}
}

func TestForbiddenCommandsRefused(t *testing.T) {
	for cmd := range forbidden {
		if _, err := (frame{protocolType: writeNack, cmd: cmd}).encode(); err == nil {
			t.Errorf("cmd 0x%04x was encoded, expected refusal", cmd)
		}
	}
}
