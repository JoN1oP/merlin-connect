package media

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// CoverSize is the side of the square JPEG the box displays.
const CoverSize = 128

// NormalizeCover decodes a JPEG, PNG, GIF, BMP or WebP image, crops it to a
// centered square and returns it as a 128x128 JPEG.
func NormalizeCover(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("media: cannot read image: %w", err)
	}
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	crop := image.Rect(0, 0, side, side).Add(image.Pt(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2))
	dst := image.NewRGBA(image.Rect(0, 0, CoverSize, CoverSize))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
	return encodeJPEG(dst)
}

// placeholderColors are soft tones that read well behind the box's UI.
var placeholderColors = []color.RGBA{
	{0xE8, 0xB4, 0x9A, 0xFF}, {0x9A, 0xC4, 0xE8, 0xFF}, {0xA8, 0xD8, 0xB0, 0xFF},
	{0xE8, 0xD2, 0x8E, 0xFF}, {0xC4, 0xA8, 0xE0, 0xFF}, {0xE8, 0xA0, 0xB8, 0xFF},
}

// Placeholder returns a plain 128x128 JPEG whose color is derived from seed, so
// the same title always gets the same color.
func Placeholder(seed string) []byte {
	h := fnv.New32a()
	h.Write([]byte(seed))
	c := placeholderColors[h.Sum32()%uint32(len(placeholderColors))]
	img := image.NewRGBA(image.Rect(0, 0, CoverSize, CoverSize))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	data, err := encodeJPEG(img)
	if err != nil {
		panic(err) // encoding an in-memory RGBA image cannot fail
	}
	return data
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
