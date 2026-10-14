package covers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func pngOf(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestReencodeShrinksAndStripsToJPEG(t *testing.T) {
	out, err := Reencode(pngOf(1056, 1496), 528, 748)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || format != "jpeg" || cfg.Width != 528 || cfg.Height != 748 {
		t.Fatalf("got %s %dx%d %v", format, cfg.Width, cfg.Height, err)
	}
	// Small images aren't enlarged.
	out, _ = Reencode(pngOf(100, 50), 528, 748)
	cfg, _, _ = image.DecodeConfig(bytes.NewReader(out))
	if cfg.Width != 100 || cfg.Height != 50 {
		t.Errorf("small image resized to %dx%d", cfg.Width, cfg.Height)
	}
	for _, bad := range [][]byte{nil, []byte("<svg onload=alert(1)>"), bytes.Repeat([]byte{0xff}, MaxUploadBytes+1)} {
		if _, err := Reencode(bad, 10, 10); err != ErrBadImage {
			t.Errorf("accepted %d bytes: %v", len(bad), err)
		}
	}
}

func TestSaveUploadAndHiDPIServing(t *testing.T) {
	dir := t.TempDir()
	if err := SaveUpload(dir, -3, pngOf(300, 400)); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "-3.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.Decode(f); err != nil {
		t.Errorf("saved cover is not a JPEG: %v", err)
	}
	f.Close()

	os.WriteFile(filepath.Join(dir, "7.jpg"), []byte("one"), 0o644)
	serve := ServeCover(dir)
	get := func(path string) string {
		rec := httptest.NewRecorder()
		serve(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Body.String()
	}
	if got := get("/covers/7@2x.jpg"); got != "one" {
		t.Errorf("2x falls back to 1x: %q", got)
	}
	os.WriteFile(HiDPIPath(dir, 7), []byte("two"), 0o644)
	if got := get("/covers/7@2x.jpg"); got != "two" {
		t.Errorf("2x served %q", got)
	}
	if got := get("/covers/-3.jpg"); len(got) < 100 {
		t.Errorf("custom cover with a negative ID not served")
	}
	if hiDPIURL("https://images.igdb.com/igdb/image/upload/t_cover_big/co1.jpg") != "https://images.igdb.com/igdb/image/upload/t_cover_big_2x/co1.jpg" || hiDPIURL("/x.jpg") != "" {
		t.Error("hiDPIURL")
	}
}
