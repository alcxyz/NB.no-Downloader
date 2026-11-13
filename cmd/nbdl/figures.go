package main

import (
	"sort"
	"strings"
	"unicode"
)

// Charts reach the OCR as many pieces: the drawing, which it reads as
// empty or garbled blocks, and the labels printed on and around it (axis
// values, category names, legends, rotated axis titles), which it reads as
// small blocks of their own, interleaved with the rest of the page. A
// chart is put back together by growing a region from the drawing over the
// labels near it, stopping at captions and running text, which stay text.

const (
	figureReach   = 4.5  // how far, in body lines, a label may sit from its chart
	minGrowArea   = 0.02 // a drawing must cover this much of the page to gather labels
	figurePadding = 0.4  // margin around a chart's crop, in body lines
)

// What each block of a page may become: a label that can join a chart,
// the seed of a chart, or text that stays text and that charts do not grow
// past.
const (
	roleLabel = iota
	roleSeed
	roleText
)

// roleOf decides what a block may become in a chart.
func roleOf(b textBlock, col rect) int {
	switch {
	case b.Image:
		return roleSeed
	case caption(b) || bodyText(b, col):
		return roleText
	case drawing(b) && !plainText(b) && !textLines(b, col):
		return roleSeed
	case prose(b, col):
		return roleText
	}
	return roleLabel
}

// minGapPicture is the height, of the page, of an empty stretch between
// text that is taken for a picture OCR did not see.
const minGapPicture = 0.1

// gapPictures adds a picture block for each tall stretch of the column
// between two pieces of text that holds no text of its own, only labels
// or nothing: a photograph or map OCR did not recognise ("EBA" and "LBA"
// over two photos of boats). Stretches that turn out to be empty paper are
// dropped when the picture is fetched.
func gapPictures(blocks []textBlock, pg *textPage) []textBlock {
	col := textColumn(blocks, pg)
	// The text, top to bottom: OCR does not always list it in that order.
	var text []rect
	for _, b := range blocks {
		if !b.Image && !gapFiller(b, col) {
			text = append(text, b.Box)
		}
	}
	sort.Slice(text, func(i, j int) bool { return text[i].Y0 < text[j].Y0 })
	var gaps []rect
	for i := 1; i < len(text); i++ {
		bottom := text[0].Y1
		for _, r := range text[:i] {
			bottom = max(bottom, r.Y1)
		}
		if text[i].Y0-bottom >= minGapPicture*pg.Height {
			gaps = append(gaps, rect{col.X0, bottom, col.X1, text[i].Y0})
		}
	}
	if len(gaps) == 0 {
		return blocks
	}
	// Each picture goes before the first block below it.
	var out []textBlock
	for _, b := range blocks {
		for k := 0; k < len(gaps); k++ {
			if b.Box.Y0 >= gaps[k].Y1 && !b.Image && !gapFiller(b, col) {
				out = append(out, textBlock{Box: gaps[k], Image: true})
				gaps = append(gaps[:k], gaps[k+1:]...)
				k--
			}
		}
		out = append(out, b)
	}
	for _, g := range gaps {
		out = append(out, textBlock{Box: g, Image: true})
	}
	return out
}

// gapFiller reports whether a block may sit in a picture OCR did not see:
// a drawing it tried to read, or short labels ("EBA", "Øysunde").
func gapFiller(b textBlock, col rect) bool {
	if roleOf(b, col) == roleSeed {
		return true
	}
	words := 0 // on the fullest line
	for _, l := range b.Lines {
		words = max(words, len(l.Words))
	}
	// A chapter's or section's number ("III.") is text, alone in its space.
	if len(b.Lines) == 1 && chapterNumber(lineText(b.Lines[0])) != "" {
		return false
	}
	return roleOf(b, col) == roleLabel && words <= 3 && b.Box.W() < col.W()/2
}

