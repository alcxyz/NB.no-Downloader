package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Turning recognised words into flowing text. The rules are heuristics
// tuned on printed books: running heads and page numbers sit in the outer
// tenth of the page and repeat, headings are set larger than body text,
// footnotes smaller at the bottom, and a paragraph continues across a page
// break when it does not end a sentence.

const (
	minTextWords      = 15   // pages with fewer words are shown as images
	headZone          = 0.10 // running heads live in the top or bottom tenth
	headingRatio      = 1.5  // line height that makes a heading
	chapterRatio      = 2.0  // line height that makes a chapter heading
	sinkDepth         = 0.08 // text starting this much lower than usual opens a chapter
	breakGap          = 2.5  // gap between paragraphs, in body lines, that marks a scene break
	noteRatio         = 0.85 // line height of footnotes and captions
	minFigureArea     = 0.06 // smallest illustration worth keeping, of the page
	maxPagesInChapter = 40   // split longer stretches without headings
)

type pageKind int

const (
	pageNormal pageKind = iota
	pageCover           // front cover, shown as the EPUB cover
	pageBack            // back cover, shown as the last page
	pageSkip            // spine and similar
)

// layoutPage is one page as the layout sees it. The caller decides from
// the page image whether a page without text is blank.
type layoutPage struct {
	Text      *textPage // nil when the page has no text source
	Kind      pageKind
	ImageOnly bool // render the whole page as an image
	Blank     bool // leave the page out
}

// figureRef asks the caller for an image: a whole page, or a region of it
// in the page's text units.
type figureRef struct {
	Page   int
	Region rect // zero for the whole page
	Name   string
}

type para struct {
	kind  string // "p", "h1", "h2", "note", "figure", or the markers "chapter" and "break"
	text  string
	image string // figure file name
	page  int
	lh    float64 // median line height relative to the page
}

// bookStats are measured once over the whole book.
type bookStats struct {
	bodyLH float64        // typical body line height, relative to page height
	top    float64        // where text usually starts, relative to page height
	heads  map[string]int // running head text and on how many pages it appears
}

func measureBook(pages []layoutPage) bookStats {
	var heights, tops []float64
	heads := map[string]int{}
	for _, lp := range pages {
		pg := lp.Text
		if pg == nil || pg.Height == 0 || lp.Kind != pageNormal {
			continue
		}
		seen := map[string]bool{}
		first := true
		for _, b := range pg.Blocks {
			if b.Margin || b.Image {
				continue
			}
			for _, l := range b.Lines {
				if key, ok := headLine(l, pg); ok && key != "" && !seen[key] {
					seen[key] = true
					heads[key]++
				}
			}
			body := bodyLines(b, pg)
			if first && len(body) >= 3 {
				tops = append(tops, body[0].Box.Y0/pg.Height)
				first = false
			}
			if len(body) >= 2 {
				for _, l := range body {
					heights = append(heights, l.Box.H()/pg.Height)
				}
			}
		}
	}
	return bookStats{bodyLH: median(heights), top: median(tops), heads: heads}
}

// bodyLines are the lines of a block outside the running head zones.
func bodyLines(b textBlock, pg *textPage) []textLine {
	var out []textLine
	for _, l := range b.Lines {
		if _, ok := headLine(l, pg); !ok {
			out = append(out, l)
		}
	}
	return out
}

// textBlocks returns the blocks of a page worth reading: no margins,
// running heads or page numbers. Text recognised in charts and pictures
// becomes a picture region, and touching regions are joined.
func textBlocks(pg *textPage, st bookStats) []textBlock {
	var out []textBlock
	for _, b := range pg.Blocks {
		if b.Margin {
			continue
		}
		if b.Image {
			out = appendImage(out, b.Box, pg)
			continue
		}
		var kept []textLine
		for _, l := range b.Lines {
			if key, ok := headLine(l, pg); ok && (key == "" || isRoman(key) || st.heads[key] >= 3) {
				continue
			}
			kept = append(kept, l)
		}
		if len(kept) == 0 {
			continue
		}
		if len(kept) < len(b.Lines) {
			b.Lines = kept
			b.Box = rect{}
			for _, l := range kept {
				b.Box = b.Box.union(l.Box)
			}
		}
		if graphic(b) {
			out = appendImage(out, b.Box, pg)
			continue
		}
		out = append(out, b)
	}
	return out
}

