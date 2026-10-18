// Package ipod implements the iPod Accessory protocol (iap)
package ipod

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	PacketStartByte byte = 0x55
)
const (
	rawSmallPacketMinLen = 1 + 1 + 2 // start + len + ids
	rawLargePacketMinLen = 1 + 3 + 2 // start + len + ids
	smallPacketMaxLen    = 255       // the most a small packet's 1-byte length holds; 0x00 marks a large packet
	minPacketBufSize     = 1024
)

type PacketReader struct {
	frame []byte

	// LenientAuth accepts RetDevAuthenticationInfo (0x15) and RetDevAuthenticationSignature
	// (0x18) packets that are short or fail their checksum, padding a short payload with
	// zeros. Some accessories damage these long packets when they split them over HID
	// reports, and an iPod emulator never verifies the certificate or signature they carry:
	// what the handler acts on is all at the start. Every other packet stays strict.
	LenientAuth bool
	// OnLenient, if set, is called for every packet accepted only because of LenientAuth,
	// with the bytes received and the bytes declared, both counting the start byte.
	OnLenient func(cmd byte, got, want int)
}

var errInvalidChecksum = errors.New("invalid checksum")

func NewPacketReader(frame []byte) *PacketReader {
	return &PacketReader{
		frame: frame,
	}
}

func parseHeader(data []byte) (payOff, payLen int, err error) {
	if len(data) < 3 {
		err = io.ErrUnexpectedEOF
		return
	}
	if data[0] == 0x00 {
		payOff = 3
		payLen = int(binary.BigEndian.Uint16(data[1:3]))
	} else {
		payOff = 1
		payLen = int(data[0])
	}
	return
}

// parsePacket parses the packet at the start of data (after its start byte). n is the
// number of bytes the packet takes up, on error too, so that a damaged packet is
// skipped whole rather than searched for start bytes of packets nobody sent.
func parsePacket(data []byte) (n int, payload []byte, err error) {
	payOff, payLen, err := parseHeader(data)
	if err != nil {
		return len(data), nil, err
	}
	pktLen := payOff + payLen + 1
	// before slicing: data[:pktLen] past len(data) doesn't fail while it is within
	// cap(data), it reads stale bytes of the underlying buffer (and panics past cap)
	if len(data) < pktLen {
		return len(data), nil, io.ErrUnexpectedEOF
	}
	pkt := data[:pktLen]
	if Checksum(pkt) != 0x00 {
		return pktLen, nil, errInvalidChecksum
	}

	return pktLen, pkt[payOff : payOff+payLen], nil
}

// lenientAuthPayload returns the payload of a damaged packet that LenientAuth accepts:
// RetDevAuthenticationInfo or RetDevAuthenticationSignature, zero padded to the
// declared length.
func lenientAuthPayload(data []byte) (cmd byte, payload []byte, want int, ok bool) {
	payOff, payLen, err := parseHeader(data)
	if err != nil || len(data) < payOff+2 || data[payOff] != LingoGeneralID {
		return 0, nil, 0, false
	}
	cmd = data[payOff+1]
	if cmd != 0x15 && cmd != 0x18 {
		return 0, nil, 0, false
	}
	payload = make([]byte, payLen)
	end := payOff + payLen
	if end > len(data) {
		end = len(data)
	}
	copy(payload, data[payOff:end])
	return cmd, payload, payOff + payLen + 1, true
}

func (pd *PacketReader) ReadPacket() ([]byte, error) {
	next := bytes.IndexByte(pd.frame, PacketStartByte)
	if next == -1 {
		return nil, io.EOF
	}
	data := pd.frame[next+1:]
	n, payload, err := parsePacket(data)
	pd.frame = data[n:]
	if err != nil && pd.LenientAuth && (err == io.ErrUnexpectedEOF || err == errInvalidChecksum) {
		if cmd, lenient, want, ok := lenientAuthPayload(data); ok {
			if pd.OnLenient != nil {
				pd.OnLenient(cmd, 1+n, 1+want)
			}
			return lenient, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return payload, nil
}

type PacketWriter struct {
	frame []byte
}

func NewPacketWriter() *PacketWriter {
	return &PacketWriter{
		frame: make([]byte, 0, 512),
	}
}

func (pw *PacketWriter) WritePacket(payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("packet encode: empty packet")
	}

	pw.frame = append(pw.frame, PacketStartByte)
	pktStart := len(pw.frame)

	if len(payload) > smallPacketMaxLen {
		var pktLen [3]byte
		binary.BigEndian.PutUint16(pktLen[1:], uint16(len(payload)))
		pw.frame = append(pw.frame, pktLen[:]...)
	} else {
		pw.frame = append(pw.frame, byte(len(payload)))
	}

	pw.frame = append(pw.frame, payload...)
	pw.frame = append(pw.frame, Checksum(pw.frame[pktStart:]))
	return nil
}

func (pw *PacketWriter) Bytes() []byte {
	return pw.frame
}
