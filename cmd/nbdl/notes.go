package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Notes. Many books number their notes through the whole book and print
// them at the back, under a heading such as "Sluttnoter", with the numbers
// in a column of their own. The text refers to them with small raised
// numbers, which OCR reads as "*", "?" or not at all. A marker it did see
// is read again from the page image, enlarged, as a line of its own; the
// note section's numbers are put back beside their notes. A reference
// becomes a link only when the note it names exists and the references
// around it keep the book's order, so a misread number links nowhere
// rather than to the wrong note.

// noteRef marks a note reference in paragraph text until it is written
// out as a link; private-use characters survive the text clean-up.
const (
	refOpen  = ""
	refClose = ""
)

var refPattern = regexp.MustCompile(refOpen + `(\d+)` + refClose)

func refMark(n int) string { return refOpen + strconv.Itoa(n) + refClose }

// stripRefs removes reference marks, for tests on the text around them.
func stripRefs(s string) string { return refPattern.ReplaceAllString(s, "") }

// readNoteRefs reads the numbers of the note markers OCR saw, from the
// page images. It returns how many it read.
func readNoteRefs(ctx context.Context, d *downloader, book *Book, pages []layoutPage, dir string, engines *lazyEngine, workers int, report progressFunc) int {
	type job struct {
		page int
		word *textWord
	}
	var jobs []job
	notes := notesPages(pages, measureBook(pages))
	for i, lp := range pages {
		// Marks in the notes themselves refer to nothing.
		if lp.Kind != pageNormal || notes[i] {
			continue
		}
		eachWord(lp, func(w *textWord) {
			if w.Marker && w.Box.W() > 0 {
				jobs = append(jobs, job{i, w})
			}
		})
	}
	if len(jobs) == 0 {
		return 0
	}
	read := make([]int, len(jobs))
	alts := make([][]int, len(jobs))
	_ = parallel(ctx, len(jobs), workers, func(done, total int) { report("Reading note references", done, total) },
		func(ctx context.Context, j int) error {
			pg := pages[jobs[j].page].Text
			w := jobs[j].word
			h := w.Box.H()
			// The raised number ends the word, in its upper part.
			region := rect{max(0, w.Box.X1-1.8*h), max(0, w.Box.Y0-0.3*h), min(pg.Width, w.Box.X1+0.3*h), w.Box.Y0 + 0.7*h}
			whole, cut, err := readRegion(ctx, d, book, pg, jobs[j].page, region, dir, engines, h)
			if err != nil {
				return err
			}
			read[j], alts[j] = bestReading(markerNumber(whole), markerNumber(cut))
			return nil
		})
	settleReadings(read, alts)
	// What OCR made of a marker it did not read as a number may be left
	// at the end of the word as a quote mark ("prosent.'"). A curly
	// closing quote is such a remnant only in a book that never opens one,
	// quoting with « » instead.
	remnants := "'\"’?!*%>"
	curly := false
	for _, lp := range pages {
		eachWord(lp, func(w *textWord) { curly = curly || strings.ContainsRune(w.Text, '“') })
	}
	if !curly {
		remnants += "”"
	}
	n := 0
	for j, num := range read {
		if num > 0 {
			w := jobs[j].word
			w.Note = num
			w.Text = trimMarker(w.Text, num, remnants)
			n++
		}
	}
	return n
}

// trimMarker removes what is left of a read marker at the end of a word:
// stray marks, and figures that are part of its number ("kroner5" for
// note 56). The word must end a sentence or in a letter afterwards.
func trimMarker(word string, num int, remnants string) string {
	n := strconv.Itoa(num)
	t := strings.TrimRight(word, remnants)
	end := len(t)
	for end > 0 && t[end-1] >= '0' && t[end-1] <= '9' {
		end--
	}
	if digits := t[end:]; digits != "" && (strings.HasPrefix(n, digits) || strings.HasSuffix(n, digits)) {
		t = strings.TrimRight(t[:end], remnants)
	}
	if t == "" || t == word {
		return word
	}
	if endsSentence(t) || endsLowerLetter(t) {
		return t
	}
	return word
}

