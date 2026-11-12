package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A drop cap is the large initial that opens a chapter, set beside the
// first two or three lines. OCR often reads it as a picture, or not at all,
// and the chapter then starts "denne delen av boken" for "I denne delen".
// Its place shows in the text: the lines beside it are indented together,
// and nothing else is printed there. Other times OCR reads it together
// with the first word, as one tall word it is unsure of ("Tønne" for
// "I denne"). The letter is read again by putting it, scaled to the size
// of the text, in front of the first line, which OCR reads as ordinary
// text.

// lostCap is a drop cap OCR did not read.
type lostCap struct {
	page, block int
	cap         rect // where the letter is printed
	merged      bool // read with the first word, as one tall word
}

// findLostCaps finds paragraphs that start with a small letter beside an
// empty space as tall as two to four of their lines.
func findLostCaps(pages []layoutPage) []lostCap {
	var out []lostCap
	for i, lp := range pages {
		if lp.Text == nil || lp.Kind != pageNormal {
			continue
		}
		for bi, b := range lp.Text.Blocks {
			if c, ok := lostCapIn(b, lp.Text.Blocks, bi); ok {
				out = append(out, lostCap{page: i, block: bi, cap: c})
			} else if c, ok := mergedCapIn(b); ok {
				out = append(out, lostCap{page: i, block: bi, cap: c, merged: true})
			}
		}
	}
	return out
}

func lostCapIn(b textBlock, blocks []textBlock, bi int) (rect, bool) {
	if b.Margin || b.Image || len(b.Lines) < 3 || len(b.Lines[0].Words) == 0 {
		return rect{}, false
	}
	first, _ := utf8.DecodeRuneInString(b.Lines[0].Words[0].Text)
	if !unicode.IsLower(first) {
		return rect{}, false
	}
	h := lineHeight(b.Lines[0])
	x0 := b.Lines[0].Box.X0
	k := 1
	for k < len(b.Lines) && abs(b.Lines[k].Box.X0-x0) < h/2 {
		k++
	}
	if k < 2 || k > 4 || k == len(b.Lines) {
		return rect{}, false
	}
	left := b.Lines[k].Box.X0
	for _, l := range b.Lines[k:] {
		left = min(left, l.Box.X0)
	}
	if x0-left < h/2 || x0-left > 6*h {
		return rect{}, false
	}
	c := rect{left, b.Lines[0].Box.Y0 - h/2, x0 - h/4, b.Lines[k-1].Box.Y1}
	for oi, o := range blocks {
		if oi != bi && !o.Margin && o.Box.X0 < c.X1 && o.Box.X1 > c.X0 && o.Box.Y0 < c.Y1 && o.Box.Y1 > c.Y0 {
			return rect{}, false
		}
	}
	return c, true
}

// mergedCapIn finds a first word far taller than the rest of its line and
// read with doubt: a drop cap and the word after it, read as one.
func mergedCapIn(b textBlock) (rect, bool) {
	if b.Margin || b.Image || len(b.Lines) < 2 || len(b.Lines[0].Words) < 3 {
		return rect{}, false
	}
	ws := b.Lines[0].Words
	var rest []float64
	for _, w := range ws[1:] {
		rest = append(rest, w.Box.H())
	}
	// A cap I read with a year ("12001") is mended by fixDropCap.
	if ws[0].Conf >= doubtConf || ws[0].Box.H() < 1.8*median(rest) || strings.ContainsAny(ws[0].Text, "0123456789") {
		return rect{}, false
	}
	return ws[0].Box, true
}

// textBand is the height of a line's text, without a tall first word.
func textBand(l textLine) rect {
	var y0, y1 []float64
	for _, w := range l.Words[1:] {
		y0 = append(y0, w.Box.Y0)
		y1 = append(y1, w.Box.Y1)
	}
	return rect{l.Box.X0, median(y0), l.Box.X1, median(y1)}
}

