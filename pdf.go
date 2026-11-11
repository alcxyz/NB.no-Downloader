package main

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // register the JPEG decoder for image.DecodeConfig
	"io"
	"os"
	"strconv"
	"unicode/utf16"
)

// a4WidthPt is the page width used when the library reports no physical
// scale for a page.
const a4WidthPt = 595.28

// pdfPage is one JPEG to place on its own page, filling it.
type pdfPage struct {
	Path           string
	InchesPerPixel float64 // physical scale of the original scan, or 0
	OriginalWidth  int     // original scan width in pixels, for the scale
}

// pdfWriter writes numbered objects and records their byte offsets for the
// cross-reference table.
type pdfWriter struct {
	w       *bufio.Writer
	n       int64
	offsets []int64
}

func (p *pdfWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	return n, err
}

func (p *pdfWriter) printf(format string, args ...any) {
	fmt.Fprintf(p, format, args...)
}

// object starts object number id (1-based, allocated in order).
func (p *pdfWriter) object(id int) {
	for len(p.offsets) < id {
		p.offsets = append(p.offsets, 0)
	}
	p.offsets[id-1] = p.n
	p.printf("%d 0 obj\n", id)
}

// writePDF embeds each JPEG unchanged (DCTDecode), so pages keep the
// quality the server delivered and no image is decoded or re-encoded.
// Images are read one at a time to keep memory bounded.
func writePDF(out io.Writer, title string, pages []pdfPage) error {
	p := &pdfWriter{w: bufio.NewWriter(out)}
	p.printf("%%PDF-1.4\n%%\xe2\xe3\xcf\xd3\n")

	// Objects: 1 catalog, 2 page tree, 3 info, then 3 per page
	// (page, content stream, image).
	const firstPageObj = 4
	kids := make([]byte, 0, len(pages)*8)
	for i := range pages {
		kids = append(kids, strconv.Itoa(firstPageObj+3*i)...)
		kids = append(kids, " 0 R "...)
	}

	p.object(1)
	p.printf("<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	p.object(2)
	p.printf("<< /Type /Pages /Count %d /Kids [ %s] >>\nendobj\n", len(pages), kids)
	p.object(3)
	p.printf("<< /Producer %s", pdfText("NB.no-Downloader"))
	if title != "" {
		p.printf(" /Title %s", pdfText(title))
	}
	p.printf(" >>\nendobj\n")

	for i, page := range pages {
		data, err := os.ReadFile(page.Path)
		if err != nil {
			return err
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%s: %w", page.Path, err)
		}
		if format != "jpeg" {
			return fmt.Errorf("%s: expected JPEG, got %s", page.Path, format)
		}
		colorSpace := "/DeviceRGB"
		decode := ""
		switch cfg.ColorModel {
		case color.GrayModel:
			colorSpace = "/DeviceGray"
		case color.CMYKModel:
			// Adobe CMYK JPEGs are stored inverted.
			colorSpace = "/DeviceCMYK"
			decode = " /Decode [1 0 1 0 1 0 1 0]"
		}

		widthPt := a4WidthPt
		if page.InchesPerPixel > 0 && page.OriginalWidth > 0 {
			widthPt = float64(page.OriginalWidth) * page.InchesPerPixel * 72
		}
		heightPt := widthPt * float64(cfg.Height) / float64(cfg.Width)

		pageObj := firstPageObj + 3*i
		content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", widthPt, heightPt)

		p.object(pageObj)
		p.printf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>\nendobj\n",
			widthPt, heightPt, pageObj+2, pageObj+1)
		p.object(pageObj + 1)
		p.printf("<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content)
		p.object(pageObj + 2)
		p.printf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent 8 /Filter /DCTDecode%s /Length %d >>\nstream\n",
			cfg.Width, cfg.Height, colorSpace, decode, len(data))
		p.Write(data)
		p.printf("\nendstream\nendobj\n")
	}

	xref := p.n
	p.printf("xref\n0 %d\n0000000000 65535 f \n", len(p.offsets)+1)
	for _, off := range p.offsets {
		p.printf("%010d 00000 n \n", off)
	}
	p.printf("trailer\n<< /Size %d /Root 1 0 R /Info 3 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(p.offsets)+1, xref)
	return p.w.Flush()
}

// pdfText encodes s as a UTF-16BE hex string, which PDF readers accept for
// any text including Norwegian letters.
func pdfText(s string) string {
	var b bytes.Buffer
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteString(">")
	return b.String()
}
