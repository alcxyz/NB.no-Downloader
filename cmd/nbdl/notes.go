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
	_ = parallel(ctx, len(jobs), workers, func(done, total int) { report("Reading note numbers", done, total) },
		func(ctx context.Context, j int) error {
			pg := pages[jobs[j].page].Text
			w := jobs[j].word
			h := w.Box.H()
			// The raised number ends the word, in its upper part.
			region := rect{max(0, w.Box.X1-1.8*h), max(0, w.Box.Y0-0.3*h), min(pg.Width, w.Box.X1+0.3*h), w.Box.Y0 + 0.7*h}
			key := sha256.Sum256([]byte(fmt.Sprintf("%d %v", jobs[j].page, region)))
			data, err := d.cachedImage(ctx, filepath.Join(dir, fmt.Sprintf("ref-%x.jpg", key[:8])),
				figureURL(book.Pages[jobs[j].page], pg, region, 0))
			if err != nil {
				return nil
			}
			img, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil {
				return nil
			}
			path := filepath.Join(dir, fmt.Sprintf("ref-%04d-%d.jpg", jobs[j].page+1, j))
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, enlarge(toGray(img), 4, 60), &jpeg.Options{Quality: 95}); err != nil {
				return nil
			}
			if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
				return nil
			}
			defer os.Remove(path)
			engine, err := engines.get(ctx)
			if err != nil {
				return err
			}
			tsv, err := engine.recognizeLine(ctx, path)
			if err != nil {
				return nil
			}
			pgRead, err := parseTSV(tsv)
			if err != nil {
				return nil
			}
			var text strings.Builder
			for _, b := range pgRead.Blocks {
				for _, l := range b.Lines {
					text.WriteString(lineText(l))
				}
			}
			read[j] = markerNumber(text.String())
			return nil
		})
	// What OCR made of a marker it did not read as a number may be left
	// at the end of the word as a quote mark ("prosent.'"). A curly
	// closing quote is such a remnant only in a book that never opens one,
	// quoting with « » instead.
	remnants := "'\"’?!*%"
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
	before, _ := utf8.DecodeLastRuneInString(s[:start])
	if !strings.ContainsAny(run, "0123456789") && !strings.ContainsRune(".,;:»”\")!?*", before) {
		return 0
	}
	n, err := strconv.Atoi(strings.NewReplacer("o", "0", "O", "0", "l", "1", "I", "1", "|", "1").Replace(run))
	if err != nil || n <= 0 || n > 999 {
		return 0
	}
	return n
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
	var idx []int
	for i, p := range paras {
		if inNotes(p) && (p.kind == "endnote" || p.kind == "p") {
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
		if p.note == prev+1 || p.note == prev+1+pending ||
			p.note > prev && next == p.note+1 && (prev == 0 || p.note <= prev+15) {
			prev, pending = p.note, 0
			continue
		}
		pending++
		p.kind = "p"
		p.text = strconv.Itoa(p.note) + " " + p.text
		p.note = 0
	}
	// Fill gaps whose size matches.
	last := 0
	var gap []int
	for _, i := range idx {
		p := &paras[i]
		if p.kind == "p" {
			gap = append(gap, i)
			continue
		}
		if len(gap) == p.note-last-1 {
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