// appendImage adds a picture region, joining it to the previous one when
// they touch, as the parts of one chart do.
func appendImage(blocks []textBlock, box rect, pg *textPage) []textBlock {
	if n := len(blocks); n > 0 && blocks[n-1].Image && box.Y0-blocks[n-1].Box.Y1 < pg.Height*0.02 {
		blocks[n-1].Box = blocks[n-1].Box.union(box)
		return blocks
	}
	return append(blocks, textBlock{Box: box, Image: true})
}

// headLine reports whether a line could be a running head or page number:
// short and in the top or bottom tenth of the page. It returns the letters
// the line would be recognised by on other pages.
func headLine(l textLine, pg *textPage) (string, bool) {
	if len(l.Words) == 0 || len(l.Words) > 8 {
		return "", false
	}
	if l.Box.Y1 > pg.Height*headZone && l.Box.Y0 < pg.Height*(1-headZone) {
		return "", false
	}
	var key strings.Builder
	for _, w := range l.Words {
		for _, r := range w.Text {
			if unicode.IsLetter(r) {
				key.WriteRune(unicode.ToLower(r))
			}
		}
	}
	return key.String(), true
}
func isRoman(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("ivxlcdm", r) {
			return false
		}
	}
	return true
}

// graphic spots text recognised in charts and pictures: low confidence,
// few letters, or many short lines of uncertain words.
func graphic(b textBlock) bool {
	var conf float64
	var words, letters, chars int
	for _, l := range b.Lines {
		for _, w := range l.Words {
			words++
			conf += w.Conf
			for _, r := range w.Text {
				chars++
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					letters++
				}
			}
		}
	}
	if words == 0 {
		return true
	}
	avg := conf / float64(words)
	shortLines := len(b.Lines) >= 3 && float64(words)/float64(len(b.Lines)) < 5
	// Chart labels and axes: short and mostly digits and symbols, unlike
	// a chapter number on a line of its own.
	labels := len(b.Lines) <= 2 && letterShare(b) < 0.7 && !(len(b.Lines) == 1 && chapterNumber(lineText(b.Lines[0])) != "")
	return avg < 0.45 || (chars >= 3 && float64(letters)/float64(chars) < 0.5) || (avg < 0.88 && shortLines) || labels
}

// letterShare is the share of letters among the printed characters.
func letterShare(b textBlock) float64 {
	var letters, chars int
	for _, l := range b.Lines {
		for _, w := range l.Words {
			for _, r := range w.Text {
				chars++
				if unicode.IsLetter(r) {
					letters++
				}
			}
		}
	}
	if chars == 0 {
		return 0
	}
	return float64(letters) / float64(chars)
}

// sparsePage spots pages that read better as an image: almost no text, or
// a few short lines as on title and imprint pages.
func sparsePage(blocks []textBlock) bool {
	words, lines := 0, 0
	for _, b := range blocks {
		for _, l := range b.Lines {
			words += len(l.Words)
			lines++
		}
	}
	if words < minTextWords {
		return true
	}
	return words < 80 && lines >= 3 && float64(words)/float64(lines) < 3.5
}

// imprintPage spots the copyright page in the front matter, which reads
// better as an image than as scattered lines of text.
func imprintPage(pg *textPage) bool {
	for _, b := range pg.Blocks {
		for _, l := range b.Lines {
			for _, w := range l.Words {
				if strings.HasPrefix(w.Text, "ISBN") || strings.Contains(w.Text, "©") {
					return true
				}
			}
		}
	}
	return false
}

// hasRealWord reports whether any word has two letters or more and was
// recognised with confidence, unlike specks on an empty page.
func hasRealWord(blocks []textBlock) bool {
	for _, b := range blocks {
		for _, l := range b.Lines {
			for _, w := range l.Words {
				letters := 0
				for _, r := range w.Text {
					if unicode.IsLetter(r) {
						letters++
					}
				}
				if letters >= 2 && w.Conf >= 0.6 {
					return true
				}
			}
		}
	}
	return false
}

func wordCount(blocks []textBlock) int {
	n := 0
	for _, b := range blocks {
		for _, l := range b.Lines {
			n += len(l.Words)
		}
	}
	return n
}