// readNoteNumbers reads the numbers of notes whose number OCR missed,
// from the strip of the page image left of the note's first line, where
// the number is printed. A note continued from the line before has none,
// and stays a continuation. It returns how many it read.
func readNoteNumbers(ctx context.Context, d *downloader, book *Book, pages []layoutPage, dir string, engines *lazyEngine, workers int, report progressFunc) int {
	st := measureBook(pages)
	type job struct {
		page int
		line *textLine
	}
	var jobs []job
	notes := notesPages(pages, st)
	for i := range pages {
		if !notes[i] {
			continue
		}
		pg := pages[i].Text
		view := withNoteNumbers(pg, st)
		numbered := map[rect]bool{}
		for _, b := range view.Blocks {
			for _, l := range b.Lines {
				if noteNumber(l) != 0 {
					numbered[l.Box] = true
				}
			}
		}
		for bi := range pg.Blocks {
			b := &pg.Blocks[bi]
			if b.Margin || b.Image || len(b.Lines) == 0 {
				continue
			}
			l := &b.Lines[0]
			if numbered[l.Box] || noteNumber(*l) != 0 || len(l.Words) < 2 || !startsUpper(l.Words[0].Text) {
				continue
			}
			jobs = append(jobs, job{i, l})
		}
	}
	if len(jobs) == 0 {
		return 0
	}
	read := make([]int, len(jobs))
	_ = parallel(ctx, len(jobs), workers, func(done, total int) { report("Reading note numbers", done, total) },
		func(ctx context.Context, j int) error {
			pg := pages[jobs[j].page].Text
			l := jobs[j].line
			h := l.Box.H()
			region := rect{max(0, l.Box.X0-3*h), l.Box.Y0, l.Box.X0 - 0.15*h, l.Box.Y1}
			if region.W() <= 0 {
				return nil
			}
			text, _, err := readRegion(ctx, d, book, pg, jobs[j].page, region, dir, engines, 0)
			if err != nil {
				return err
			}
			// Nothing but the number is printed here, so a lone stroke is
			// a one.
			t := strings.TrimRight(strings.TrimSpace(text), ".")
			if t != "" && len(t) <= 3 && strings.Trim(t, "0123456789oOlI|") == "" {
				n, err := strconv.Atoi(strings.NewReplacer("o", "0", "O", "0", "l", "1", "I", "1", "|", "1").Replace(t))
				if err == nil && n > 0 {
					read[j] = n
				}
			}
			return nil
		})
	n := 0
	for j, num := range read {
		if num > 0 {
			l := jobs[j].line
			box := rect{l.Box.X0 - 2*l.Box.H(), l.Box.Y0, l.Box.X0 - 0.2*l.Box.H(), l.Box.Y1}
			l.Words = append([]textWord{{Text: strconv.Itoa(num), Box: box, Conf: 1}}, l.Words...)
			n++
		}
	}
	return n
}

// readRegion reads a part of a page image as a single line of text,
// enlarged. When raised is set, it also reads the raised number cut out
// of it and returns that as well.
func readRegion(ctx context.Context, d *downloader, book *Book, pg *textPage, page int, region rect, dir string, engines *lazyEngine, raised float64) (string, string, error) {
	key := sha256.Sum256([]byte(fmt.Sprintf("%d %v", page, region)))
	data, err := d.cachedImage(ctx, filepath.Join(dir, fmt.Sprintf("ref-%x.jpg", key[:8])), figureURL(book.Pages[page], pg, region, 0))
	if err != nil {
		return "", "", ctx.Err()
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return "", "", nil
	}
	gray := toGray(img)
	whole, err := readLine(ctx, gray, dir, engines)
	if err != nil || raised == 0 {
		return whole, "", err
	}
	cut := raisedInk(gray, raised*float64(gray.Bounds().Dx())/region.W())
	if cut == nil {
		return whole, "", nil
	}
	part, err := readLine(ctx, cut, dir, engines)
	return whole, part, err
}

// readLine reads an image as a single line of text, enlarged.
func readLine(ctx context.Context, img *image.Gray, dir string, engines *lazyEngine) (string, error) {
	f, err := os.CreateTemp(dir, "read-*.jpg")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	err = jpeg.Encode(f, enlarge(img, 4, 60), &jpeg.Options{Quality: 95})
	f.Close()
	if err != nil {
		return "", err
	}
	engine, err := engines.get(ctx)
	if err != nil {
		return "", err
	}
	tsv, err := engine.recognizeLine(ctx, f.Name())
	if err != nil {
		return "", nil
	}
	read, err := parseTSV(tsv)
	if err != nil {
		return "", nil
	}
	var text strings.Builder
	for _, b := range read.Blocks {
		for _, l := range b.Lines {
			text.WriteString(lineText(l))
		}
	}
	return text.String(), nil
}

