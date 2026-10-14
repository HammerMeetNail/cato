package covers

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"

	_ "image/gif" // decoders for uploads
	_ "image/png"
)

// MaxUploadBytes bounds an uploaded cover (custom games, avatars).
const MaxUploadBytes = 4 << 20

// ErrBadImage means the upload isn't a JPEG, PNG or GIF image.
var ErrBadImage = errors.New("upload a JPEG, PNG or GIF image")

// Reencode decodes an uploaded image and re-encodes it as a JPEG no larger
// than maxW×maxH (keeping the aspect ratio). Re-encoding drops metadata
// (EXIF location and the like) and anything that isn't pixels.
func Reencode(data []byte, maxW, maxH int) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxUploadBytes {
		return nil, ErrBadImage
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > 40_000_000 {
		return nil, ErrBadImage
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrBadImage
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := min(1, min(float64(maxW)/float64(w), float64(maxH)/float64(h)))
	dw, dh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// Box-filter downscale: each destination pixel averages the source
	// pixels it covers (good quality for shrinking, no dependency).
	for y := 0; y < dh; y++ {
		sy0, sy1 := b.Min.Y+y*h/dh, b.Min.Y+max((y+1)*h/dh, y*h/dh+1)
		for x := 0; x < dw; x++ {
			sx0, sx1 := b.Min.X+x*w/dw, b.Min.X+max((x+1)*w/dw, x*w/dw+1)
			var r, g, bl, a, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			// Flatten transparency onto white.
			al := float64(a) / float64(n) / 0xffff
			mix := func(c uint64) uint8 {
				return uint8((float64(c)/float64(n)/0xffff*al + (1 - al)) * 255)
			}
			dst.Set(x, y, color.RGBA{mix(r), mix(g), mix(bl), 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// SaveUpload stores a re-encoded cover for gameID (custom entries).
func SaveUpload(coverDir string, gameID int64, data []byte) error {
	jpg, err := Reencode(data, 528, 748)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(coverDir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(jpg); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(coverDir, fmt.Sprintf("%d.jpg", gameID)))
}