// restoreCaps reads the lost drop caps again from the page images and puts
// the letters back, as a word of their own or as the start of the first
// word, whichever the book's vocabulary knows. It returns how many it put
// back; a cap it cannot read is left as it was.
func restoreCaps(ctx context.Context, d *downloader, book *Book, pages []layoutPage, dir string, engines *lazyEngine) int {
	caps := findLostCaps(pages)
	if len(caps) == 0 {
		return 0
	}
	vocab := bookVocabulary(pages)
	restored := 0
	for _, c := range caps {
		pg := pages[c.page].Text
		b := &pg.Blocks[c.block]
		line := b.Lines[0]
		capBox, lineBox := c.cap, line.Box
		if c.merged {
			lineBox = textBand(line)
		}
		region := c.cap.union(lineBox)
		key := sha256.Sum256([]byte(fmt.Sprintf("%d %v", c.page, region)))
		data, err := d.cachedImage(ctx, filepath.Join(dir, fmt.Sprintf("cap-%x.jpg", key[:8])),
			figureURL(book.Pages[c.page], pg, region, 0))
		if err != nil {
			if ctx.Err() != nil {
				return restored
			}
			continue
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		gray := toGray(img)
		if c.merged {
			right, ok := capRightEdge(gray, region, c.cap, lineBox)
			if !ok {
				continue
			}
			capBox.X1, lineBox.X0 = right, right
		}
		composed := capLine(gray, region, capBox, lineBox)
		if composed == nil {
			continue
		}
		path := filepath.Join(dir, fmt.Sprintf("cap-%04d-%d.jpg", c.page+1, c.block))
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, composed, &jpeg.Options{Quality: 95}); err != nil {
			continue
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			continue
		}
		engine, err := engines.get(ctx)
		if err != nil {
			return restored
		}
		tsv, err := engine.recognize(ctx, path)
		os.Remove(path)
		if err != nil {
			continue
		}
		read, err := parseTSV(tsv)
		if err != nil {
			continue
		}
		var words []string
		for _, rb := range read.Blocks {
			for _, l := range rb.Lines {
				for _, w := range l.Words {
					words = append(words, w.Text)
				}
			}
		}
		known := func(s string) bool { return vocab[strings.ToLower(s)] >= minKnown }
		if c.merged {
			if fixed := mergedWords(words, line.Words[1].Text, known); fixed != nil {
				replaceFirstWord(&b.Lines[0], fixed, capBox, lineBox)
				restored++
			}
			continue
		}
		if letter := capLetter(words, line.Words[0].Text); letter != "" && putCap(&b.Lines[0], letter, c.cap, known) {
			restored++
		}
	}
	return restored
}

// capLetter finds the letter OCR read in front of the first word.
func capLetter(read []string, first string) string {
	_, core, _ := splitWordParts(first)
	if len(read) == 0 || core == "" {
		return ""
	}
	var prefix string
	_, w0, _ := splitWordParts(read[0])
	switch {
	case strings.HasSuffix(w0, core) && len(w0) > len(core):
		prefix = strings.TrimSuffix(w0, core)
	case len(read) > 1 && strings.HasPrefix(read[1], core):
		prefix = w0
	default:
		return ""
	}
	// A drop-cap I is often read as a one or a bar.
	if prefix == "1" || prefix == "|" || prefix == "l" {
		prefix = "I"
	}
	r, size := utf8.DecodeRuneInString(prefix)
	if utf8.RuneCountInString(prefix) > 2 || !unicode.IsUpper(r) || !onlyLetters(prefix[size:]) {
		return ""
	}
	return prefix
}