// bestReading settles a marker's number from the reading of the word's
// end and of the raised number cut out of it. It is sure when they agree,
// when one found nothing, or when one holds only the last figures of the
// other ("3" for "53"). Otherwise both are candidates for settleReadings.
func bestReading(whole, raised int) (sure int, alts []int) {
	w, r := strconv.Itoa(whole), strconv.Itoa(raised)
	switch {
	case whole == 0 || whole == raised:
		return raised, nil
	case raised == 0 || strings.HasSuffix(w, r):
		return whole, nil
	case strings.HasSuffix(r, w):
		return raised, nil
	}
	return 0, []int{whole, raised}
}

// settleReadings picks, for each marker whose readings disagree, the
// candidate that falls between the sure numbers before and after it, and
// leaves the marker unread when none or both do.
func settleReadings(sure []int, alts [][]int) {
	known := append([]int(nil), sure...)
	for j, cands := range alts {
		if len(cands) == 0 {
			continue
		}
		prev, next := 0, math.MaxInt
		for k := j - 1; k >= 0; k-- {
			if known[k] > 0 {
				prev = known[k]
				break
			}
		}
		for k := j + 1; k < len(known); k++ {
			if known[k] > 0 {
				next = known[k]
				break
			}
		}
		fit := 0
		for _, c := range cands {
			if c > prev && c < next {
				if fit != 0 {
					fit = 0
					break
				}
				fit = c
			}
		}
		sure[j] = fit
	}
}

// markerNumber takes the number at the end of what OCR read of a word's
// end ("e.»36", ".»12O"), reading the round zero and the one of old-style
// figures as the digits they are. Without a real digit, the run must
// follow the punctuation the number is printed after.
func markerNumber(s string) int {
	s = strings.TrimSpace(s)
	start := len(s)
	for start > 0 && strings.IndexByte("0123456789oOlI|", s[start-1]) >= 0 {
		start--
	}
	run := s[start:]
	if run == "" {
		return 0
	}
	before, size := utf8.DecodeLastRuneInString(s[:start])
	if !strings.ContainsAny(run, "0123456789") && !strings.ContainsRune(".,;:»”\")!?*", before) {
		return 0
	}
	// A "!" after the end of the sentence is a figure misread, so the
	// number is not all there (".!9" for 10).
	if gone, _ := utf8.DecodeLastRuneInString(s[:start-size]); before == '!' && strings.ContainsRune(".,;:»”\")?", gone) {
		return 0
	}
	n, err := strconv.Atoi(strings.NewReplacer("o", "0", "O", "0", "l", "1", "I", "1", "|", "1").Replace(run))
	if err != nil || n <= 0 || n > 999 {
		return 0
	}
	return n
}

// raisedInk cuts the raised number out of a crop of a word's end: the
// rightmost cluster of ink that stays clear of the text line, which the
// word's letters reach down to. wordH is the word's height in pixels; the
// crop runs from above the word to above the line's foot, so it cuts
// through the letters. It returns nil when nothing is raised.
func raisedInk(img *image.Gray, wordH float64) *image.Gray {
	b := img.Bounds()
	dark := uint8(max(0, paperLevel(img, b)-80))
	type cluster struct{ x0, x1, y0, y1, ink int }
	var cs []cluster
	gap := max(2, int(0.12*wordH))
	cur := cluster{x0: -1}
	blank := 0
	for x := b.Min.X; x <= b.Max.X; x++ {
		inked, top, bottom, n := false, b.Max.Y, b.Min.Y, 0
		if x < b.Max.X {
			for y := b.Min.Y; y < b.Max.Y; y++ {
				if img.GrayAt(x, y).Y < dark {
					inked = true
					top, bottom, n = min(top, y), max(bottom, y+1), n+1
				}
			}
		}
		if inked {
			if cur.x0 < 0 {
				cur = cluster{x0: x, y0: top, y1: bottom}
			}
			cur.x1, cur.y0, cur.y1, cur.ink = x+1, min(cur.y0, top), max(cur.y1, bottom), cur.ink+n
			blank = 0
			continue
		}
		blank++
		if cur.x0 >= 0 && (blank > gap || x == b.Max.X) {
			cs = append(cs, cur)
			cur = cluster{x0: -1}
		}
	}
	// Raised: clear of the bottom edge, which cuts through the letters of
	// the line; and more than a speck.
	minInk := int(0.02 * wordH * wordH)
	raised := func(c cluster) bool { return c.y1 < b.Max.Y-1 && c.ink >= minInk }
	last := -1
	for i := len(cs) - 1; i >= 0; i-- {
		if raised(cs[i]) {
			last = i
			break
		}
	}
	if last < 0 {
		return nil
	}
	r := image.Rect(cs[last].x0, cs[last].y0, cs[last].x1, cs[last].y1)
	// Figures of one number set a little apart.
	for i := last - 1; i >= 0 && raised(cs[i]) && cs[i+1].x0-cs[i].x1 <= int(0.35*wordH); i-- {
		r = r.Union(image.Rect(cs[i].x0, cs[i].y0, cs[i].x1, cs[i].y1))
	}
	pad := max(2, int(0.1*wordH))
	r = image.Rect(r.Min.X-pad, r.Min.Y-pad, r.Max.X+pad, r.Max.Y+pad).Intersect(b)
	out := image.NewGray(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		for x := 0; x < r.Dx(); x++ {
			out.SetGray(x, y, img.GrayAt(r.Min.X+x, r.Min.Y+y))
		}
	}
	return out
}

