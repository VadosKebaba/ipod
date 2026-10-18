package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"os"

	extremote "github.com/oandrew/ipod/lingo-extremote"
)

// --artwork: album art for the head unit. Step one of Level 4: one picture for every track,
// a test pattern or a file, to see whether and how the car shows it. The phone's own covers
// would come from AVRCP cover art, which BlueZ doesn't offer here yet (no ImgHandle in the
// MediaPlayer1 track).

const (
	artworkOff  = "off"
	artworkTest = "test"

	artworkPixelLE = "565le"
	artworkPixelBE = "565be"
)

// the formats offered to the head unit: square, RGB 565
var artworkSizes = []struct {
	id   uint16
	size int
}{{1, 128}, {2, 240}}

// artworkSet is the picture in every offered format.
type artworkSet struct {
	source  string // test or the file
	pixel   string
	formats []extremote.ArtworkFormat
	images  map[uint16]*extremote.ArtworkImage
}

// loadArtwork: spec is off (nil), test or an image file (PNG or JPEG; cropped to a square
// around its centre); pixel is 565le or 565be.
func loadArtwork(spec, pixel string) (*artworkSet, error) {
	var be bool
	switch pixel {
	case artworkPixelLE:
	case artworkPixelBE:
		be = true
	default:
		return nil, fmt.Errorf("--artwork-pixel=%s: expected %s or %s", pixel, artworkPixelLE, artworkPixelBE)
	}
	var img image.Image
	switch spec {
	case "", artworkOff:
		return nil, nil
	case artworkTest:
		img = artworkTestPattern(240)
	default:
		f, err := os.Open(spec)
		if err != nil {
			return nil, fmt.Errorf("--artwork: %v", err)
		}
		defer f.Close()
		img, _, err = image.Decode(f)
		if err != nil {
			return nil, fmt.Errorf("--artwork=%s: %v", spec, err)
		}
	}
	a := &artworkSet{source: spec, pixel: pixel, images: map[uint16]*extremote.ArtworkImage{}}
	pf := extremote.ArtworkRGB565LE
	if be {
		pf = extremote.ArtworkRGB565BE
	}
	crop := squareCrop(img.Bounds())
	for _, s := range artworkSizes {
		f := extremote.ArtworkFormat{FormatID: s.id, PixelFormat: pf, ImageWidth: uint16(s.size), ImageHeight: uint16(s.size)}
		a.formats = append(a.formats, f)
		a.images[s.id] = &extremote.ArtworkImage{
			Format:  f,
			RowSize: uint32(2 * s.size),
			Pixels:  rgb565(img, crop, s.size, be),
		}
	}
	return a, nil
}

func (a *artworkSet) String() string {
	if a == nil {
		return artworkOff
	}
	s := a.source + " " + a.pixel
	for _, f := range a.formats {
		s += fmt.Sprintf(", format %d %dx%d", f.FormatID, f.ImageWidth, f.ImageHeight)
	}
	return s
}

// artworkTestPattern: red top left, green top right, blue bottom left, white bottom right,
// in a black frame. Wrong colours mean the wrong pixel format (try the other byte order), a
// mirrored or turned pattern a different row order.
func artworkTestPattern(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	frame, half := size/30+1, size/2
	quads := []struct {
		r image.Rectangle
		c color.Color
	}{
		{image.Rect(frame, frame, half, half), color.RGBA{255, 0, 0, 255}},
		{image.Rect(half, frame, size-frame, half), color.RGBA{0, 255, 0, 255}},
		{image.Rect(frame, half, half, size-frame), color.RGBA{0, 0, 255, 255}},
		{image.Rect(half, half, size-frame, size-frame), color.White},
	}
	for _, q := range quads {
		draw.Draw(img, q.r, image.NewUniform(q.c), image.Point{}, draw.Src)
	}
	return img
}

// squareCrop is the largest square in the middle of r.
func squareCrop(r image.Rectangle) image.Rectangle {
	if d := r.Dx() - r.Dy(); d > 0 {
		r.Min.X += d / 2
		r.Max.X = r.Min.X + r.Dy()
	} else if d < 0 {
		r.Min.Y += -d / 2
		r.Max.Y = r.Min.Y + r.Dx()
	}
	return r
}

// rgb565 scales the src part of img to size x size (each pixel the average of the source
// pixels it covers) as RGB 565, 2 bytes a pixel, little or big endian, row by row.
func rgb565(img image.Image, src image.Rectangle, size int, be bool) []byte {
	out := make([]byte, 0, 2*size*size)
	sw, sh := src.Dx(), src.Dy()
	for y := 0; y < size; y++ {
		y0, y1 := src.Min.Y+y*sh/size, src.Min.Y+(y+1)*sh/size
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < size; x++ {
			x0, x1 := src.Min.X+x*sw/size, src.Min.X+(x+1)*sw/size
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					c := color.RGBA64Model.Convert(img.At(xx, yy)).(color.RGBA64)
					r, g, b, n = r+uint64(c.R), g+uint64(c.G), b+uint64(c.B), n+1
				}
			}
			v := uint16(r/n>>11)<<11 | uint16(g/n>>10)<<5 | uint16(b/n>>11)
			if be {
				out = append(out, byte(v>>8), byte(v))
			} else {
				out = append(out, byte(v), byte(v>>8))
			}
		}
	}
	return out
}