// captionWords open the lines that number a figure or name its source.
var captionWords = map[string]bool{
	"figur": true, "figure": true, "fig": true, "tabell": true, "table": true,
	"kilde": true, "kilder": true, "source": true, "sources": true,
}

// splitCaptions separates a figure's number and title, or its source, from
// a chart OCR read as part of the same block: the caption lines are text,
// the rest a chart.
func splitCaptions(b textBlock) []textBlock {
	part := func(ls []textLine) textBlock { return textBlock{Lines: ls, Box: boxOf(ls)} }
	if len(b.Lines) <= 4 {
		// A chart's scale or legend, or a table's column heads, set
		// right under its number ("FIGUR 3a" over "Minimum Medium
		// Maksimum").
		if len(b.Lines) > 1 && figureNumber(b) {
			n := len(b.Lines)
			for n > 1 {
				lb := part(b.Lines[n-1 : n])
				if !legendLike(lb) && letterShare(lb) > 0 && farGap(b.Lines[n-1]) == 0 {
					break
				}
				n--
			}
			if n < len(b.Lines) {
				return []textBlock{part(b.Lines[:n]), part(b.Lines[n:])}
			}
		}
		return []textBlock{b}
	}
	opens := func(l textLine) bool {
		return caption(textBlock{Lines: []textLine{l}})
	}
	titleLine := func(l textLine) bool {
		var conf float64
		for _, w := range l.Words {
			conf += w.Conf
		}
		tb := textBlock{Lines: []textLine{l}}
		return len(l.Words) >= 3 && letterShare(tb) >= 0.6 && conf/float64(len(l.Words)) >= doubtConf
	}
	var out []textBlock
	lines := b.Lines
	if opens(lines[0]) {
		n := 1
		for n < len(lines)-1 && titleLine(lines[n]) {
			n++
		}
		out = append(out, part(lines[:n]))
		lines = lines[n:]
	}
	var source []textLine
	for k := max(1, len(lines)-3); k < len(lines); k++ {
		if opens(lines[k]) {
			source = lines[k:]
			lines = lines[:k]
			break
		}
	}
	out = append(out, part(lines))
	if source != nil {
		out = append(out, part(source))
	}
	return out
}

// strayLabel reports whether a block left outside any chart is still a
// chart's label, to be dropped: short, mostly figures, and without a word
// read with confidence ("4%", "1900 1910 1920", "-10 %"). A column of
// figures with no letters at all goes too, such as the years of a chart
// or the numbers of a list of notes.
func strayLabel(b textBlock) bool {
	if len(b.Lines) == 1 && chapterNumber(lineText(b.Lines[0])) != "" {
		return false
	}
	if letterShare(b) == 0 {
		return true
	}
	if len(b.Lines) > 2 || letterShare(b) >= 0.7 {
		return false
	}
	// A short sentence of figures is text, such as a reference
	// ("Se NOU 2002: 9 (2003)."); labels do not end in a full stop.
	if t := textOf(b); len(strings.Fields(t)) >= 3 && strings.HasSuffix(strings.TrimSpace(t), ".") {
		return false
	}
	for _, l := range b.Lines {
		for _, w := range l.Words {
			letters := 0
			for _, r := range w.Text {
				if unicode.IsLetter(r) {
					letters++
				}
			}
			if letters >= 4 && w.Conf >= sureConf {
				return false
			}
		}
	}
	return true
}

// caption reports whether a block numbers a figure or table or names its
// source ("FIGUR 2", "Kilde: SSB").
func caption(b textBlock) bool {
	if len(b.Lines) == 0 || len(b.Lines[0].Words) == 0 {
		return false
	}
	first := strings.ToLower(strings.TrimRight(b.Lines[0].Words[0].Text, ".:"))
	return captionWords[first]
}