// enlarge scales a small image up by factor, smoothing between pixels,
// with a white border, the size OCR reads best.
func enlarge(img *image.Gray, factor, border int) *image.Gray {
	b := img.Bounds()
	w, h := b.Dx()*factor, b.Dy()*factor
	out := image.NewGray(image.Rect(0, 0, w+2*border, h+2*border))
	for i := range out.Pix {
		out.Pix[i] = 255
	}
	at := func(x, y int) float64 {
		x = min(max(x, 0), b.Dx()-1)
		y = min(max(y, 0), b.Dy()-1)
		return float64(img.GrayAt(b.Min.X+x, b.Min.Y+y).Y)
	}
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)/float64(factor) - 0.5
		y0 := int(fy)
		if fy < 0 {
			y0 = -1
		}
		ty := fy - float64(y0)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)/float64(factor) - 0.5
			x0 := int(fx)
			if fx < 0 {
				x0 = -1
			}
			tx := fx - float64(x0)
			v := (at(x0, y0)*(1-tx)+at(x0+1, y0)*tx)*(1-ty) + (at(x0, y0+1)*(1-tx)+at(x0+1, y0+1)*tx)*ty
			out.SetGray(border+x, border+y, color.Gray{Y: uint8(v + 0.5)})
		}
	}
	return out
}

// notesTitle recognises the heading of a book's notes.
var notesTitle = regexp.MustCompile(`(?i)^(slutt|fot)?noter$|^(end|foot)?notes$|^henvisninger$`)

