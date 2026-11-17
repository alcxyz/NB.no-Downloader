package ocr

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// ParseALTO reads nb.no's ALTO OCR files into the shared page model. It
// walks the XML as a token stream because ALTO nests blocks in different
// ways (margins, print space, composed blocks) and only a few elements and
// attributes matter.
func ParseALTO(r io.Reader) (*Page, error) {
	dec := xml.NewDecoder(r)
	page := &Page{}
	var (
		inMargin int // depth inside TopMargin/BottomMargin/LeftMargin/RightMargin
		block    *Block
		line     *Line
		extent   Rect // the margins and print space together cover the page
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse ALTO: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			a := attrs(t)
			switch t.Name.Local {
			case "Page":
				page.Width, page.Height = num(a["WIDTH"]), num(a["HEIGHT"])
			case "TopMargin", "BottomMargin", "LeftMargin", "RightMargin":
				inMargin++
				extent = extent.Union(altoBox(a))
			case "PrintSpace":
				extent = extent.Union(altoBox(a))
			case "TextBlock":
				page.Blocks = append(page.Blocks, Block{Box: altoBox(a), Margin: inMargin > 0})
				block = &page.Blocks[len(page.Blocks)-1]
			case "Illustration", "GraphicalElement":
				if inMargin == 0 {
					page.Blocks = append(page.Blocks, Block{Box: altoBox(a), Image: true})
					block, line = nil, nil
				}
			case "ComposedBlock":
				if t := strings.ToLower(a["TYPE"]); inMargin == 0 && (t == "illustration" || t == "picture" || t == "image") {
					page.Blocks = append(page.Blocks, Block{Box: altoBox(a), Image: true})
				}
			case "TextLine":
				if block == nil {
					continue
				}
				block.Lines = append(block.Lines, Line{Box: altoBox(a)})
				line = &block.Lines[len(block.Lines)-1]
			case "String":
				if line == nil {
					continue
				}
				w := Word{Text: a["CONTENT"], Box: altoBox(a), Conf: 1}
				if wc, err := strconv.ParseFloat(a["WC"], 64); err == nil {
					w.Conf = wc
				}
				switch a["SUBS_TYPE"] {
				case "HypPart1":
					w.HypFull = a["SUBS_CONTENT"]
				case "HypPart2":
					w.HypPart2 = true
				}
				if strings.TrimSpace(w.Text) != "" {
					line.Words = append(line.Words, w)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "TopMargin", "BottomMargin", "LeftMargin", "RightMargin":
				inMargin--
			case "TextBlock":
				block, line = nil, nil
			case "TextLine":
				line = nil
			}
		}
	}
	normaliseALTO(page, extent)
	return page, nil
}

// normaliseALTO puts every box in the units of the words. Some of nb.no's
// files give blocks, margins and the print space in a larger unit than
// lines and words, while the Page size matches the words.
func normaliseALTO(page *Page, extent Rect) {
	var ratios []float64
	var words Rect
	for i := range page.Blocks {
		b := &page.Blocks[i]
		if b.Image || len(b.Lines) == 0 {
			continue
		}
		var lines Rect
		for _, l := range b.Lines {
			lines = lines.Union(l.Box)
		}
		if lines.W() > 0 && b.Box.W() > 0 {
			ratios = append(ratios, b.Box.W()/lines.W())
		}
		b.Box = lines
		words = words.Union(lines)
	}
	scale := median(ratios)
	if scale < 1.1 {
		scale = 1
	}
	for i := range page.Blocks {
		b := &page.Blocks[i]
		if b.Image {
			b.Box = Rect{b.Box.X0 / scale, b.Box.Y0 / scale, b.Box.X1 / scale, b.Box.Y1 / scale}
		}
	}
	if page.Width < words.X1 || page.Height < words.Y1 {
		page.Width = max(page.Width, extent.X1/scale, words.X1)
		page.Height = max(page.Height, extent.Y1/scale, words.Y1)
	}
}

func attrs(t xml.StartElement) map[string]string {
	m := make(map[string]string, len(t.Attr))
	for _, a := range t.Attr {
		m[a.Name.Local] = a.Value
	}
	return m
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func altoBox(a map[string]string) Rect {
	x, y := num(a["HPOS"]), num(a["VPOS"])
	return Rect{x, y, x + num(a["WIDTH"]), y + num(a["HEIGHT"])}
}

func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	return s[len(s)/2]
}