// prose reports whether a block reads as running text or a figure title:
// mostly letters, with a line of several words spanning much of the column.
func prose(b textBlock, col rect) bool {
	if b.Image || letterShare(b) < 0.7 || legendLike(b) || (drawing(b) && !plainText(b)) {
		return false
	}
	// A block of axis values and legend lines OCR read together.
	chart := 0
	for _, l := range b.Lines {
		lb := textBlock{Lines: []textLine{l}}
		if letterShare(lb) == 0 || legendLike(lb) {
			chart++
		}
	}
	if len(b.Lines) >= 3 && 2*chart >= len(b.Lines) {
		return false
	}
	// Words set in columns on every line: a table's heads.
	spread := 0
	for _, l := range b.Lines {
		if farGap(l) > 0 {
			spread++
		}
	}
	if spread == len(b.Lines) {
		return false
	}
	for _, l := range b.Lines {
		if len(l.Words) >= 5 && l.Box.W() >= col.W()*0.4 && l.Box.W() > l.Box.H() {
			return true
		}
	}
	return false
}

// bodyText reports whether a block is running text by its layout alone:
// three or more lines of several words across most of the column, spaced
// as words are. Text in a chart may be as wide, but its words sit far
// apart, by the bars and lines between them. Layout tells text even where
// OCR is unsure of it, as of web addresses in a list of references.
func bodyText(b textBlock, col rect) bool {
	full := 0
	for _, l := range b.Lines {
		if len(l.Words) >= 5 && l.Box.W() >= col.W()*0.6 && farGap(l) == 0 {
			full++
		}
	}
	return full >= 3 || full >= 2 && full == len(b.Lines)
}

// textLines reports whether most lines of a block run across the column
// with words spaced as in text, as uncertain lines of web addresses do;
// a drawing's text is narrow or spread out.
func textLines(b textBlock, col rect) bool {
	n := 0
	for _, l := range b.Lines {
		if len(l.Words) > 0 && l.Box.W() >= col.W()*0.6 && farGap(l) == 0 {
			n++
		}
	}
	return n > 0 && 2*n >= len(b.Lines)
}

// plainText reports whether a block is text read with confidence, such as
// a short reference ("Se Pritchett (2024).") or a column of note numbers,
// rather than a drawing OCR tried to read: short blocks like these look
// like chart labels, but only a drawing starts a chart.
func plainText(b textBlock) bool {
	var conf float64
	words := 0
	for _, l := range b.Lines {
		for _, w := range l.Words {
			if strings.TrimSpace(w.Text) != "" {
				conf += w.Conf
				words++
			}
		}
	}
	return words > 0 && conf/float64(words) >= sureConf
}

// legendLike reports whether a short block is a chart legend: series names
// beside the line samples OCR reads as dashes, quotes or equals signs
// ("—— Fastlands-Norge - ------- Danmark"), or a row of names, mostly
// capitalised, without the commas and small words of a title ("Offentlig
// Privat konsentrert Privat distribuert Stiftelse").
func legendLike(b textBlock) bool {
	if len(b.Lines) > 2 {
		return false
	}
	capitals, words := 0, 0
	for _, l := range b.Lines {
		t := lineText(l)
		if endsSentence(t) {
			return false
		}
		for _, w := range l.Words {
			words++
			if startsUpper(w.Text) {
				capitals++
			}
		}
	}
	if capitals >= 3 && float64(capitals) >= 0.6*float64(words) && !strings.Contains(textOf(b), ",") {
		return true
	}
	// The boxes before a legend's entries, read as letters ("M 2024
	// virkelig M Stabil andel"); "I" and "A" may be words.
	boxes := 0
	for _, l := range b.Lines {
		for _, w := range l.Words {
			if r := []rune(w.Text); len(r) == 1 && unicode.IsUpper(r[0]) && !strings.ContainsRune("IAÅ", r[0]) {
				boxes++
			}
		}
	}
	if boxes >= 2 {
		return true
	}
	for _, l := range b.Lines {
		for _, w := range l.Words {
			// A lone "=" belongs to titles ("indeks: 2020 = 100").
			if w.Text != "=" && strings.Trim(w.Text, "-—–=\"“”'+o•*") == "" {
				return true
			}
			if lineSample(w.Text) {
				return true
			}
		}
	}
	return false
}

