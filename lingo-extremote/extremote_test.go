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
