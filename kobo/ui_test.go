package main

import (
	"image"
	"testing"
)

// A tap on the panel must land on the same logical pixel the rotated frame put there.
func TestRotationRoundTrip(t *testing.T) {
	for _, rot := range []string{"cw", "ccw"} {
		src := image.NewGray(image.Rect(0, 0, W, H))
		for _, p := range []image.Point{{0, 0}, {W - 1, 0}, {0, H - 1}, {W - 1, H - 1}, {123, 456}} {
			src.Pix[p.Y*src.Stride+p.X] = 200
			dst := panelFrame(src, rot)
			if dst.Bounds() != image.Rect(0, 0, PW, PH) {
				t.Fatalf("%s: panel frame is %v", rot, dst.Bounds())
			}
			found := false
			for i, v := range dst.Pix {
				if v == 200 {
					q := image.Pt(i%dst.Stride, i/dst.Stride)
					if got := toLogical(q, rot); got != p {
						t.Errorf("%s: logical %v -> panel %v -> logical %v", rot, p, q, got)
					}
					found = true
				}
			}
			if !found {
				t.Errorf("%s: pixel %v lost", rot, p)
			}
			src.Pix[p.Y*src.Stride+p.X] = 0
		}
	}
}