// lineSample reports whether a word holds a legend's line sample as OCR
// reads it: runs of dashes, equals signs or quotes, or an arrow
// ("------", "—-Norgefastland", "—>Rentedifferanse"). Dashes between
// figures are a range ("1984—-2013").
func lineSample(w string) bool {
	if strings.IndexAny(w, "0123456789") >= 0 {
		return false
	}
	for _, run := range []string{"--", "——", "—-", "-—", "==", "\"\""} {
		if strings.Contains(w, run) {
			return true
		}
	}
	return strings.HasPrefix(w, "—>") || strings.HasPrefix(w, "->")
}

// splitLabels separates chart labels that OCR set on the first or last
// line of a neighbouring block: an axis value under a chart's title
// ("1890-2020, prosent" over "20%"), or one at the end of the title's line
// ("prosent 40 %"), far to the right of the words before it.
func splitLabels(b textBlock) []textBlock {
	if len(b.Lines) == 0 {
		return []textBlock{b}
	}
	var before, after []textBlock
	label := func(ws []textWord) bool {
		if len(ws) == 0 || len(ws) > 3 {
			return false
		}
		for _, w := range ws {
			if hasLetter(w.Text) {
				return false
			}
		}
		return true
	}
	// A line of figures that is not a label lines up with the text it
	// belongs to, as the years ending a title do ("1870-2013").
	apart := func(l, other textLine) bool {
		return strings.Contains(lineText(l), "%") || abs(l.Box.X0-other.Box.X0) > 2*other.Box.H() ||
			l.Box.H() < 0.7*other.Box.H()
	}
	lines := b.Lines
	for len(lines) > 1 && label(lines[0].Words) && apart(lines[0], lines[1]) {
		before = append(before, textBlock{Lines: lines[:1], Box: lines[0].Box})
		lines = lines[1:]
	}
	for len(lines) > 1 && label(lines[len(lines)-1].Words) && apart(lines[len(lines)-1], lines[len(lines)-2]) {
		l := lines[len(lines)-1]
		after = append([]textBlock{{Lines: []textLine{l}, Box: l.Box}}, after...)
		lines = lines[:len(lines)-1]
	}
	// A label far to the right on the last line.
	last := lines[len(lines)-1]
	if cut := farGap(last); cut > 0 && label(last.Words[cut:]) {
		head := textLine{Words: last.Words[:cut]}
		tail := textLine{Words: last.Words[cut:]}
		head.Box, tail.Box = wordsBox(head.Words), wordsBox(tail.Words)
		lines = append(append([]textLine{}, lines[:len(lines)-1]...), head)
		after = append([]textBlock{{Lines: []textLine{tail}, Box: tail.Box}}, after...)
	}
	if len(before) == 0 && len(after) == 0 {
		return []textBlock{b}
	}
	b.Lines = lines
	b.Box = boxOf(lines)
	out := append(before, b)
	return append(out, after...)
}

// farGap returns the index of the first word after a gap far wider than
// any space between words, or 0.
// tableLike reports whether most lines of a block are rows of a table:
// a label and, far to its right, a figure.
func tableLike(b textBlock) bool {
	rows := 0
	for _, l := range b.Lines {
		if farGap(l) > 0 && strings.ContainsAny(lineText(l), "0123456789") {
			rows++
		}
	}
	return len(b.Lines) >= 3 && 2*rows >= len(b.Lines)
}

func farGap(l textLine) int {
	h := l.Box.H()
	for i := 1; i < len(l.Words); i++ {
		if l.Words[i].Box.X0-l.Words[i-1].Box.X1 > 3*h {
			return i
		}
	}
	return 0
}

func wordsBox(ws []textWord) rect {
	var r rect
	for _, w := range ws {
		r = r.union(w.Box)
	}
	return r
}