// notesPages finds the pages of the notes section: from the page whose
// heading names it, as long as the pages after it hold numbered notes.
// The heading's page counts even when OCR read none of its numbers,
// often the case for single figures.
func notesPages(pages []layoutPage, st bookStats) map[int]bool {
	out := map[int]bool{}
	start := -1
	for i, lp := range pages {
		if lp.Text == nil || lp.Kind != pageNormal || lp.Blank || lp.ImageOnly {
			continue
		}
		for _, b := range lp.Text.Blocks {
			// In the upper half: the same word at the foot of a page is a
			// running foot.
			if !b.Margin && len(b.Lines) == 1 && b.Box.Y1 < lp.Text.Height/2 &&
				notesTitle.MatchString(strings.TrimSpace(lineText(b.Lines[0]))) {
				start = i
			}
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return out
	}
	out[start] = true
	for i := start + 1; i < len(pages); i++ {
		lp := pages[i]
		if lp.Text == nil || lp.Kind != pageNormal {
			continue
		}
		if numberedLines(withNoteNumbers(lp.Text, st)) < 2 {
			break
		}
		out[i] = true
	}
	if len(out) == 1 && numberedLines(withNoteNumbers(pages[start].Text, st)) < 2 {
		return map[int]bool{} // a heading without notes after it
	}
	return out
}

// unknownNote stands for a note's number OCR could not read.
const unknownNote = "\ue002"

// noteNumber reads a line that opens with a note's number ("145 Sverige"),
// and returns -1 for one whose number OCR could not read.
func noteNumber(l textLine) int {
	if len(l.Words) < 2 {
		return 0
	}
	if l.Words[0].Text == unknownNote {
		return -1
	}
	t := strings.TrimSuffix(l.Words[0].Text, ".")
	if len(t) > 3 || strings.Trim(t, "0123456789") != "" {
		return 0
	}
	n, _ := strconv.Atoi(t)
	return n
}

func numberedLines(pg *textPage) int {
	n := 0
	for _, b := range pg.Blocks {
		for _, l := range b.Lines {
			if noteNumber(l) != 0 {
				n++
			}
		}
	}
	return n
}

// withNoteNumbers returns the page with the numbers printed in a column
// beside the notes moved onto the lines they number. OCR returns such a
// column as one block per number or as one block of several.
func withNoteNumbers(pg *textPage, st bookStats) *textPage {
	lh := st.bodyLH * pg.Height
	if lh <= 0 {
		lh = 0.012 * pg.Height
	}
	blocks := make([]textBlock, len(pg.Blocks))
	for i, b := range pg.Blocks {
		blocks[i] = b
		blocks[i].Lines = append([]textLine{}, b.Lines...)
		for li := range b.Lines {
			blocks[i].Lines[li].Words = append([]textWord{}, b.Lines[li].Words...)
		}
	}
	isNumber := func(l textLine) bool {
		if len(l.Words) != 1 {
			return false
		}
		t := strings.TrimSuffix(l.Words[0].Text, ".")
		return t != "" && len(t) <= 3 && strings.Trim(t, "0123456789") == ""
	}
	// Number lines in blocks of nothing but short lines.
	type ref struct{ block, line int }
	var nums []ref
	for bi, b := range blocks {
		if b.Margin || b.Image {
			continue
		}
		short := true
		for _, l := range b.Lines {
			short = short && len(l.Words) <= 2
		}
		if !short {
			continue
		}
		for li, l := range b.Lines {
			if isNumber(l) {
				nums = append(nums, ref{bi, li})
			}
		}
	}
	// Three or more at one left edge make a column.
	var column []ref
	for _, r := range nums {
		x := blocks[r.block].Lines[r.line].Box.X0
		same := 0
		for _, o := range nums {
			if abs(blocks[o.block].Lines[o.line].Box.X0-x) < lh {
				same++
			}
		}
		if same >= 3 {
			column = append(column, r)
		}
	}
	// Lines of the column OCR could not read as numbers ("$2") still start
	// a note, its number unknown.
	for bi, b := range blocks {
		if b.Margin || b.Image || len(column) == 0 {
			continue
		}
		short := true
		for _, l := range b.Lines {
			short = short && len(l.Words) <= 2
		}
		for li, l := range b.Lines {
			x := blocks[column[0].block].Lines[column[0].line].Box.X0
			if short && len(l.Words) == 1 && !isNumber(l) && abs(l.Box.X0-x) < lh/2 && l.Box.W() < 2*lh {
				column = append(column, ref{bi, li})
			}
		}
	}
	moved := map[ref]bool{}
	for _, r := range column {
		num := blocks[r.block].Lines[r.line]
		mid := (num.Box.Y0 + num.Box.Y1) / 2
		best, bestLine, bestDist := -1, -1, lh/2
		for j, b := range blocks {
			if j == r.block || b.Margin || b.Image {
				continue
			}
			for li, l := range b.Lines {
				if d := abs((l.Box.Y0+l.Box.Y1)/2 - mid); d < bestDist && l.Box.X0 > num.Box.X1 && !isNumber(l) {
					best, bestLine, bestDist = j, li, d
				}
			}
		}
		if best < 0 {
			continue
		}
		l := &blocks[best].Lines[bestLine]
		word := num.Words[0]
		word.Text = strings.TrimSuffix(word.Text, ".")
		if !isNumber(num) {
			word.Text = unknownNote
		}
		l.Words = append([]textWord{word}, l.Words...)
		moved[r] = true
	}
	out := *pg
	out.Blocks = nil
	for bi, b := range blocks {
		var kept []textLine
		for li, l := range b.Lines {
			if !moved[ref{bi, li}] {
				kept = append(kept, l)
			}
		}
		if len(kept) == 0 && len(b.Lines) > 0 {
			continue
		}
		if len(kept) < len(b.Lines) {
			b.Lines = kept
			b.Box = boxOf(kept)
		}
		out.Blocks = append(out.Blocks, b)
	}
	return &out
}

// noteParas lays out a block of the notes section: each line opening with
// a number starts a note, other lines continue the one before.
func noteParas(b textBlock, page int) []para {
	var out []para
	var lines []textLine
	num := 0
	flush := func() {
		if len(lines) == 0 {
			return
		}
		switch {
		case num > 0:
			l0 := lines[0]
			l0.Words = l0.Words[1:]
			lines[0] = l0
			out = append(out, para{kind: "endnote", note: num, text: joinLines(lines), page: page})
		case num < 0:
			// A note of its own; its number is settled with the others.
			l0 := lines[0]
			l0.Words = l0.Words[1:]
			lines[0] = l0
			out = append(out, para{kind: "p", text: joinLines(lines), page: page})
		default:
			out = append(out, para{kind: "p", text: joinLines(lines), page: page})
		}
		lines = nil
	}
	for _, l := range b.Lines {
		if n := noteNumber(l); n != 0 {
			flush()
			num = n
		}
		lines = append(lines, l)
	}
	flush()
	return out
}

// numberNotes settles the numbers of the notes section. Numbers that keep
// the order count; a run of unnumbered paragraphs between two of them is
// numbered when it holds as many notes as the gap ("D OLN A" for 1 to 9),
// and is otherwise the continuation of the note before. It returns the
// numbers of the notes.
func numberNotes(paras []para, inNotes func(p para) bool) map[int]bool {
	// Notes, and paragraphs that may be notes: OCR's remains of a column
	// of numbers ("An a w") hold no word and are none.
	var idx []int
	for i, p := range paras {
		if inNotes(p) && (p.kind == "endnote" || (p.kind == "p" || p.kind == "note") && realWord.MatchString(p.text)) {
			idx = append(idx, i)
		}
	}
	// Keep the numbers that rise one at a time or are confirmed by the
	// next one; a line of a note that opens with a figure ("70 prosent")
	// is text.
	prev, pending := 0, 0 // the last number kept, and paragraphs since it
	for k, i := range idx {
		p := &paras[i]
		if p.kind != "endnote" {
			pending++
			continue
		}
		next := 0
		for _, j := range idx[k+1:] {
			if paras[j].kind == "endnote" {
				next = paras[j].note
				break
			}
		}
		if p.note > prev && p.note <= prev+1+pending ||
			p.note > prev && next == p.note+1 && (prev == 0 || p.note <= prev+15) {
			prev, pending = p.note, 0
			continue
		}
		pending++
		p.kind = "p"
		p.text = strconv.Itoa(p.note) + " " + p.text
		p.note = 0
	}
	// Fill gaps whose size matches, counting either every paragraph or
	// only those that look like the start of a note.
	last := 0
	var gap []int
	for _, i := range idx {
		p := &paras[i]
		if p.kind != "endnote" {
			gap = append(gap, i)
			continue
		}
		missing := p.note - last - 1
		if len(gap) != missing {
			var starts []int
			for _, g := range gap {
				if noteStart(paras, g) {
					starts = append(starts, g)
				}
			}
			gap = starts
		}
		if len(gap) == missing {
			for k, g := range gap {
				paras[g].kind, paras[g].note = "endnote", last+1+k
			}
		}
		gap, last = nil, p.note
	}
	nums := map[int]bool{}
	for _, i := range idx {
		if paras[i].kind == "endnote" {
			nums[paras[i].note] = true
		}
	}
	return nums
}

// noteStart tells whether a paragraph looks like a note of its own: it
// opens with a capital after a paragraph that ended a sentence.
func noteStart(paras []para, i int) bool {
	return i > 0 && startsUpper(paras[i].text) && endsSentence(paras[i-1].text) && !strings.HasSuffix(strings.TrimSpace(stripRefs(paras[i-1].text)), ":")
}

var realWord = regexp.MustCompile(`\pL{3}`)

// keepRefs keeps the references that name an existing note and keep the
// book's order, the longest such run; the others are dropped.
func keepRefs(paras []para, notes map[int]bool) map[int]bool {
	var nums []int
	for _, p := range paras {
		if p.kind == "endnote" {
			continue
		}
		for _, m := range refPattern.FindAllStringSubmatch(p.text, -1) {
			n, _ := strconv.Atoi(m[1])
			if notes[n] {
				nums = append(nums, n)
			}
		}
	}
	keep := map[int]bool{}
	for _, n := range longestRising(nums) {
		keep[n] = true
	}
	for i := range paras {
		paras[i].text = refPattern.ReplaceAllStringFunc(paras[i].text, func(m string) string {
			n, _ := strconv.Atoi(refPattern.FindStringSubmatch(m)[1])
			if keep[n] && paras[i].kind != "endnote" {
				delete(keep, n) // each note is linked once
				return m
			}
			return ""
		})
	}
	kept := map[int]bool{}
	for _, p := range paras {
		for _, m := range refPattern.FindAllStringSubmatch(p.text, -1) {
			n, _ := strconv.Atoi(m[1])
			kept[n] = true
		}
	}
	return kept
}

// longestRising returns the longest strictly rising subsequence.
func longestRising(nums []int) []int {
	if len(nums) == 0 {
		return nil
	}
	length := make([]int, len(nums))
	prev := make([]int, len(nums))
	best := 0
	for i := range nums {
		length[i], prev[i] = 1, -1
		for j := 0; j < i; j++ {
			if nums[j] < nums[i] && length[j]+1 > length[i] {
				length[i], prev[i] = length[j]+1, j
			}
		}
		if length[i] > length[best] {
			best = i
		}
	}
	var out []int
	for i := best; i >= 0; i = prev[i] {
		out = append([]int{nums[i]}, out...)
	}
	return out
}

// noteLinks writes the reference marks of escaped paragraph text as links
// to their notes.
func noteLinks(escaped string) string {
	return refPattern.ReplaceAllString(escaped, `<a epub:type="noteref" class="noteref" id="ref-$1" href="#note-$1"><sup>$1</sup></a>`)
}

var hrefPattern = regexp.MustCompile(`href="#((?:note|ref)-\d+)"`)
var idPattern = regexp.MustCompile(`id="((?:note|ref)-\d+)"`)

// linkAcross points the links between references and notes at the
// chapter files they are in.
func linkAcross(chapters []epubChapter) {
	where := map[string]string{}
	for i, ch := range chapters {
		for _, m := range idPattern.FindAllStringSubmatch(ch.Body, -1) {
			where[m[1]] = chapterFile(i)
		}
	}
	for i := range chapters {
		chapters[i].Body = hrefPattern.ReplaceAllStringFunc(chapters[i].Body, func(m string) string {
			id := hrefPattern.FindStringSubmatch(m)[1]
			if f, ok := where[id]; ok {
				return `href="` + f + `#` + id + `"`
			}
			return m
		})
	}
}

// remnantTail matches what OCR made of a note marker it did not read as a
// number at the end of a word: "eksportsektor.”", "økonomi*”",
// "inntekter.*%8", "sommeren.\"s". A trailing comma or colon is kept.
var remnantTail = regexp.MustCompile(`^(.*[\p{L}\p{N}.;:)»?!])([”"*][”"*'’%?!:;+s$]*\d*)([,;:]?)$`)

// afterStop matches what is left of a marker after a full stop that
// ends a word: "land.?", "matematikk.$".
var afterStop = regexp.MustCompile(`^(.*\p{L}\.)([?$'’][”"*'’%?!:;+s$]*\d*)()$`)

// dropRemnants removes marker remnants from the ends of words in a book that
// quotes with « », where a double quote or a star never ends a word. Words
// that open a quote of their own are left alone, and so are the notes.
func dropRemnants(pages []layoutPage) int {
	guillemets, other := 0, false
	for _, lp := range pages {
		eachWord(lp, func(w *textWord) {
			guillemets += strings.Count(w.Text, "«")
			other = other || strings.ContainsAny(w.Text, "“„")
		})
	}
	if guillemets < 10 || other {
		return 0
	}
	notes := notesPages(pages, measureBook(pages))
	n := 0
	for i, lp := range pages {
		if lp.Kind != pageNormal || notes[i] {
			continue
		}
		eachWord(lp, func(w *textWord) {
			if t := withoutRemnant(w.Text); t != w.Text {
				w.Text = t
				n++
			}
		})
	}
	return n
}

func withoutRemnant(word string) string {
	m := remnantTail.FindStringSubmatch(word)
	if m == nil {
		m = afterStop.FindStringSubmatch(word)
	}
	if m == nil || strings.ContainsAny(m[1], `”"`) {
		return word
	}
	return m[1] + m[3]
}
