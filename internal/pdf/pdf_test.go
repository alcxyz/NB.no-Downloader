package pdf

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestWritePDF(t *testing.T) {
	dir := t.TempDir()
	rgb := filepath.Join(dir, "1.jpg")
	gray := filepath.Join(dir, "2.jpg")
	os.WriteFile(rgb, testJPEG(t, 100, 150, false), 0o644)
	os.WriteFile(gray, testJPEG(t, 200, 100, true), 0o644)

	var buf bytes.Buffer
	err := Write(&buf, "Å lykkes", []Page{
		{Path: rgb, InchesPerPixel: 0.01, OriginalWidth: 1000}, // 10 in wide
		{Path: gray},
	})
	if err != nil {
		t.Fatal(err)
	}
	pdf := buf.String()

	if !strings.HasPrefix(pdf, "%PDF-1.4\n") || !strings.HasSuffix(pdf, "%%EOF\n") {
		t.Fatal("missing PDF header or trailer")
	}
	for _, want := range []string{
		"/Count 2",
		"/MediaBox [0 0 720.00 1080.00]", // 10 in at 72 pt/in, image aspect 2:3
		"/MediaBox [0 0 595.28 297.64]",  // no scale: A4 width
		"/ColorSpace /DeviceRGB",
		"/ColorSpace /DeviceGray",
		"/Title <FEFF00C5",
	} {
		if !strings.Contains(pdf, want) {
			t.Errorf("PDF lacks %q", want)
		}
	}

	// Every xref entry must point at the start of its object.
	m := regexp.MustCompile(`(?s)xref\n0 (\d+)\n0000000000 65535 f \n(.*?)trailer`).FindStringSubmatch(pdf)
	if m == nil {
		t.Fatal("no xref table")
	}
	n, _ := strconv.Atoi(m[1])
	entries := strings.Split(strings.TrimSpace(m[2]), "\n")
	if len(entries) != n-1 {
		t.Fatalf("%d xref entries, header says %d", len(entries), n-1)
	}
	for i, e := range entries {
		off, _ := strconv.Atoi(e[:10])
		if want := fmt.Sprintf("%d 0 obj", i+1); !strings.HasPrefix(pdf[off:], want) {
			t.Errorf("xref %d points at %q", i+1, pdf[off:off+12])
		}
	}
	start := regexp.MustCompile(`startxref\n(\d+)`).FindStringSubmatch(pdf)
	if off, _ := strconv.Atoi(start[1]); !strings.HasPrefix(pdf[off:], "xref") {
		t.Error("startxref does not point at xref")
	}
}

func testJPEG(t *testing.T, w, h int, gray bool) []byte {
	t.Helper()
	var img image.Image
	if gray {
		g := image.NewGray(image.Rect(0, 0, w, h))
		g.SetGray(1, 1, color.Gray{Y: 200})
		img = g
	} else {
		rgba := image.NewRGBA(image.Rect(0, 0, w, h))
		rgba.Set(1, 1, color.RGBA{R: 200, A: 255})
		img = rgba
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
