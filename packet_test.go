package ipod_test

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	"github.com/oandrew/ipod"
	_ "github.com/oandrew/ipod/lingo-general"
)

func TestPacketWriter_WritePacket(t *testing.T) {
	largeData := bytes.Repeat([]byte{0xee}, 255)

	tests := []struct {
		name    string
		data    []byte
		want    []byte
		wantErr bool
	}{
		{"no-data", []byte{}, nil, true},
		{"with-data", []byte{0x01, 0x02, 0xfd}, []byte{0x55, 0x03, 0x01, 0x02, 0xfd, 0xfd}, false},
		{"large-with-data", append([]byte{0x1, 0x02}, largeData...), append([]byte{0x55, 0x00, 0x01, 0x01, 0x1, 0x02}, append(largeData, 0xe9)...), false},
		// 255 bytes is the most the small format's length byte holds
		{"small-255", largeData, append([]byte{0x55, 0xff}, append(largeData, 0xef)...), false},
		// 256 bytes: the large format (a small length byte would be 0x00, the large-packet marker)
		{"large-256", append([]byte{0x01}, largeData...), append([]byte{0x55, 0x00, 0x01, 0x00, 0x01}, append(largeData, 0xec)...), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := ipod.NewPacketWriter()
			err := w.WritePacket(tt.data)
			got := w.Bytes()
			if (err != nil) != tt.wantErr {
				t.Errorf("PacketWriter.WritePacket() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("PacketWriter.WritePacket() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPacketReader_ReadPacket(t *testing.T) {
	largeData := bytes.Repeat([]byte{0xee}, 255)

	tests := []struct {
		name    string
		data    []byte
		want    []byte
		wantErr bool
	}{
		{"no-data", []byte{0x55, 0x02, 0x01, 0x02, 256 - 0x05}, []byte{0x01, 0x02}, false},
		{"with-data", []byte{0x55, 0x03, 0x01, 0x02, 0xfd, 0xfd}, []byte{0x01, 0x02, 0xfd}, false},
		{"bad-crc", []byte{0x55, 0x03, 0x01, 0x02, 0xfd, 0x22}, nil, true},
		{"wrong-start-byte", []byte{0xff, 0x03, 0x01, 0x02, 0xfd, 0xfd}, nil, true},

		{"large-with-data", append([]byte{0x55, 0x00, 0x01, 0x01, 0x1, 0x02}, append(largeData, 0xe9)...), append([]byte{0x1, 0x02}, largeData...), false},
		{"large-with-data-short", append([]byte{0x55, 0x00, 0x01, 0x02, 0x1, 0x02}, append(largeData, 0xe9)...), nil, true},
		{"large-bad-crc", append([]byte{0x55, 0x00, 0x01, 0x01, 0x1, 0x02}, append(largeData, 0x22)...), nil, true},
		{"short-packet", []byte{0x55}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ipod.NewPacketReader(tt.data)
			got, err := r.ReadPacket()
			if (err != nil) != tt.wantErr {
				t.Errorf("PacketReader.ReadPacket() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.want != nil && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PacketReader.ReadPacket() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPacketRoundtrip(t *testing.T) {
	pw := [][]byte{
		[]byte("packet1"),
		[]byte("_packet2"),
	}

	w := ipod.NewPacketWriter()
	for i := range pw {
		w.WritePacket(pw[i])
	}

	r := ipod.NewPacketReader(w.Bytes())

	for i := range pw {
		pr, err := r.ReadPacket()
		if err != nil {
			t.Error(err)
		}
		if !bytes.Equal(pr, pw[i]) {
			t.Fail()
		}
	}
}

func BenchmarkPacketReader(b *testing.B) {
	frame := []byte{
		0x55, 0x28, 0x0a, 0x03, 0x03, 0xe7, 0x00, 0x00,
		0x1f, 0x40, 0x00, 0x00, 0x2b, 0x11, 0x00, 0x00,
		0x2e, 0xe0, 0x00, 0x00, 0x3e, 0x80, 0x00, 0x00,
		0x56, 0x22, 0x00, 0x00, 0x5d, 0xc0, 0x00, 0x00,
		0x7d, 0x00, 0x00, 0x00, 0xac, 0x44, 0x00, 0x00,
		0xbb, 0x80, 0x3d, 0x00, 0x00, 0x00, 0x00,
	}

	for i := 0; i < b.N; i++ {
		r := ipod.NewPacketReader(frame)
		r.ReadPacket()
	}
}

func BenchmarkPacketWriter(b *testing.B) {
	packet := []byte{
		0x0a, 0x03, 0x03, 0xe7, 0x00, 0x00, 0x1f, 0x40,
		0x00, 0x00, 0x2b, 0x11, 0x00, 0x00, 0x2e, 0xe0,
		0x00, 0x00, 0x3e, 0x80, 0x00, 0x00, 0x56, 0x22,
		0x00, 0x00, 0x5d, 0xc0, 0x00, 0x00, 0x7d, 0x00,
		0x00, 0x00, 0xac, 0x44, 0x00, 0x00, 0xbb, 0x80,
	}
	for i := 0; i < b.N; i++ {
		pw := ipod.NewPacketWriter()
		pw.WritePacket(packet)
	}
}

func packetOf(payload []byte) []byte {
	w := ipod.NewPacketWriter()
	w.WritePacket(payload)
	return append([]byte(nil), w.Bytes()...)
}

// A packet whose bytes stop short of its declared length must not be completed from
// whatever is left in the buffer past the end of the frame.
func TestReadPacketShortNotReadPastEnd(t *testing.T) {
	full := packetOf([]byte{0x00, 0x07}) // RequestiPodName: 55 02 00 07 f7
	buf := make([]byte, len(full), 64)
	copy(buf, full)
	short := buf[:len(full)-1] // the checksum is still in the buffer, just past len
	if p, err := ipod.NewPacketReader(short).ReadPacket(); err != io.ErrUnexpectedEOF {
		t.Fatalf("got % x, %v; want io.ErrUnexpectedEOF", p, err)
	}
}

// A packet that fails its checksum is skipped whole: a start byte inside it is data,
// not the start of another packet.
func TestReadPacketSkipsDamagedPacket(t *testing.T) {
	inner := []byte{0x55, 0x02, 0x00, 0x07, 0xf7} // a valid RequestiPodName, as data
	frame := packetOf(append([]byte{0x00, 0x15, 0x02, 0x00, 0x00, 0x00}, inner...))
	frame[len(frame)-1] ^= 0xff
	r := ipod.NewPacketReader(frame)
	if _, err := r.ReadPacket(); err == nil {
		t.Fatal("damaged packet accepted")
	}
	if p, err := r.ReadPacket(); err != io.EOF {
		t.Fatalf("read % x (%v) from inside the damaged packet", p, err)
	}
}

func TestReadPacketLenientAuth(t *testing.T) {
	// RetDevAuthenticationInfo: trx 1, v2.0, section 0 of 3, certificate data
	info := packetOf(append([]byte{0x00, 0x15, 0x00, 0x01, 0x02, 0x00, 0x00, 0x03}, bytes.Repeat([]byte{0xab}, 200)...))
	// RetDevAuthenticationSignature: trx 5, 128 bytes of signature
	sig := packetOf(append([]byte{0x00, 0x18, 0x00, 0x05}, bytes.Repeat([]byte{0xcd}, 128)...))
	name := packetOf([]byte{0x00, 0x07})

	dropped := func(p []byte, at, n int) []byte { // n bytes lost in the middle, like the Q60
		return append(append([]byte(nil), p[:at]...), p[at+n:]...)
	}
	badSum := func(p []byte) []byte {
		q := append([]byte(nil), p...)
		q[len(q)-2] ^= 0x01
		return q
	}

	tests := []struct {
		name     string
		frame    []byte
		lenient  bool
		wantErr  bool
		wantLen  int // payload length when accepted
		wantGot  int // OnLenient got/want, 0 if it must not be called
		wantWant int
	}{
		{"info intact", info, true, false, 208, 0, 0},
		{"info short, strict", dropped(info, 70, 4), false, true, 0, 0, 0},
		{"info short, lenient", dropped(info, 70, 4), true, false, 208, 207, 211},
		{"info bad checksum, strict", badSum(info), false, true, 0, 0, 0},
		{"info bad checksum, lenient", badSum(info), true, false, 208, 211, 211},
		{"signature short, lenient", dropped(sig, 66, 2), true, false, 132, 133, 135},
		{"other command bad checksum, lenient", badSum(name), true, true, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ipod.NewPacketReader(tt.frame)
			r.LenientAuth = tt.lenient
			var got, want int
			r.OnLenient = func(cmd byte, g, w int) { got, want = g, w }
			p, err := r.ReadPacket()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if len(p) != tt.wantLen {
					t.Fatalf("payload %d bytes, want %d", len(p), tt.wantLen)
				}
				if !bytes.Equal(p[:4], tt.frame[2:6]) {
					t.Fatalf("payload starts % x, want % x", p[:4], tt.frame[2:6])
				}
			}
			if got != tt.wantGot || want != tt.wantWant {
				t.Fatalf("OnLenient(got=%d, want=%d), want (%d, %d)", got, want, tt.wantGot, tt.wantWant)
			}
		})
	}
}

// Every payload size around the small/large boundary reads back as written.
func TestPacketRoundtripSizes(t *testing.T) {
	for n := 250; n <= 260; n++ {
		payload := bytes.Repeat([]byte{0x04}, n)
		got, err := ipod.NewPacketReader(packetOf(payload)).ReadPacket()
		if err != nil || !bytes.Equal(got, payload) {
			t.Errorf("%d bytes: read back %d bytes, err %v", n, len(got), err)
		}
	}
}