func onlyLetters(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// putCap puts the letter back before the first word of the line: joined
// to it when that makes a word the book uses ("D" and "ette"), on its own
// when it is a word and the first word is one too ("I" and "denne").
// Otherwise the letter was likely misread, and nothing changes.
func putCap(line *textLine, letter string, at rect, known func(string) bool) bool {
	w := &line.Words[0]
	pre, core, post := splitWordParts(w.Text)
	switch {
	case known(letter + core):
		w.Text = pre + letter + core + post
	case known(letter) && known(core):
		// The size of the text, so it is not taken for a drop cap again.
		h := lineHeight(*line)
		box := rect{at.X0, w.Box.Y1 - h, at.X1, w.Box.Y1}
		line.Words = append([]textWord{{Text: letter, Box: box, Conf: 1}}, line.Words...)
		line.Box = line.Box.union(box)
	default:
		return false
	}
	return true
}

// mergedWords reads what OCR found before the line's second word in the
// rebuilt line and makes it a word the book uses, or two ("I" "denne"),
// or returns nil.
func mergedWords(read []string, second string, known func(string) bool) []string {
	_, next, _ := splitWordParts(second)
	var before []string
	for _, w := range read {
		if _, core, _ := splitWordParts(w); core == next {
			break
		}
		before = append(before, w)
	}
	if len(before) == 0 || len(before) == len(read) {
		return nil
	}
	var core strings.Builder
	post := ""
	for _, w := range before {
		_, c, p := splitWordParts(w)
		core.WriteString(c)
		post = p
	}
	word := core.String()
	if strings.ContainsAny(word[:1], "1|l") {
		word = "I" + word[1:] // a drop-cap I read as a one or a bar
	}
	r, size := utf8.DecodeRuneInString(word)
	rest := word[size:]
	if !unicode.IsUpper(r) || rest == "" || !onlyLetters(rest) {
		return nil
	}
	switch {
	case known(word):
		return []string{word + post}
	case known(string(r)) && known(rest):
		return []string{string(r), rest + post}
	}
	return nil
}

// replaceFirstWord puts the words read in place of the tall first word,
// at the size of the text.
func replaceFirstWord(line *textLine, words []string, capBox, band rect) {
	var ws []textWord
	if len(words) == 2 {
		ws = append(ws, textWord{Text: words[0], Conf: 1, Box: rect{capBox.X0, band.Y0, capBox.X1, band.Y1}})
		words = words[1:]
	}
	x0 := capBox.X0
	if len(ws) > 0 {
		x0 = band.X0
	}
	ws = append(ws, textWord{Text: words[0], Conf: 1, Box: rect{x0, band.Y0, line.Words[0].Box.X1, band.Y1}})
	line.Words = append(ws, line.Words[1:]...)
}

// capRightEdge finds where a drop cap read with the first word ends: the
// cap reaches down past the first line, the letters after it do not.
func capRightEdge(img *image.Gray, region, word, band rect) (float64, bool) {
	scale := float64(img.Bounds().Dx()) / region.W()
	y0 := int((band.Y1 + 0.4*band.H() - region.Y0) * scale)
	y1 := min(img.Bounds().Dy(), int((word.Y1-region.Y0)*scale))
	x0, x1 := max(0, int((word.X0-region.X0)*scale)), min(img.Bounds().Dx(), int((word.X1-region.X0)*scale))
	if y0 >= y1 || x0 >= x1 {
		return 0, false
	}
	ink := inkBounds(img, image.Rect(x0, 0, x1, img.Bounds().Dy()))
	if ink.Empty() {
		return 0, false
	}
	dark := paperLevel(img, img.Bounds()) - 80
	inked := func(x int) bool {
		for y := y0; y < y1; y++ {
			if float64(img.GrayAt(x, y).Y) < dark {
				return true
			}
		}
		return false
	}
	x := x0
	for x < x1 && !inked(x) {
		x++
	}
	if x == x1 {
		return 0, false
	}
	for x < x1 && inked(x) {
		x++
	}
	if x >= x1 {
		return 0, false
	}
	return region.X0 + float64(x)/scale, true
}

// bookVocabulary counts the words of the book read with confidence, by
// their lower-case form.
func bookVocabulary(pages []layoutPage) map[string]int {
	vocab := map[string]int{}
	for _, lp := range pages {
		eachWord(lp, func(w *textWord) {
			if w.Conf >= sureConf {
				if _, core, _ := splitWordParts(w.Text); core != "" {
					vocab[strings.ToLower(core)]++
				}
			}
		})
	}
	return vocab
}

func toGray(img image.Image) *image.Gray {
	b := img.Bounds()
	g := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			g.Set(x, y, color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)))
		}
	}
	return g
}