// layoutBook turns pages into chapters and lists the images they use.
func layoutBook(pages []layoutPage) ([]epubChapter, []figureRef) {
	st := measureBook(pages)
	var paras []para
	var figures []figureRef

	for i, lp := range pages {
		if lp.Blank || lp.Kind == pageSkip || lp.Kind == pageCover {
			continue
		}
		if lp.Kind == pageBack || lp.ImageOnly || lp.Text == nil {
			name := fmt.Sprintf("p%04d.jpg", i+1)
			figures = append(figures, figureRef{Page: i, Name: name})
			paras = append(paras, para{kind: "figure", image: name, page: i})
			continue
		}
		pg := lp.Text
		blocks := textBlocks(pg, st)
		col := textColumn(blocks, pg)
		first := true
		var prevText *textBlock
		for bi, b := range blocks {
			if !b.Image {
				switch {
				case prevText == nil && st.top > 0 && b.Box.Y0/pg.Height > st.top+sinkDepth:
					paras = append(paras, para{kind: "chapter", page: i})
				case prevText != nil && st.bodyLH > 0 && b.Box.Y0-prevText.Box.Y1 > breakGap*st.bodyLH*pg.Height:
					paras = append(paras, para{kind: "break", page: i})
				}
				prevText = &blocks[bi]
			}
			if b.Image {
				if b.Box.W()*b.Box.H() < minFigureArea*pg.Width*pg.Height {
					continue
				}
				name := fmt.Sprintf("p%04d-%d.jpg", i+1, len(figures)+1)
				figures = append(figures, figureRef{Page: i, Region: b.Box, Name: name})
				paras = append(paras, para{kind: "figure", image: name, page: i})
				continue
			}
			parts := splitParagraphs(b, col)
			if headingBlock(b, pg, st) {
				parts = [][]textLine{b.Lines}
			}
			for _, lines := range parts {
				p := para{kind: "p", text: joinLines(lines), page: i, lh: medianLineHeight(lines, pg)}
				p.kind = classify(p, lines, pg, col, st, first)
				if p.text == "" {
					continue
				}
				// Headings set on several lines arrive as several short
				// paragraphs; join them.
				if n := len(paras); n > 0 && (p.kind == "h1" || p.kind == "h2") && (paras[n-1].kind == "h1" || paras[n-1].kind == "h2") && paras[n-1].page == i && chapterNumber(paras[n-1].text) == "" && !endsSentence(paras[n-1].text) {
					paras[n-1].text += " " + p.text
					continue
				}
				paras = append(paras, p)
				first = false
			}
		}
	}
	// A heading right next to a picture on the same page is its title.
	for i, p := range paras {
		if p.kind != "h1" && p.kind != "h2" {
			continue
		}
		next := i+1 < len(paras) && paras[i+1].kind == "figure" && paras[i+1].page == p.page
		prev := i > 0 && paras[i-1].kind == "figure" && paras[i-1].page == p.page
		if next || prev {
			paras[i].kind = "note"
		}
	}
	paras = mergeContinued(paras)
	for i := range paras {
		paras[i].text = fixOCR(paras[i].text)
		// A short line without a full stop opening a chapter is its title,
		// even when it is not set larger than the text.
		if i > 0 && paras[i-1].kind == "chapter" && paras[i].kind == "p" &&
			len(strings.Fields(paras[i].text)) <= 6 && !endsSentence(paras[i].text) && i+1 < len(paras) && !startsLower(paras[i+1].text) {
			paras[i].kind = "h1"
		}
	}
	return chapters(paras), figures
}

// fixOCR corrects slips Tesseract makes in Norwegian text: the preposition
// "i" read as "1" between lower-case words, and bullets read as "e".
func fixOCR(text string) string {
	words := strings.Split(text, " ")
	for i := 1; i+1 < len(words); i++ {
		if words[i] == "1" && endsLowerLetter(words[i-1]) && startsLower(words[i+1]) && !countingWords[strings.ToLower(words[i-1])] {
			words[i] = "i"
		}
	}
	if len(words) > 1 && (words[0] == "e" || words[0] == "*") && startsUpper(words[1]) {
		words[0] = "•"
	}
	return strings.Join(words, " ")
}