// assembleFigures replaces each chart's pieces with one picture block
// covering them, placed where its first piece was.
func assembleFigures(blocks []textBlock, pg *textPage, st bookStats) []textBlock {
	col := textColumn(blocks, pg)
	lh := st.bodyLH * pg.Height
	if lh <= 0 {
		lh = pg.Height * 0.012
	}
	reach := figureReach * lh

	role := make([]int, len(blocks))
	for i, b := range blocks {
		role[i] = roleOf(b, col)
	}

	// Regions are named by their first seed; alive marks those not merged
	// into another.
	owner := make([]int, len(blocks)) // the block's region, or -1
	box := make([]rect, len(blocks))
	alive := make([]bool, len(blocks))
	for i := range blocks {
		owner[i] = -1
		if role[i] == roleSeed {
			owner[i], box[i], alive[i] = i, blocks[i].Box, true
		}
	}
	// A chart framed by a figure number and title above and its source
	// below is everything between them that is not text, even where OCR
	// found no drawing, only labels.
	frame := map[int]rect{} // a framed chart's region fills its frame
	for _, fb := range figureBands(blocks, role, col, lh) {
		band := fb.blocks
		first := band[0]
		frame[first] = fb.frame
		for _, k := range band {
			if owner[k] >= 0 && owner[k] != first {
				alive[owner[k]] = false
			}
			owner[k] = first
			box[first] = box[first].union(blocks[k].Box)
		}
		alive[first] = true
		for k := range owner {
			if owner[k] >= 0 && !alive[owner[k]] {
				owner[k] = first
			}
		}
	}
	// blocked reports whether text stands between two boxes, one above the
	// other: a chart does not reach past its caption or the running text.
	blocked := func(a, c rect) bool {
		top, bottom := a, c
		if c.Y0 < a.Y0 {
			top, bottom = c, a
		}
		for k, b := range blocks {
			if role[k] != roleText {
				continue
			}
			if b.Box.Y0 >= top.Y1-1 && b.Box.Y1 <= bottom.Y0+1 &&
				b.Box.X0 < max(a.X1, c.X1) && b.Box.X1 > min(a.X0, c.X0) {
				return true
			}
		}
		return false
	}
	merge := func(into, from int) {
		for k := range owner {
			if owner[k] == from {
				owner[k] = into
			}
		}
		box[into] = box[into].union(box[from])
		alive[from] = false
	}
	for changed := true; changed; {
		changed = false
		// Pieces of a drawing with no text between them are one chart.
	pairs:
		for r := range box {
			for s := r + 1; s < len(box); s++ {
				if alive[r] && alive[s] && gap(box[r], box[s]) <= reach && !blocked(box[r], box[s]) {
					merge(r, s)
					changed = true
					break pairs
				}
			}
		}
		if changed {
			continue
		}
		for i, b := range blocks {
			if owner[i] >= 0 || role[i] == roleText {
				continue
			}
			for r, rb := range box {
				if !alive[r] || rb.W()*rb.H() < minGrowArea*pg.Width*pg.Height {
					continue
				}
				if (gap(rb, b.Box) <= reach && !blocked(rb, b.Box)) || inside(b.Box, rb) {
					owner[i] = r
					box[r] = rb.union(b.Box)
					changed = true
					break
				}
			}
		}
		// Text inside a chart is part of it: an axis title, a note in the
		// plot, or a table OCR found the picture of.
		for i, b := range blocks {
			if owner[i] < 0 && role[i] == roleText && !caption(b) {
				for r, rb := range box {
					if alive[r] && (len(b.Lines) <= 2 && inside(b.Box, rb) || within(b.Box, rb, lh/2)) {
						owner[i] = r
						changed = true
						break
					}
				}
			}
		}
	}

	numbering := numberColumn(blocks, lh)
	kept := make([]bool, len(blocks)) // blocks that stay text
	for i, b := range blocks {
		switch {
		case owner[i] >= 0:
		case numbering[i]:
			// The numbers of a list of notes, set in a column of their own.
		case role[i] == roleLabel && strayLabel(b):
			// A label of a chart whose drawing OCR did not find.
		default:
			kept[i] = true
		}
	}
	pad := figurePadding * lh
	var out []textBlock
	for i, b := range blocks {
		r := owner[i]
		switch {
		case kept[i]:
			out = append(out, b)
		case r >= 0 && firstOwned(owner, r) == i:
			rb := box[r]
			if f, ok := frame[r]; ok {
				rb = rb.union(f)
			}
			rb = rect{max(0, rb.X0-pad), max(0, rb.Y0-pad), min(pg.Width, rb.X1+pad), min(pg.Height, rb.Y1+pad)}
			chart := box[r]
			if f, ok := frame[r]; ok {
				chart = chart.union(f)
			}
			out = append(out, textBlock{Box: clearOfText(rb, chart, blocks, kept, lh), Image: true})
		}
	}
	// OCR sometimes reads a picture before the caption printed above it.
	for i := 0; i < len(out); i++ {
		if !out[i].Image {
			continue
		}
		j := i
		for j+1 < len(out) && !out[j+1].Image && out[j+1].Box.Y1 <= out[i].Box.Y0+lh {
			j++
		}
		if j > i {
			pic := out[i]
			copy(out[i:j], out[i+1:j+1])
			out[j] = pic
			i = j
		}
	}
	return out
}

