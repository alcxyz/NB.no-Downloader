package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Turning recognised words into flowing text. The rules are heuristics
// tuned on printed books: running heads and page numbers sit in the outer
// tenth of the page and repeat, headings are set larger than body text,
// footnotes smaller at the bottom, and a paragraph continues across a page
// break when it does not end a sentence.

const (
	minTextWords      = 15   // pages with fewer words are shown as images
	headZone          = 0.12 // running heads live in the top or bottom eighth
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
	Title     bool // a page holding only a chapter or part title
}

// figureRef asks the caller for an image: a whole page, or a region of it
// in the page's text units.
type figureRef struct {
	Page   int
	Region rect // zero for the whole page
	Name   string
}

type para struct {
	kind   string // "p", "h1", "h2", "note", "figure", "endnote", or the markers "chapter" and "break"
	text   string
	image  string // figure file name
	page   int
	lh     float64 // median line height relative to the page
	note   int     // an endnote's number
	linked bool    // an endnote the text links to
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
					heights = append(heights, lineHeight(l)/pg.Height)
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
// running heads or page numbers. Charts and pictures, with the labels
// printed in and around them, become picture regions.
func textBlocks(pg *textPage, st bookStats) []textBlock {
	var out []textBlock
	for _, b := range pg.Blocks {
		if b.Margin {
			continue
		}
		if b.Image {
			out = append(out, b)
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
			b.Box = boxOf(kept)
		}
		for _, part := range splitCaptions(b) {
			out = append(out, splitLabels(part)...)
		}
	}
	return assembleFigures(gapPictures(dropPageNumber(out, pg), pg), pg, st)
}

func boxOf(lines []textLine) rect {
	var r rect
	for _, l := range lines {
		r = r.union(l.Box)
	}
	return r
}

// dropPageNumber removes a lone page number left first or last on a page,
// in its top or bottom fifth, just outside the running head zones.
func dropPageNumber(blocks []textBlock, pg *textPage) []textBlock {
	lone := func(b textBlock) bool {
		if b.Image || len(b.Lines) != 1 || len(b.Lines[0].Words) != 1 {
			return false
		}
		if b.Box.Y0 > pg.Height*0.2 && b.Box.Y1 < pg.Height*0.8 {
			return false
		}
		t := strings.Trim(b.Lines[0].Words[0].Text, "-–— ")
		return t != "" && len(t) <= 3 && strings.Trim(t, "0123456789") == ""
	}
	if n := len(blocks); n > 0 && lone(blocks[n-1]) {
		blocks = blocks[:n-1]
	}
	if len(blocks) > 0 && lone(blocks[0]) {
		blocks = blocks[1:]
	}
	return blocks
}

// headLine reports whether a line could be a running head or page number:
// short and in the top or bottom tenth of the page. It returns the letters
// the line would be recognised by on other pages.
func headLine(l textLine, pg *textPage) (string, bool) {
	if len(l.Words) == 0 || len(l.Words) > 12 {
		return "", false
	}
	if l.Box.Y1 > pg.Height*headZone && l.Box.Y0 < pg.Height*(1-headZone) {
		return "", false
	}
	// Only real words make the key: OCR junk next to a page number
	// differs from page to page.
	var key strings.Builder
	for _, w := range l.Words {
		var letters strings.Builder
		for _, r := range w.Text {
			if unicode.IsLetter(r) {
				letters.WriteRune(unicode.ToLower(r))
			}
		}
		if utf8.RuneCountInString(letters.String()) >= 3 {
			key.WriteString(letters.String())
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

// drawing spots blocks where OCR read a chart or picture rather than text:
// no words, low confidence, few letters, or many short lines of uncertain
// words. Short labels are not drawings, though they may belong to one.
func drawing(b textBlock) bool {
	var conf float64
	var words, letters, chars int
	for _, l := range b.Lines {
		for _, w := range l.Words {
			// OCR reports drawn areas as words with no text.
			if strings.TrimSpace(w.Text) == "" {
				continue
			}
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
	return avg < 0.45 || (chars >= 3 && float64(letters)/float64(chars) < 0.5) || (avg < 0.88 && shortLines)
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

// markPictures decides which pages to show as images: pages with little
// text, such as title and picture pages, and the imprint. It leaves out
// the printed table of contents, whose page numbers mean nothing in an
// EPUB. Pages with no word read with confidence may be blank; their
// indexes are returned for a look at the page image.
func markPictures(pages []layoutPage, textPages []int) []int {
	st := measureBook(pages)
	// Sparse pages before the first full page of text are front matter
	// (half-title, title page) and stay images; later ones holding a short
	// title in capitals open a chapter or part.
	firstBody := -1
	for _, i := range textPages {
		if pg := pages[i].Text; pg != nil && wordCount(textBlocks(pg, st)) >= 80 {
			firstBody = i
			break
		}
	}
	var check []int
	for _, i := range textPages {
		pg := pages[i].Text
		if pg != nil && i < 15 && contentsPage(textBlocks(pg, st)) {
			pages[i].Blank = true
			continue
		}
		if pg == nil || sparsePage(textBlocks(pg, st)) || (i < 12 && imprintPage(pg)) {
			blocks := []textBlock(nil)
			if pg != nil {
				blocks = textBlocks(pg, st)
			}
			if firstBody >= 0 && i > firstBody && titlePage(blocks, pg, st) {
				pages[i].Title = true
				continue
			}
			if pg != nil && hasRealWord(blocks) {
				pages[i].ImageOnly = true
				continue
			}
			check = append(check, i)
		}
	}
	return check
}

// titlePage spots a page holding only a short title: a few words in
// capitals, perhaps under a part or chapter number.
func titlePage(blocks []textBlock, pg *textPage, st bookStats) bool {
	text := titleText(blocks)
	words := strings.Fields(text)
	if len(words) == 0 || len(words) > 15 || !hasRealWord(blocks) {
		return false
	}
	if n := chapterNumber(words[0]); n != "" {
		text = strings.Join(words[1:], " ")
	}
	if text == "" {
		return false
	}
	var lines []textLine
	for _, b := range blocks {
		lines = append(lines, b.Lines...)
	}
	large := st.bodyLH > 0 && len(lines) > 0 && medianLineHeight(lines, pg) >= st.bodyLH*headingRatio
	return mostlyCapitals(text) || large
}

// titleText joins the lines of a title page, putting a leading part or
// chapter number right ("111 LAVRANS" becomes "III. LAVRANS").
func titleText(blocks []textBlock) string {
	var lines []string
	for _, b := range blocks {
		for _, l := range b.Lines {
			if t := strings.TrimSpace(joinLines([]textLine{l})); t != "" {
				lines = append(lines, t)
			}
		}
	}
	if len(lines) > 1 {
		if n := chapterNumber(lines[0]); n != "" {
			lines[0] = strings.TrimSuffix(n, ".") + "."
		}
	}
	return strings.Join(lines, " ")
}

// contentsPage spots the printed table of contents by its title.
func contentsPage(blocks []textBlock) bool {
	for _, b := range blocks {
		if b.Image || len(b.Lines) == 0 {
			continue
		}
		switch strings.ToLower(strings.Trim(lineText(b.Lines[0]), " .:")) {
		case "innhold", "innholdsfortegnelse", "innhald", "contents", "table of contents":
			return true
		}
		return false
	}
	return false
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
	notes := notesPages(pages, st)
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
		if notes[i] {
			pg = withNoteNumbers(pg, st)
		}
		blocks := textBlocks(pg, st)
		if lp.Title {
			paras = append(paras, para{kind: "chapter", page: i}, para{kind: "h1", text: titleText(blocks), page: i})
			continue
		}
		col := textColumn(blocks, pg)
		first := true
		var prev *textBlock // the block above, text or picture
		for bi, b := range blocks {
			if !b.Image {
				switch {
				case prev == nil && st.top > 0 && b.Box.Y0/pg.Height > st.top+sinkDepth && hasRealWord([]textBlock{b}):
					paras = append(paras, para{kind: "chapter", page: i})
				case prev != nil && !prev.Image && !caption(*prev) && !caption(b) && st.bodyLH > 0 &&
					b.Box.Y0-prev.Box.Y1 > breakGap*st.bodyLH*pg.Height && len(paras) > 0 && paras[len(paras)-1].kind == "p":
					// Only a gap between two stretches of body text is a
					// scene break; space around pictures and their
					// captions and after headings is layout.
					paras = append(paras, para{kind: "break", page: i})
				}
			}
			prev = &blocks[bi]
			if b.Image {
				if b.Box.W()*b.Box.H() < minFigureArea*pg.Width*pg.Height {
					continue
				}
				name := fmt.Sprintf("p%04d-%d.jpg", i+1, len(figures)+1)
				figures = append(figures, figureRef{Page: i, Region: b.Box, Name: name})
				paras = append(paras, para{kind: "figure", image: name, page: i})
				continue
			}
			if notes[i] && numberedLines(&textPage{Blocks: []textBlock{b}}) > 0 {
				paras = append(paras, noteParas(b, i)...)
				first = false
				continue
			}
			fixDropCap(b.Lines)
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
	noteNums := numberNotes(paras, func(p para) bool { return notes[p.page] })
	for i := range paras {
		paras[i].text = fixOCR(paras[i].text)
	}
	linked := keepRefs(paras, noteNums)
	for i := range paras {
		paras[i].linked = paras[i].kind == "endnote" && linked[paras[i].note]
	}
	paras = chapterTitles(paras, st)
	return chapters(paras), figures
}

// chapterTitles settles headings where a chapter opens. Short lines
// without a full stop at the top of a new chapter make up its title, even
// when set only a little larger than the text or in capitals over several
// lines. A lone number is a chapter number only when text follows it on
// its page; otherwise it was a chart label or a note number.
func chapterTitles(paras []para, st bookStats) []para {
	var out []para
	for i := 0; i < len(paras); i++ {
		p := paras[i]
		if (p.kind == "h1" || p.kind == "h2") && chapterNumber(p.text) != "" {
			next := i + 1
			if next >= len(paras) || paras[next].page != p.page || paras[next].kind != "p" || !startsUpper(paras[next].text) {
				p.kind = "p"
			}
		}
		out = append(out, p)
		if p.kind != "chapter" {
			continue
		}
		var title []string
		j := i + 1
		for ; j < len(paras) && len(title) < 4; j++ {
			q := paras[j]
			if q.page != p.page || (q.kind != "p" && q.kind != "h1" && q.kind != "h2") ||
				len(strings.Fields(q.text)) > 8 || endsSentence(q.text) || chapterNumber(q.text) != "" {
				break
			}
			if !mostlyCapitals(q.text) && q.lh < st.bodyLH*1.15 && len(title) > 0 {
				break
			}
			title = append(title, q.text)
		}
		if len(title) > 0 && (j >= len(paras) || !startsLower(paras[j].text)) {
			out = append(out, para{kind: "h1", text: strings.Join(title, " "), page: p.page, lh: paras[i+1].lh})
			i = j - 1
		}
	}
	return out
}

func mostlyCapitals(s string) bool {
	upper, letters := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	return letters > 0 && upper*10 >= letters*8
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
	r := []rune(stripRefs(s))
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
		hs = append(hs, lineHeight(l)/pg.Height)
	}
	return median(hs)
}

// lineHeight is the typical height of a line's words, so that one tall
// drop cap does not make a line of text look like a heading.
func lineHeight(l textLine) float64 {
	var hs []float64
	for _, w := range l.Words {
		if w.Box.H() > 0 {
			hs = append(hs, w.Box.H())
		}
	}
	if len(hs) == 0 {
		return l.Box.H()
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

// fixDropCap mends the large initial that opens many chapters. OCR reads
// a drop-cap "I" before a year as "1" ("12001" for "I 2001") and often
// splits a drop-cap letter from the rest of its word ("D et").
func fixDropCap(lines []textLine) {
	if len(lines) == 0 || len(lines[0].Words) < 3 {
		return
	}
	words := lines[0].Words
	var rest []float64
	for _, w := range words[1:] {
		rest = append(rest, w.Box.H())
	}
	if words[0].Box.H() < median(rest)*1.5 {
		return
	}
	first := words[0].Text
	if len(first) <= 2 && strings.Trim(first, "I1l|") == "" {
		words[0].Text = "I" // "I1 1869" for "I 1869"
		return
	}
	if len(first) >= 2 && strings.ContainsRune("1l|", rune(first[0])) && strings.Trim(first[1:], "0123456789") == "" {
		words[0].Text = "I " + first[1:]
		return
	}
	if r := []rune(first); len(r) == 1 && unicode.IsUpper(r[0]) && startsLower(words[1].Text) {
		words[1].Text = first + words[1].Text
		words[1].Box = words[0].Box.union(words[1].Box)
		lines[0].Words = words[1:]
	}
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
	case digits && len(core) <= 2:
		// Longer numbers are years and other figures, as on chart axes.
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
			if w.Note > 0 {
				b.WriteString(refMark(w.Note))
			}
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
	s = strings.TrimSpace(stripRefs(s))
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
		// A gap followed by lower case was a page or column turn, not a
		// scene break.
		if n := len(out); n > 0 && p.kind == "p" && out[n-1].kind == "break" && startsLower(p.text) {
			out = out[:n-1]
		}
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
			if (prev.kind == "p" || prev.kind == "endnote") && continues {
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
	hasText := false // a chapter holding only headings so far stays open
	flush := func() {
		if strings.TrimSpace(body.String()) != "" {
			out = append(out, epubChapter{Title: title, Body: body.String()})
		}
		body.Reset()
		title = ""
		afterHeading = true
		hasText = false
	}
	for i, p := range paras {
		switch {
		case p.kind == "chapter":
			if hasText || title == "" {
				flush()
				title = chapterTitle(paras[i+1:])
			}
			startPage = p.page
			continue
		case p.kind == "h1" && (title == "" || hasText):
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
			text := stripRefs(p.text)
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
			fmt.Fprintf(&body, "<p class=\"note\">%s</p>\n", noteLinks(xmlEscape(p.text)))
			hasText = true
		case "endnote":
			num := strconv.Itoa(p.note)
			if p.linked {
				num = `<a class="noteback" href="#ref-` + num + `">` + num + `</a>`
			}
			fmt.Fprintf(&body, "<p class=\"endnote\" id=\"note-%d\">%s %s</p>\n", p.note, num, xmlEscape(p.text))
			afterHeading = false
			hasText = true
		case "figure":
			fmt.Fprintf(&body, "<figure class=\"%s\"><img src=\"images/%s\" alt=\"\"/></figure>\n", figureClass(p.image), p.image)
			afterHeading = true
			hasText = true
		default:
			if afterHeading {
				fmt.Fprintf(&body, "<p class=\"noindent\">%s</p>\n", noteLinks(xmlEscape(p.text)))
			} else {
				fmt.Fprintf(&body, "<p>%s</p>\n", noteLinks(xmlEscape(p.text)))
			}
			afterHeading = false
			hasText = true
		}
	}
	flush()
	return out
}

// chapterTitle names a chapter after its heading, or else after its first
// words, which is how the table of contents finds it again.
func chapterTitle(rest []para) string {
	for _, p := range rest {
		p.text = stripRefs(p.text)
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
