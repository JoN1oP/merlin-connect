package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// pngImage returns a w x h PNG: left half red, right half blue.
func pngImage(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{0xFF, 0, 0, 0xFF}
			if x >= w/2 {
				c = color.RGBA{0, 0, 0xFF, 0xFF}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNormalizeCoverCropsCenterAndResizes(t *testing.T) {
	out, err := NormalizeCover(pngImage(t, 400, 200))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != CoverSize || img.Bounds().Dy() != CoverSize {
		t.Fatalf("size = %v", img.Bounds())
	}
	// The centered 200x200 crop keeps red on the left and blue on the right.
	r, _, b, _ := img.At(10, 64).RGBA()
	if r < 0xC000 || b > 0x4000 {
		t.Fatalf("left pixel not red: r=%x b=%x", r, b)
	}
	r, _, b, _ = img.At(118, 64).RGBA()
	if b < 0xC000 || r > 0x4000 {
		t.Fatalf("right pixel not blue: r=%x b=%x", r, b)
	}
}

func TestNormalizeCoverRejectsGarbage(t *testing.T) {
	if _, err := NormalizeCover([]byte("not an image")); err == nil {
		t.Fatal("want error")
	}
}

func TestPlaceholderIsStableJPEG(t *testing.T) {
	a, b := Placeholder("Le loup"), Placeholder("Le loup")
	if !bytes.Equal(a, b) {
		t.Fatal("placeholder not deterministic")
	}
	img, err := jpeg.Decode(bytes.NewReader(a))
	if err != nil || img.Bounds().Dx() != CoverSize {
		t.Fatalf("placeholder = %v, %v", img.Bounds(), err)
	}
}
