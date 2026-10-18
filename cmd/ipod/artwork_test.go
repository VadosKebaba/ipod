package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oandrew/ipod"
	extremote "github.com/oandrew/ipod/lingo-extremote"
	general "github.com/oandrew/ipod/lingo-general"
)

// px is the pixel at x, y of a size x size RGB 565 picture.
func px(img *extremote.ArtworkImage, x, y int, be bool) uint16 {
	i := 2 * (y*int(img.Format.ImageWidth) + x)
	if be {
		return uint16(img.Pixels[i])<<8 | uint16(img.Pixels[i+1])
	}
	return uint16(img.Pixels[i+1])<<8 | uint16(img.Pixels[i])
}

func TestArtworkTestPattern(t *testing.T) {
	for _, be := range []bool{false, true} {
		pixel := artworkPixelLE
		if be {
			pixel = artworkPixelBE
		}
		a, err := loadArtwork(artworkTest, pixel)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.formats) != 2 {
			t.Fatalf("formats %+v", a.formats)
		}
		for _, f := range a.formats {
			img := a.images[f.FormatID]
			n := int(f.ImageWidth)
			wantPF := extremote.ArtworkRGB565LE
			if be {
				wantPF = extremote.ArtworkRGB565BE
			}
			if f.PixelFormat != wantPF || len(img.Pixels) != 2*n*n || img.RowSize != uint32(2*n) {
				t.Fatalf("%s format %d: %+v, %d bytes", pixel, f.FormatID, f, len(img.Pixels))
			}
			q, e := n/4, 3*n/4
			for _, c := range []struct {
				x, y int
				want uint16
			}{
				{q, q, 0xf800}, {e, q, 0x07e0}, {q, e, 0x001f}, {e, e, 0xffff}, {0, 0, 0x0000}, {n - 1, n - 1, 0},
			} {
				if got := px(img, c.x, c.y, be); got != c.want {
					t.Errorf("%s %dx%d at %d,%d: %04x, want %04x", pixel, n, n, c.x, c.y, got, c.want)
				}
			}
		}
	}
	if a, err := loadArtwork(artworkOff, artworkPixelLE); a != nil || err != nil {
		t.Fatalf("off: %v %v", a, err)
	}
	if _, err := loadArtwork(artworkTest, "rgb"); err == nil {
		t.Fatal("bad pixel format accepted")
	}
	if _, err := loadArtwork("/nonexistent.png", artworkPixelLE); err == nil {
		t.Fatal("missing file accepted")
	}
}

// A wide PNG is cropped to the square in its middle and scaled down by averaging.
func TestArtworkFile(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 600, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 600; x++ {
			c := color.RGBA{0, 0, 0, 255} // the 100-pixel margins, cut off
			if x >= 100 && x < 500 {
				c = color.RGBA{0, 255, 0, 255}
				if (x+y)%2 == 0 {
					c = color.RGBA{255, 0, 255, 255} // a fine checkerboard: averages to grey
				}
				if x < 300 {
					c = color.RGBA{255, 255, 0, 255}
				}
			}
			src.Set(x, y, c)
		}
	}
	path := filepath.Join(t.TempDir(), "cover.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, src); err != nil {
		t.Fatal(err)
	}
	f.Close()

	a, err := loadArtwork(path, artworkPixelLE)
	if err != nil {
		t.Fatal(err)
	}
	img := a.images[1] // 128 x 128
	if got := px(img, 0, 64, false); got != 0xffe0 {
		t.Fatalf("left edge: %04x, want yellow ffe0 (the black margin cropped)", got)
	}
	got := px(img, 127, 64, false)
	r, g, b := got>>11, got>>5&0x3f, got&0x1f
	if r < 12 || r > 19 || g < 25 || g > 38 || b < 12 || b > 19 {
		t.Fatalf("right half: %04x (r %d g %d b %d), want the checkerboard's grey", got, r, g, b)
	}
	if _, err := loadArtwork(filepath.Join(t.TempDir(), "x.png"), artworkPixelLE); err == nil {
		t.Fatal("missing file accepted")
	}
	junk := filepath.Join(t.TempDir(), "junk.jpg")
	os.WriteFile(junk, []byte("not a picture"), 0644)
	if _, err := loadArtwork(junk, artworkPixelLE); err == nil {
		t.Fatal("junk accepted")
	}
}