// clearOfText moves a picture's top and bottom edges off the text kept
// above and below it, such as the caption and the source line, so the
// picture shows no slivers of lines that are also in the text. An edge is
// moved only as far as the chart itself allows.
func clearOfText(rb, chart rect, blocks []textBlock, kept []bool, lh float64) rect {
	mid := (chart.Y0 + chart.Y1) / 2
	for i, b := range blocks {
		if !kept[i] || b.Image {
			continue
		}
		overlap := min(b.Box.X1, rb.X1) - max(b.Box.X0, rb.X0)
		if overlap < b.Box.W()/2 || b.Box.Y1 <= rb.Y0 || b.Box.Y0 >= rb.Y1 {
			continue
		}
		// A small margin off the text, but not into the chart unless the
		// text itself reaches into it.
		switch {
		case b.Box.Y1 <= mid && b.Box.Y1 < chart.Y0+chart.H()/4:
			y := b.Box.Y1 + lh/4
			if b.Box.Y1 <= chart.Y0 {
				y = min(y, chart.Y0)
			}
			rb.Y0 = max(rb.Y0, y)
		case b.Box.Y0 >= mid && b.Box.Y0 > chart.Y1-chart.H()/4:
			y := b.Box.Y0 - lh/4
			if b.Box.Y0 >= chart.Y1 {
				y = max(y, chart.Y1)
			}
			rb.Y1 = min(rb.Y1, y)
		}
	}
	return rb
}

// numberColumn marks lone numbers stacked at one left edge, three or more
// to a page: the numbers of notes or references printed beside them.
func numberColumn(blocks []textBlock, lh float64) []bool {
	lone := func(b textBlock) bool {
		return len(b.Lines) == 1 && len(b.Lines[0].Words) == 1 && !b.Image &&
			strings.Trim(b.Lines[0].Words[0].Text, "0123456789.") == ""
	}
	marked := make([]bool, len(blocks))
	for _, b := range blocks {
		if !lone(b) {
			continue
		}
		var same []int
		for j, c := range blocks {
			if lone(c) && abs(c.Box.X0-b.Box.X0) < lh {
				same = append(same, j)
			}
		}
		if len(same) >= 3 {
			for _, j := range same {
				marked[j] = true
			}
		}
	}
	return marked
}

// figureBands returns, for each figure numbered above ("FIGUR 31") with
// its source named below ("Kilder: ..."), the blocks between its title and
// its source that are not text, in order.
// figureBand is the blocks of a chart framed by its number and title above
// and its source below, and the frame: the column's width between them.
type figureBand struct {
	blocks []int
	frame  rect
}