// countingWords are usually followed by a number, so "kapittel 1 handler"
// keeps its 1.
var countingWords = map[string]bool{
	"kapittel": true, "kap.": true, "side": true, "s.": true, "del": true, "fase": true,
	"prosjekt": true, "tabell": true, "figur": true, "punkt": true, "nummer": true, "nr.": true,
	"steg": true, "trinn": true, "nivå": true, "klasse": true, "versjon": true, "år": true,
	"paragraf": true, "artikkel": true, "ledd": true, "bind": true, "utgave": true, "runde": true,
	"kvartal": true, "alternativ": true, "vedlegg": true, "modell": true, "gruppe": true,
	"plass": true, "time": true, "dag": true, "uke": true,
}

func endsLowerLetter(s string) bool {
	r := []rune(s)
	return len(r) > 0 && unicode.IsLower(r[len(r)-1])
}

func startsUpper(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

func medianLineHeight(lines []textLine, pg *textPage) float64 {
	var hs []float64
	for _, l := range lines {
		hs = append(hs, l.Box.H()/pg.Height)
	}
	return median(hs)
}

func classify(p para, lines []textLine, pg *textPage, col rect, st bookStats, firstOnPage bool) string {
	if chapterNumber(p.text) != "" && len(lines) == 1 && centred(lines[0], col) {
		return "h1"
	}
	if st.bodyLH == 0 {
		return "p"
	}
	ratio := p.lh / st.bodyLH
	words := len(strings.Fields(p.text))
	if ratio >= headingRatio && len(lines) <= 3 && words <= 15 && headingText(p.text, lines) {
		top := lines[0].Box.Y0 < pg.Height*0.45
		if ratio >= chapterRatio || (firstOnPage && top) {
			return "h1"
		}
		return "h2"
	}
	if ratio <= noteRatio && lines[0].Box.Y0 > pg.Height*0.75 {
		return "note"
	}
	return "p"
}

// headingText checks that large text reads like a heading rather than a
// chart label or a speck: mostly letters, a real word, read with
// confidence.
func headingText(text string, lines []textLine) bool {
	if strings.Contains(text, "|") || letterShare(textBlock{Lines: lines}) < 0.8 {
		return false
	}
	var conf float64
	var n int
	longest := 0
	for _, l := range lines {
		for _, w := range l.Words {
			conf += w.Conf
			n++
			letters := 0
			for _, r := range w.Text {
				if unicode.IsLetter(r) {
					letters++
				}
			}
			longest = max(longest, letters)
		}
	}
	return n > 0 && conf/float64(n) >= 0.8 && longest >= 3
}

// headingBlock spots a short block set larger than body text: a heading,
// kept whole even when its centred lines look like separate paragraphs.
func headingBlock(b textBlock, pg *textPage, st bookStats) bool {
	if st.bodyLH == 0 || len(b.Lines) < 2 || len(b.Lines) > 3 || wordCount([]textBlock{b}) > 15 {
		return false
	}
	return medianLineHeight(b.Lines, pg)/st.bodyLH >= headingRatio
}

// chapterNumber recognises a chapter numbered with a lone numeral, such as
// "II." or "12", and returns it with roman numerals OCR read as ones or
// ells put right ("11." becomes "II.").
func chapterNumber(text string) string {
	t := strings.TrimSpace(text)
	if t == "" || len([]rune(t)) > 6 {
		return ""
	}
	core := strings.TrimSuffix(t, ".")
	if core == "" {
		return ""
	}
	digits, romanish := true, true
	for _, r := range core {
		if r < '0' || r > '9' {
			digits = false
		}
		if !strings.ContainsRune("IVXLC1l|", r) {
			romanish = false
		}
	}
	switch {
	case romanish && strings.ContainsAny(core, "IVXLC") || romanish && len(core) > 1 && !strings.ContainsAny(core, "023456789"):
		// "11" and "111" are far more often a misread II and III than
		// chapters eleven and one hundred and eleven.
		fixed := strings.NewReplacer("1", "I", "l", "I", "|", "I").Replace(core)
		if !romanNumeral.MatchString(fixed) {
			// OCR also reads a final I as L: "VIL" for "VII".
			fixed = strings.ReplaceAll(fixed, "L", "I")
		}
		if !romanNumeral.MatchString(fixed) {
			return ""
		}
		return fixed + strings.TrimPrefix(t, core)
	case digits:
		return t
	}
	return ""
}

// textColumn is the horizontal extent of the page's body text. Scans often
// include space beside the page, so horizontal measures are taken against
// the column rather than the page.
func textColumn(blocks []textBlock, pg *textPage) rect {
	var col rect
	for _, b := range blocks {
		if !b.Image && len(b.Lines) >= 3 {
			col = col.union(b.Box)
		}
	}
	if col.W() <= 0 {
		return rect{0, 0, pg.Width, pg.Height}
	}
	return col
}

var romanNumeral = regexp.MustCompile(`^M{0,3}(CM|CD|D?C{0,3})(XC|XL|L?X{0,3})(IX|IV|V?I{0,3})$`)

// centred reports whether a short line sits in the middle of the column.
func centred(l textLine, col rect) bool {
	mid := (l.Box.X0 + l.Box.X1) / 2
	centre := (col.X0 + col.X1) / 2
	return col.W() > 0 && mid > centre-col.W()*0.15 && mid < centre+col.W()*0.15 && l.Box.W() < col.W()*0.3
}

// splitParagraphs splits a block where a line is indented or the line
// before it ends short with a full stop.
func splitParagraphs(b textBlock, col rect) [][]textLine {
	if len(b.Lines) < 2 {
		return [][]textLine{b.Lines}
	}
	lefts := make([]float64, len(b.Lines))
	right := 0.0
	for i, l := range b.Lines {
		lefts[i] = l.Box.X0
		right = max(right, l.Box.X1)
	}
	base := mode(lefts, col.W()*0.01)
	indent := col.W() * 0.025
	var out [][]textLine
	start := 0
	for i := 1; i < len(b.Lines); i++ {
		off := b.Lines[i].Box.X0 - base
		indented := off > indent && off < col.W()*0.25
		prev := b.Lines[i-1]
		short := prev.Box.X1 < right-col.W()*0.1 && endsSentence(lineText(prev))
		if indented || short {
			out = append(out, b.Lines[start:i])
			start = i
		}
	}
	return append(out, b.Lines[start:])
}

// mode returns the most common value after rounding to step.
func mode(vals []float64, step float64) float64 {
	if step <= 0 {
		step = 1
	}
	counts := map[int]int{}
	best, bestN := 0, 0
	for _, v := range vals {
		k := int(v / step)
		counts[k]++
		if counts[k] > bestN || (counts[k] == bestN && k < best) {
			best, bestN = k, counts[k]
		}
	}
	return float64(best) * step
}

func lineText(l textLine) string {
	var parts []string
	for _, w := range l.Words {
		parts = append(parts, w.Text)
	}
	return strings.Join(parts, " ")
}

// joinLines joins the words of lines into one paragraph, rejoining words
// hyphenated at line ends.
func joinLines(lines []textLine) string {
	var b strings.Builder
	for _, l := range lines {
		for _, w := range l.Words {
			text := w.Text
			if w.HypPart2 {
				continue
			}
			if w.HypFull != "" {
				text = w.HypFull
			}
			appendWord(&b, text)
		}
	}
	return b.String()
}

// appendWord adds a word, joining it to a trailing line-end hyphen when it
// continues the word.
func appendWord(b *strings.Builder, word string) {
	s := b.String()
	if s == "" {
		b.WriteString(word)
		return
	}
	if strings.HasSuffix(s, "-") || strings.HasSuffix(s, "¬") {
		lastSpace := strings.LastIndex(s, " ")
		prefix := strings.TrimRight(s[lastSpace+1:], "-¬")
		if prefix != "" && startsLower(word) {
			trimmed := strings.TrimRight(s, "-¬")
			b.Reset()
			b.WriteString(trimmed)
			if keepHyphen(prefix) {
				b.WriteString("-")
			}
			b.WriteString(word)
			return
		}
	}
	b.WriteString(" ")
	b.WriteString(word)
}

// keepHyphen keeps hyphens that belong to the word, as in "EU-landene" or
// "1990-tallet".
func keepHyphen(prefix string) bool {
	for i, r := range prefix {
		if unicode.IsDigit(r) || (i > 0 && unicode.IsUpper(r)) {
			return true
		}
	}
	return false
}

func startsLower(s string) bool {
	for _, r := range s {
		return unicode.IsLower(r)
	}
	return false
}

func endsSentence(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	last := []rune(s)[len([]rune(s))-1]
	return strings.ContainsRune(".!?:;»\"”)…", last)
}

// mergeContinued joins paragraphs split by a page break or a block
// boundary: the first does not end a sentence and the next starts in
// lower case.
func mergeContinued(paras []para) []para {
	var out []para
	for _, p := range paras {
		if n := len(out); n > 0 && p.kind == "p" {
			// Look past figures and footnotes, which sit between the halves
			// of a paragraph broken across pages.
			j := n - 1
			for j > 0 && (out[j].kind == "figure" || out[j].kind == "note") && out[j].page < p.page {
				j--
			}
			prev := &out[j]
			continues := !endsSentence(prev.text) && startsLower(p.text)
			if (prev.kind == "h1" || prev.kind == "h2") && j == n-1 && continues {
				// A heading does not run on into lower case; this was a
				// line of body text set a little large.
				prev.kind = "p"
			}
			if prev.kind == "p" && continues {
				var b strings.Builder
				b.WriteString(prev.text)
				appendWord(&b, p.text)
				prev.text = b.String()
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// chapters renders paragraphs into XHTML chapters. A chapter starts at a
// chapter heading or where text sinks lower on the page; long stretches
// without either are split at page boundaries.
func chapters(paras []para) []epubChapter {
	var out []epubChapter
	var body strings.Builder
	title := ""
	startPage := -1
	afterHeading := true
	flush := func() {
		if strings.TrimSpace(body.String()) != "" {
			out = append(out, epubChapter{Title: title, Body: body.String()})
		}
		body.Reset()
		title = ""
		afterHeading = true
	}
	for i, p := range paras {
		switch {
		case p.kind == "chapter":
			flush()
			title = chapterTitle(paras[i+1:])
			startPage = p.page
			continue
		case p.kind == "h1" && (title == "" || body.Len() > 0):
			flush()
			title = chapterTitle(paras[i:])
			startPage = p.page
		case startPage >= 0 && p.page-startPage >= maxPagesInChapter && i > 0 && paras[i-1].page < p.page && p.kind == "p" && !startsLower(p.text):
			flush()
			startPage = p.page
		}
		if startPage < 0 {
			startPage = p.page
		}
		switch p.kind {
		case "h1", "h2":
			text := p.text
			if n := chapterNumber(text); n != "" {
				text = n
			}
			fmt.Fprintf(&body, "<%s>%s</%s>\n", p.kind, xmlEscape(text), p.kind)
			afterHeading = true
		case "break":
			if body.Len() > 0 {
				body.WriteString("<hr class=\"break\"/>\n")
				afterHeading = true
			}
		case "note":
			fmt.Fprintf(&body, "<p class=\"note\">%s</p>\n", xmlEscape(p.text))
		case "figure":
			fmt.Fprintf(&body, "<figure class=\"%s\"><img src=\"images/%s\" alt=\"\"/></figure>\n", figureClass(p.image), p.image)
			afterHeading = true
		default:
			if afterHeading {
				fmt.Fprintf(&body, "<p class=\"noindent\">%s</p>\n", xmlEscape(p.text))
			} else {
				fmt.Fprintf(&body, "<p>%s</p>\n", xmlEscape(p.text))
			}
			afterHeading = false
		}
	}
	flush()
	return out
}

// chapterTitle names a chapter after its heading, or else after its first
// words, which is how the table of contents finds it again.
func chapterTitle(rest []para) string {
	for _, p := range rest {
		switch p.kind {
		case "h1", "h2":
			if n := chapterNumber(p.text); n != "" {
				return strings.TrimSuffix(n, ".") + ". " + chapterTitle(rest[1:])
			}
			return p.text
		case "p":
			words := strings.Fields(p.text)
			if len(words) > 7 {
				return strings.Join(words[:7], " ") + " …"
			}
			return p.text
		case "chapter":
			return ""
		}
	}
	return ""
}

func figureClass(name string) string {
	if strings.Contains(name, "-") {
		return "figure"
	}
	return "pageimage"
}

func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	return s[len(s)/2]
}
