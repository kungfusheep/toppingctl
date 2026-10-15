package dac

import "encoding/binary"

// topping's hid control frames are 16 bytes: a leading report-id placeholder,
// magic, a header, the command, a big-endian uint32 payload, crc16 and a footer.
const (
	frameLen = 16

	magic0  = 0x22
	magic1  = 0x33
	footer0 = 0x66
	footer1 = 0x77
)

// protocol types. the ack variants ask the device to confirm; nack variants don't.
const (
	readNack  = 0x10
	readAck   = 0x11
	rAck      = 0x1f
	writeNack = 0x20
	writeAck  = 0x21
	wAck      = 0x2f
)

type frame struct {
	protocolType  byte
	totalFrameLen byte
	curFrame      byte
	cmd           uint16
	data          uint32
}

// crc16/modbus. the standard check value for "123456789" is 0x4b37.
func crc16(data []byte) uint16 {
	crc := uint16(0xffff)
	for _, v := range data {
		crc ^= uint16(v)
		for i := 0; i < 8; i++ {
			if crc&1 == 1 {
				crc = crc>>1 ^ 0xa001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func (f frame) encode() ([]byte, error) {
	if what, bad := forbidden[f.cmd]; bad {
		return nil, errRefused{what: what, cmd: f.cmd}
	}
	b := make([]byte, frameLen)
	b[0] = 0x00
	b[1] = magic0
	b[2] = magic1
	b[3] = f.protocolType
	b[4] = f.totalFrameLen
	b[5] = f.curFrame
	binary.BigEndian.PutUint16(b[6:8], f.cmd)
	binary.BigEndian.PutUint32(b[8:12], f.data)
	binary.BigEndian.PutUint16(b[12:14], crc16(b[3:12]))
	b[14] = footer0
	b[15] = footer1
	return b, nil
}

// decode accepts both framings the device emits: with the leading placeholder
// byte present (magic at [1:3]) or already stripped (magic at [0:2]).
func decode(b []byte) (frame, bool) {
	var off int
	switch {
	case len(b) >= 16 && b[1] == magic0 && b[2] == magic1 && b[14] == footer0 && b[15] == footer1:
		off = 1
	case len(b) >= 15 && b[0] == magic0 && b[1] == magic1 && b[13] == footer0 && b[14] == footer1:
		off = 0
	default:
		return frame{}, false
	}
	return frame{
		protocolType:  b[off+2],
		totalFrameLen: b[off+3],
		curFrame:      b[off+4],
		cmd:           binary.BigEndian.Uint16(b[off+5 : off+7]),
		data:          binary.BigEndian.Uint32(b[off+7 : off+11]),
	}, true
}