func figureBands(blocks []textBlock, role []int, col rect, lh float64) []figureBand {
	var bands []figureBand
	for i, b := range blocks {
		// The number on a short line of its own, not a sentence of the
		// text that names a figure ("Figur 24 viser ...").
		if len(b.Lines) > 2 || !figureNumber(b) || len(b.Lines[0].Words) > 3 {
			continue
		}
		// The title: text right below the number.
		top := b.Box.Y1
		for j := i + 1; j < len(blocks) && role[j] == roleText && !caption(blocks[j]) && !legendLike(blocks[j]) && !tableLike(blocks[j]) && blocks[j].Box.Y0-top < 3*lh; j++ {
			top = blocks[j].Box.Y1
		}
		// The chart ends at its source or, without one, at the running
		// text that follows.
		bottom := -1.0
		width := col // a source line set flush with the chart's right edge widens it
		for j := i + 1; j < len(blocks); j++ {
			if (caption(blocks[j]) || bodyText(blocks[j], col)) && blocks[j].Box.Y0 > top {
				bottom = blocks[j].Box.Y0
				width = width.union(blocks[j].Box)
				break
			}
		}
		toEnd := bottom < 0
		if toEnd {
			// A chart filling the rest of the page.
			bottom = col.Y1 + lh
		}
		// Inside the frame, short text is part of the chart too: a
		// legend OCR read as words ("Statsbudsjett M Statsregnskap").
		var band []int
		for j, c := range blocks {
			// So is a table's text, which is not prose.
			legend := role[j] == roleText && !caption(c) && !figureNumber(c) && !bodyText(c, col) &&
				(len(c.Lines) <= 2 || !prose(c, col) || tableLike(c))
			if (role[j] != roleText || legend) && c.Box.Y0 >= top && c.Box.Y1 <= bottom {
				band = append(band, j)
			}
		}
		drawn := false
		for _, j := range band {
			drawn = drawn || role[j] != roleText
		}
		if len(band) > 0 && (drawn || !toEnd) {
			bands = append(bands, figureBand{band, rect{width.X0, top + lh/4, width.X1, bottom - lh/4}})
		}
	}
	return bands
}

// figureNumber reports whether a block numbers a figure or table
// ("FIGUR 31", "TABELL 4").
func figureNumber(b textBlock) bool {
	if len(b.Lines) == 0 || len(b.Lines[0].Words) < 2 {
		return false
	}
	first := strings.ToLower(strings.TrimRight(b.Lines[0].Words[0].Text, ".:"))
	numbered := first == "figur" || first == "figure" || first == "fig" || first == "tabell" || first == "table"
	return numbered && strings.IndexAny(b.Lines[0].Words[1].Text, "0123456789") >= 0
}

func firstOwned(owner []int, r int) int {
	for i, o := range owner {
		if o == r {
			return i
		}
	}
	return -1
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// gap is the distance between two boxes, zero when they overlap.
func gap(a, b rect) float64 {
	dx := max(0, max(a.X0, b.X0)-min(a.X1, b.X1))
	dy := max(0, max(a.Y0, b.Y0)-min(a.Y1, b.Y1))
	return max(dx, dy)
}

// within reports whether a lies wholly within b.
func within(a, b rect, slack float64) bool {
	return a.X0 >= b.X0-slack && a.Y0 >= b.Y0-slack && a.X1 <= b.X1+slack && a.Y1 <= b.Y1+slack
}

func textOf(b textBlock) string {
	var parts []string
	for _, l := range b.Lines {
		parts = append(parts, lineText(l))
	}
	return strings.Join(parts, " ")
}

// inside reports whether the middle of a lies within b.
func inside(a, b rect) bool {
	x, y := (a.X0+a.X1)/2, (a.Y0+a.Y1)/2
	return x > b.X0 && x < b.X1 && y > b.Y0 && y < b.Y1
}
