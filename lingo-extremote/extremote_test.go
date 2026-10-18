package extremote

import (
	"bytes"
	"testing"

	"github.com/oandrew/ipod"
)

func TestPlayStatusChangeNotificationWire(t *testing.T) {
	cmd, err := ipod.BuildCommand(&PlayStatusChangeNotification{
		Status: PlayStatusChangeTrackTimeMs,
		Data:   []byte{0x00, 0x00, 0x03, 0xe8},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Transaction = ipod.NewTransaction(5)
	serde := ipod.CommandSerde{TrxEnabled: true}
	pkt, err := serde.MarshalCmd(cmd)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x04, 0x00, 0x27, 0x00, 0x05, 0x04, 0x00, 0x00, 0x03, 0xe8}
	if !bytes.Equal(pkt, want) {
		t.Fatalf("packet % x, want % x", pkt, want)
	}

	back, err := serde.UnmarshalCmd(pkt)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := back.Payload.(*PlayStatusChangeNotification)
	if !ok || n.Status != PlayStatusChangeTrackTimeMs || !bytes.Equal(n.Data, want[6:]) {
		t.Fatalf("decoded %#v", back.Payload)
	}

	// extended status: 1 byte of data, and no data for stopped
	for _, c := range []struct {
		n    PlayStatusChangeNotification
		want []byte
	}{
		{PlayStatusChangeNotification{Status: PlayStatusChangeExtendedStatus, Data: []byte{byte(ExtendedPlayStatusPaused)}}, []byte{0x06, 0x0b}},
		{PlayStatusChangeNotification{Status: PlayStatusChangeStopped}, []byte{0x00}},
	} {
		got, _ := c.n.MarshalBinary()
		if !bytes.Equal(got, c.want) {
			t.Fatalf("% x, want % x", got, c.want)
		}
	}
}

func TestSetPlayStatusChangeNotificationForms(t *testing.T) {
	serde := ipod.CommandSerde{TrxEnabled: true}
	short, err := serde.UnmarshalCmd([]byte{0x04, 0x00, 0x26, 0x00, 0x07, 0x01})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := short.Payload.(*SetPlayStatusChangeNotificationShort); !ok || !p.Enabled {
		t.Fatalf("1-byte form decoded as %#v", short.Payload)
	}
	long, err := serde.UnmarshalCmd([]byte{0x04, 0x00, 0x26, 0x00, 0x08, 0x00, 0x00, 0x00, 0x1f})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := long.Payload.(*SetPlayStatusChangeNotification); !ok || p.EventMask != 0x1f {
		t.Fatalf("4-byte form decoded as %#v", long.Payload)
	}

	all := PlayStatusChangesFromMask(0x3f)
	for code := PlayStatusChangeStopped; code <= PlayStatusChangeTrackTimeSec; code++ {
		if !all.Has(code) {
			t.Errorf("mask 0x3f does not enable 0x%02x", code)
		}
	}
	if m := PlayStatusChangesFromMask(0x02); m != 1<<PlayStatusChangeExtendedStatus {
		t.Errorf("mask 0x02 (extended) = %#x", m)
	}
	if m := PlayStatusChangesFromMask(0x01); m != 1<<PlayStatusChangeStopped|1<<PlayStatusChangeFFSeekStop|1<<PlayStatusChangeRewSeekStop {
		t.Errorf("mask 0x01 (basic) = %#x", m)
	}
	if PlayStatusChangesShort.Has(PlayStatusChangeExtendedStatus) || PlayStatusChangesShort.Has(PlayStatusChangeTrackTimeSec) ||
		!PlayStatusChangesShort.Has(PlayStatusChangeTrackTimeMs) || !PlayStatusChangesShort.Has(PlayStatusChangeChapterIndex) {
		t.Errorf("1-byte set = %#x", PlayStatusChangesShort)
	}
}

type fakePlayer struct{ names bool }

func (fakePlayer) PlaybackStatus() (uint32, uint32, PlayerState) {
	return 1000, 0, PlayerStatePlaying
}
func (fakePlayer) CurrentTrack() (int32, uint32)                { return 0, 1 }
func (fakePlayer) PlayControl(PlayControlCmd)                   {}
func (fakePlayer) SetCurrentTrack(int32, bool)                  {}
func (fakePlayer) SetPlayStatusNotifications(PlayStatusChanges) {}

type namedPlayer struct{ fakePlayer }

func (namedPlayer) TrackInfo(index int32) (string, string, string) {
	return "Song", "Band", "Record"
}

func TestTrackInfo(t *testing.T) {
	ask := func(dev DeviceExtRemote, payload interface{}) []byte {
		cmd, err := ipod.BuildCommand(payload)
		if err != nil {
			t.Fatal(err)
		}
		out := ipod.CmdBuffer{}
		HandleExtRemote(cmd, &out, dev)
		switch r := out.Commands[0].Payload.(type) {
		case *ReturnIndexedPlayingTrackTitle:
			return r.Title
		case *ReturnIndexedPlayingTrackArtistName:
			return r.ArtistName
		case *ReturnIndexedPlayingTrackAlbumName:
			return r.AlbumName
		}
		t.Fatalf("answer %T", out.Commands[0].Payload)
		return nil
	}
	for _, c := range []struct {
		dev  DeviceExtRemote
		want [3]string
	}{
		{nil, [3]string{"title", "artist", "album"}},
		{fakePlayer{}, [3]string{"title", "artist", "album"}},
		{namedPlayer{}, [3]string{"Song", "Band", "Record"}},
	} {
		got := [3][]byte{
			ask(c.dev, &GetIndexedPlayingTrackTitle{}),
			ask(c.dev, &GetIndexedPlayingTrackArtistName{}),
			ask(c.dev, &GetIndexedPlayingTrackAlbumName{}),
		}
		for i := range got {
			if !bytes.Equal(got[i], ipod.StringToBytes(c.want[i])) {
				t.Errorf("%T: %q, want %q", c.dev, got[i], c.want[i])
			}
		}
	}
}

func TestArtworkWire(t *testing.T) {
	first := RetTrackArtworkData{PacketIndex: 0, PixelFormat: ArtworkRGB565LE, ImageWidth: 128, ImageHeight: 128,
		BottomRightX: 127, BottomRightY: 127, RowSize: 256, Data: []byte{1, 2, 3, 4}}
	b, _ := first.MarshalBinary()
	want := []byte{0, 0, 0x02, 0, 128, 0, 128, 0, 0, 0, 0, 0, 127, 0, 127, 0, 0, 1, 0, 1, 2, 3, 4}
	if !bytes.Equal(b, want) {
		t.Fatalf("packet 0: % x, want % x", b, want)
	}
	var back RetTrackArtworkData
	if err := back.UnmarshalBinary(b); err != nil || back.RowSize != 256 || back.ImageWidth != 128 ||
		back.BottomRightY != 127 || !bytes.Equal(back.Data, first.Data) {
		t.Fatalf("packet 0 back: %+v %v", back, err)
	}
	next := RetTrackArtworkData{PacketIndex: 7, Data: []byte{9, 8}}
	b, _ = next.MarshalBinary()
	if !bytes.Equal(b, []byte{0, 7, 9, 8}) {
		t.Fatalf("packet 7: % x", b)
	}
	back = RetTrackArtworkData{}
	if err := back.UnmarshalBinary(b); err != nil || back.PacketIndex != 7 || !bytes.Equal(back.Data, []byte{9, 8}) {
		t.Fatalf("packet 7 back: %+v %v", back, err)
	}

	times, _ := RetTrackArtworkTimes{Times: []uint32{0, 1000}}.MarshalBinary()
	if !bytes.Equal(times, []byte{0, 0, 0, 0, 0, 0, 3, 0xe8}) {
		t.Fatalf("times % x", times)
	}
	info, _ := ReturnIndexedPlayingTrackInfo{InfoType: TrackInfoArtworkCount,
		Info: []TrackArtworkCount{{1, 1}, {2, 1}}}.MarshalBinary()
	if !bytes.Equal(info, []byte{0x07, 0, 1, 0, 1, 0, 2, 0, 1}) {
		t.Fatalf("artwork count % x", info)
	}
	var infoBack ReturnIndexedPlayingTrackInfo
	if err := infoBack.UnmarshalBinary(info); err != nil {
		t.Fatal(err)
	}
	if c, ok := infoBack.Info.([]TrackArtworkCount); !ok || len(c) != 2 || c[1].FormatID != 2 {
		t.Fatalf("artwork count back %#v", infoBack.Info)
	}
}

// artDev is a DeviceExtRemote with a picture for even track indexes, in format 5.
type artDev struct{ img *ArtworkImage }

func (artDev) PlaybackStatus() (uint32, uint32, PlayerState) { return 1000, 0, PlayerStatePlaying }
func (artDev) CurrentTrack() (int32, uint32)                 { return 0, 4 }
func (artDev) PlayControl(PlayControlCmd)                    {}
func (artDev) SetCurrentTrack(int32, bool)                   {}
func (artDev) SetPlayStatusNotifications(PlayStatusChanges)  {}
func (d artDev) ArtworkFormats() []ArtworkFormat             { return []ArtworkFormat{d.img.Format} }
func (d artDev) TrackArtwork(i int32, f uint16) *ArtworkImage {
	if i%2 == 0 && f == d.img.Format.FormatID {
		return d.img
	}
	return nil
}

func TestHandleArtwork(t *testing.T) {
	const w, h = 30, 20
	pixels := make([]byte, 2*w*h)
	for i := range pixels {
		pixels[i] = byte(i)
	}
	dev := artDev{img: &ArtworkImage{Format: ArtworkFormat{FormatID: 5, PixelFormat: ArtworkRGB565LE,
		ImageWidth: w, ImageHeight: h}, RowSize: 2 * w, Pixels: pixels}}
	handle := func(payload interface{}) []*ipod.Command {
		t.Helper()
		req, err := ipod.BuildCommand(payload)
		if err != nil {
			t.Fatal(err)
		}
		req.Transaction = ipod.NewTransaction(42)
		var out ipod.CmdBuffer
		HandleExtRemote(req, &out, dev)
		serde := ipod.CommandSerde{TrxEnabled: true}
		for _, c := range out.Commands {
			if c.Transaction == nil || *c.Transaction != 42 {
				t.Fatalf("reply without the request's transaction: %#v", c)
			}
			pkt, err := serde.MarshalCmd(c)
			if err != nil {
				t.Fatal(err)
			}
			if len(pkt) > 255 {
				t.Fatalf("%d-byte packet: not the small format", len(pkt))
			}
		}
		return out.Commands
	}

	if r := handle(&GetArtworkFormats{})[0].Payload.(*RetArtworkFormats); len(r.Formats) != 1 || r.Formats[0].FormatID != 5 {
		t.Fatalf("formats %+v", r)
	}
	caps := handle(&GetIndexedPlayingTrackInfo{InfoType: TrackInfoCaps, TrackIndex: 2})[0].Payload.(*ReturnIndexedPlayingTrackInfo)
	if caps.Info.(*TrackCaps).Caps&TrackCapsHasArtwork == 0 {
		t.Fatalf("caps without artwork: %+v", caps.Info)
	}
	caps = handle(&GetIndexedPlayingTrackInfo{InfoType: TrackInfoCaps, TrackIndex: 1})[0].Payload.(*ReturnIndexedPlayingTrackInfo)
	if caps.Info.(*TrackCaps).Caps&TrackCapsHasArtwork != 0 {
		t.Fatalf("caps with artwork for a track without")
	}
	count := handle(&GetIndexedPlayingTrackInfo{InfoType: TrackInfoArtworkCount, TrackIndex: 0})[0].Payload.(*ReturnIndexedPlayingTrackInfo)
	if c := count.Info.([]TrackArtworkCount); len(c) != 1 || c[0] != (TrackArtworkCount{5, 1}) {
		t.Fatalf("count %+v", count.Info)
	}
	if r := handle(&GetTrackArtworkTimes{TrackIndex: 0, FormatID: 5, ArtworkIndex: 0, ArtworkCount: -1})[0].Payload.(*RetTrackArtworkTimes); len(r.Times) != 1 || r.Times[0] != 0 {
		t.Fatalf("times %+v", r)
	}
	if r := handle(&GetTrackArtworkTimes{TrackIndex: 0, FormatID: 9, ArtworkIndex: 0, ArtworkCount: 1})[0].Payload.(*RetTrackArtworkTimes); len(r.Times) != 0 {
		t.Fatalf("times for an unknown format %+v", r)
	}

	pkts := handle(&GetTrackArtworkData{TrackIndex: 0, FormatID: 5})
	var got []byte
	for i, c := range pkts {
		d := c.Payload.(*RetTrackArtworkData)
		if int(d.PacketIndex) != i || len(d.Data)%2 != 0 {
			t.Fatalf("packet %d: index %d, %d bytes", i, d.PacketIndex, len(d.Data))
		}
		if i == 0 && (d.ImageWidth != w || d.ImageHeight != h || d.RowSize != 2*w || d.BottomRightX != w-1 ||
			d.BottomRightY != h-1 || d.PixelFormat != ArtworkRGB565LE) {
			t.Fatalf("header %+v", d)
		}
		got = append(got, d.Data...)
	}
	if !bytes.Equal(got, pixels) || len(pkts) != 6 { // 222 + 4 x 240 + 18 = 1200
		t.Fatalf("%d packets, %d bytes, equal %v", len(pkts), len(got), bytes.Equal(got, pixels))
	}
	if a := handle(&GetTrackArtworkData{TrackIndex: 1, FormatID: 5})[0].Payload.(*ACK); a.Status != ACKStatusFailed {
		t.Fatalf("no picture: %+v", a)
	}
}