// End to end: the car asks for the formats, the caps, the times and the data, and gets the
// picture through processFrames as small packets with one transaction.
func TestArtworkServed(t *testing.T) {
	quiet(t)
	art, err := loadArtwork(artworkTest, artworkPixelLE)
	if err != nil {
		t.Fatal(err)
	}
	prevNewPlayer := newPlayer
	newPlayer = func() Player {
		a := newAutoPlayer(NewDevExtRemote(time.Now), nil, textOut{mode: textAsIs, max: 200}, nil)
		a.art = art
		return a
	}
	defer func() { newPlayer = prevNewPlayer }()

	tr := &chanTransport{in: make(chan []byte)}
	served := make(chan struct{})
	go func() {
		processFrames(tr)
		close(served)
	}()
	carSerde := ipod.CommandSerde{}
	var trx uint16
	send := func(payload interface{}) uint16 {
		t.Helper()
		cmd, err := ipod.BuildCommand(payload)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Transaction = ipod.NewTransaction(trx)
		pkt, err := carSerde.MarshalCmd(cmd)
		if err != nil {
			t.Fatal(err)
		}
		pw := ipod.NewPacketWriter()
		pw.WritePacket(pkt)
		tr.in <- pw.Bytes()
		trx++
		return trx - 1
	}
	// replies collects the replies to trx, waiting until n have arrived
	replies := func(trx uint16, n int) []*ipod.Command {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var got []*ipod.Command
			for _, c := range tr.written(t) {
				if c.Transaction != nil && uint16(*c.Transaction) == trx {
					if _, ok := c.Payload.(*extremote.PlayStatusChangeNotification); !ok {
						got = append(got, c)
					}
				}
			}
			if len(got) >= n {
				return got
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d replies to %d, want %d", len(got), trx, n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	replies(send(&general.StartIDPS{}), 1)
	formats := replies(send(&extremote.GetArtworkFormats{}), 1)[0].Payload.(*extremote.RetArtworkFormats)
	if len(formats.Formats) != 2 || formats.Formats[0].ImageWidth != 128 || formats.Formats[1].ImageWidth != 240 {
		t.Fatalf("formats %+v", formats)
	}
	caps := replies(send(&extremote.GetIndexedPlayingTrackInfo{InfoType: extremote.TrackInfoCaps}), 1)[0].
		Payload.(*extremote.ReturnIndexedPlayingTrackInfo).Info.(*extremote.TrackCaps)
	if caps.Caps&extremote.TrackCapsHasArtwork == 0 {
		t.Fatalf("caps %+v", caps)
	}
	times := replies(send(&extremote.GetTrackArtworkTimes{FormatID: 2, ArtworkCount: 1}), 1)[0].Payload.(*extremote.RetTrackArtworkTimes)
	if len(times.Times) != 1 {
		t.Fatalf("times %+v", times)
	}

	want := art.images[2].Pixels // 240 x 240: 115200 bytes
	n := (len(want)-222+239)/240 + 1
	pkts := replies(send(&extremote.GetTrackArtworkData{FormatID: 2}), n)
	var got []byte
	for i, c := range pkts {
		d := c.Payload.(*extremote.RetTrackArtworkData)
		if int(d.PacketIndex) != i {
			t.Fatalf("packet %d has index %d", i, d.PacketIndex)
		}
		got = append(got, d.Data...)
	}
	if len(pkts) != n || !bytes.Equal(got, want) {
		t.Fatalf("%d packets (want %d), %d bytes (want %d)", len(pkts), n, len(got), len(want))
	}
	// every frame a small packet: the length byte is never the large-packet marker
	tr.mu.Lock()
	for _, f := range tr.out {
		if f[1] == 0x00 {
			t.Fatalf("a large packet written (%d bytes)", len(f))
		}
	}
	tr.mu.Unlock()

	close(tr.in)
	<-served
}

func TestArtworkOffByDefault(t *testing.T) {
	a := newAutoPlayer(NewDevExtRemote(time.Now), nil, textOut{mode: textAsIs}, nil)
	if a.ArtworkFormats() != nil || a.TrackArtwork(0, 1) != nil {
		t.Fatal("artwork without --artwork")
	}
	if !bytes.Contains([]byte(playerOpts.String()), []byte("artwork off")) {
		t.Fatalf("%s", playerOpts)
	}
}