// capLine builds an image of the first line with the drop cap scaled down
// in front of it, as if it were set at the size of the text. region is the
// part of the page img shows, in the page's text units.
func capLine(img *image.Gray, region, capBox, lineBox rect) *image.Gray {
	scale := float64(img.Bounds().Dx()) / region.W()
	px := func(v, origin float64) int { return int((v - origin) * scale) }
	capArea := image.Rect(0, 0, px(capBox.X1, region.X0), img.Bounds().Dy())
	lineArea := image.Rect(px(lineBox.X0, region.X0)-2, px(lineBox.Y0, region.Y0), img.Bounds().Dx(), px(lineBox.Y1, region.Y0))
	lineArea = lineArea.Intersect(img.Bounds())
	ink := inkBounds(img, capArea)
	if ink.Empty() || lineArea.Empty() {
		return nil
	}
	lineH := lineArea.Dy()
	// Capitals stand about seven tenths of a line's height, from the
	// baseline, which lies about four fifths down the line.
	capH := lineH * 7 / 10
	capW := max(1, ink.Dx()*capH/max(1, ink.Dy()))
	pad, gap := lineH/2, lineH/3
	out := image.NewGray(image.Rect(0, 0, pad+capW+gap+lineArea.Dx()+pad, lineH+2*pad))
	for i := range out.Pix {
		out.Pix[i] = 255
	}
	baseline := pad + lineH*4/5
	resize(out, image.Rect(pad, baseline-capH, pad+capW, baseline), img, ink)
	for y := 0; y < lineArea.Dy(); y++ {
		for x := 0; x < lineArea.Dx(); x++ {
			out.SetGray(pad+capW+gap+x, pad+y, img.GrayAt(lineArea.Min.X+x, lineArea.Min.Y+y))
		}
	}
	return out
}

// inkBounds is the box around the dark pixels within area.
func inkBounds(img *image.Gray, area image.Rectangle) image.Rectangle {
	area = area.Intersect(img.Bounds())
	if area.Empty() {
		return image.Rectangle{}
	}
	// The paper is judged on the whole image: a bold cap may fill most of
	// its own area.
	dark := uint8(max(0, paperLevel(img, img.Bounds())-80))
	ink := image.Rectangle{}
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			if img.GrayAt(x, y).Y < dark {
				ink = ink.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return ink
}

// paperLevel is the brightness of the paper within area.
func paperLevel(img *image.Gray, area image.Rectangle) float64 {
	area = area.Intersect(img.Bounds())
	var lum []float64
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			lum = append(lum, float64(img.GrayAt(x, y).Y))
		}
	}
	if len(lum) == 0 {
		return 255
	}
	return median(lum)
}

// resize draws the src area of img into dst area of out, averaging the
// pixels each output pixel covers.
func resize(out *image.Gray, dst image.Rectangle, img *image.Gray, src image.Rectangle) {
	fx := float64(src.Dx()) / float64(dst.Dx())
	fy := float64(src.Dy()) / float64(dst.Dy())
	for y := 0; y < dst.Dy(); y++ {
		for x := 0; x < dst.Dx(); x++ {
			x0, y0 := src.Min.X+int(float64(x)*fx), src.Min.Y+int(float64(y)*fy)
			x1, y1 := max(x0+1, src.Min.X+int(float64(x+1)*fx)), max(y0+1, src.Min.Y+int(float64(y+1)*fy))
			sum, n := 0, 0
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					sum += int(img.GrayAt(sx, sy).Y)
					n++
				}
			}
			out.SetGray(dst.Min.X+x, dst.Min.Y+y, color.Gray{Y: uint8(sum / n)})
		}
	}
}
