package main

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Correcting OCR slips against the book's own vocabulary. A book repeats
// most of its words many times, and OCR reads them right with confidence
// almost every time. A word read with low confidence that the book hardly
// knows is corrected when a typical OCR confusion turns it into a word the
// book uses often. Using the book rather than a dictionary keeps its
// spelling, old or new, and works for any language.

const (
	sureConf    = 0.9  // words read at least this well build the vocabulary
	doubtConf   = 0.85 // words read worse than this may be corrected
	minKnown    = 3    // a correction must be a word the book uses this often
	maxRareSeen = 1    // a doubtful word seen this often with confidence stays
)

// ocrConfusions are character pairs OCR mixes up, tried in both directions.
var ocrConfusions = [][2]string{
	{"o", "ø"}, {"a", "å"}, {"e", "æ"}, {"O", "Ø"}, {"A", "Å"}, {"E", "Æ"},
	{"rn", "m"}, {"cl", "d"}, {"li", "h"},
	{"1", "l"}, {"1", "i"}, {"l", "i"}, {"I", "l"}, {"0", "o"},
	{"c", "e"}, {"n", "u"}, {"h", "b"}, {"v", "y"},
}

// strayMarks are marks OCR makes of specks; full stops and commas are left
// alone because abbreviations and numbers need them.
const strayMarks = "!|]»«;*^~[{}"

// correctionStats counts what a correction pass did, for reporting.
type correctionStats struct {
	doubtful, corrected, markers int
}

// correctWords fixes doubtful words in place across all pages.
func correctWords(pages []layoutPage) correctionStats {
	vocab := bookVocabulary(pages)
	var st correctionStats
	for _, lp := range pages {
		eachWord(lp, func(w *textWord) {
			if t := stripNoteMarker(w.Text); t != w.Text {
				w.Text = t
				st.markers++
			}
			if w.HypPart2 || w.HypFull != "" {
				return
			}
			pre, core, post := splitWordParts(w.Text)
			// A word ending in a hyphen is half of one broken across lines.
			if utf8.RuneCountInString(core) < 3 || !hasLetter(core) || vocab[strings.ToLower(core)] > maxRareSeen ||
				strings.HasPrefix(post, "-") || strings.HasPrefix(post, "¬") {
				return
			}
			lower := strings.ToLower(core)
			// Words read with confidence are left alone, even unknown ones:
			// splitting them would cut real words into small words ("i",
			// "er", "en", "et") that Norwegian is full of.
			if w.Conf >= doubtConf {
				return
			}
			st.doubtful++
			if fixed := bestCandidate(core, vocab); fixed != "" {
				w.Text = pre + fixed + post
				st.corrected++
			} else if parts := splitRunTogether(lower, vocab); parts != nil {
				w.Text = pre + matchCase(strings.Join(parts, " "), core, lower) + post
				st.corrected++
			}
		})
	}
	return st
}

func eachWord(lp layoutPage, fn func(w *textWord)) {
	if lp.Text == nil {
		return
	}
	for bi := range lp.Text.Blocks {
		b := &lp.Text.Blocks[bi]
		if b.Margin || b.Image {
			continue
		}
		for li := range b.Lines {
			for wi := range b.Lines[li].Words {
				fn(&b.Lines[li].Words[wi])
			}
		}
	}
}

// bestCandidate returns the most used known word one confusion away from
// core, matching its capitalisation, or "" when there is none.
func bestCandidate(core string, vocab map[string]int) string {
	lower := strings.ToLower(core)
	best, bestN := "", minKnown-1
	try := func(c string) {
		if n := vocab[strings.ToLower(c)]; n > bestN && c != core {
			best, bestN = c, n
		}
	}
	for _, pair := range ocrConfusions {
		for _, dir := range [][2]string{{pair[0], pair[1]}, {pair[1], pair[0]}} {
			for i := 0; ; {
				j := strings.Index(core[i:], dir[0])
				if j < 0 {
					break
				}
				j += i
				try(core[:j] + dir[1] + core[j+len(dir[0]):])
				i = j + len(dir[0])
			}
		}
	}
	// A speck read as a mark inside a word: "eksempe!l", "energ;i".
	for i, r := range core {
		if strings.ContainsRune(strayMarks, r) {
			try(core[:i] + core[i+utf8.RuneLen(r):])
		}
	}
	if best == "" {
		return ""
	}
	return matchCase(best, core, lower)
}

// matchCase gives the candidate the original's capitalisation.
func matchCase(cand, orig, origLower string) string {
	if orig == origLower {
		return strings.ToLower(cand)
	}
	if orig == strings.ToUpper(orig) {
		return strings.ToUpper(cand)
	}
	r, size := utf8.DecodeRuneInString(cand)
	return string(unicode.ToUpper(r)) + strings.ToLower(cand[size:])
}

// splitWordParts splits off the punctuation around a word.
func splitWordParts(s string) (pre, core, post string) {
	start := strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
	if start < 0 {
		return s, "", ""
	}
	end := strings.LastIndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
	_, size := utf8.DecodeRuneInString(s[end:])
	return s[:start], s[start : end+size], s[end+size:]
}

func hasLetter(s string) bool {
	return strings.IndexFunc(s, unicode.IsLetter) >= 0
}

// functionWords are the small words that fill running text in Norwegian
// and English; a lost space between them is far likelier than a compound
// made of them.
var functionWords = map[string]bool{
	"i": true, "og": true, "er": true, "en": true, "et": true, "ei": true, "på": true, "til": true,
	"det": true, "den": true, "de": true, "at": true, "å": true, "som": true, "har": true, "hadde": true,
	"var": true, "jeg": true, "vi": true, "du": true, "han": true, "hun": true, "for": true, "med": true,
	"av": true, "om": true, "ikke": true, "kan": true, "skal": true, "skulle": true, "vil": true, "må": true,
	"få": true, "seg": true, "sin": true, "ble": true, "blir": true, "men": true, "så": true, "da": true,
	"når": true, "fra": true, "ut": true, "opp": true, "inn": true, "the": true, "of": true, "and": true,
	"to": true, "in": true, "is": true, "was": true, "that": true,
}

// stripNoteMarker removes footnote references OCR turns into junk at the
// end of a word: asterisks ("tynne*", "fallende.**") and superscript digits
// after punctuation ("tider.**9", "forfektet.3*"). A reader cannot follow
// them anyway once the notes are separated from the page.
func stripNoteMarker(s string) string {
	t := strings.TrimRight(s, "*")
	if t != s && t != "" {
		return stripNoteMarker(t)
	}
	end := len(s)
	for end > 0 && (s[end-1] >= '0' && s[end-1] <= '9' || s[end-1] == '*') {
		end--
	}
	if end < len(s) && end > 1 && strings.ContainsRune(".,;:!?»\"”)", rune(s[end-1])) {
		// Keep years, amounts and abbreviations: the part before the
		// punctuation must be a word of three letters or more ("1960."
		// and "nr.3" stay).
		word := strings.TrimRight(s[:end], ".,;:!?»\"”)")
		letters := 0
		for _, r := range word {
			if unicode.IsLetter(r) {
				letters++
			} else {
				letters = 0
			}
		}
		if letters >= 3 {
			return s[:end]
		}
	}
	return s
}

// splitRunTogether splits words whose spaces OCR lost, using the book's
// vocabulary. Norwegian writes compounds as one word, so only two safe
// patterns are split: a word followed by "i", "å" or "og" ("kroneri"), and
// runs of four words or more ("hartilhørtensværtliten").
func splitRunTogether(word string, vocab map[string]int) []string {
	known := func(w string) bool { return vocab[w] >= minKnown }
	for _, tail := range []string{"i", "å", "og"} {
		if head := strings.TrimSuffix(word, tail); head != word && utf8.RuneCountInString(head) >= 5 && known(head) && known(tail) {
			return []string{head, tail}
		}
	}
	return splitRunOn(word, vocab)
}

// splitRunOn splits a run of four or more everyday words whose spaces
// were lost ("hartilhørtensværtliten").
func splitRunOn(word string, vocab map[string]int) []string {
	known := func(w string) bool { return vocab[w] >= minKnown }
	piece := func(w string) bool {
		return known(w) && (utf8.RuneCountInString(w) >= 2 || w == "i" || w == "å")
	}
	// Fewest pieces, each a known word, by dynamic programming over byte
	// positions that start a rune.
	n := len(word)
	if n > 60 {
		return nil
	}
	best := make([][]string, n+1)
	best[0] = []string{}
	for i := 0; i < n; i++ {
		if best[i] == nil || !utf8.RuneStart(word[i]) {
			continue
		}
		for j := i + 1; j <= n; j++ {
			if j < n && !utf8.RuneStart(word[j]) {
				continue
			}
			p := word[i:j]
			if !piece(p) {
				continue
			}
			if best[j] == nil || len(best[i])+1 < len(best[j]) {
				best[j] = append(append([]string{}, best[i]...), p)
			}
		}
	}
	// Running text, not a long word cut into fragments: every piece is a
	// common word in the book, and everyday small words are among them.
	parts := best[n]
	if len(parts) < 4 || utf8.RuneCountInString(word)/len(parts) > 5 {
		return nil
	}
	small := 0
	for _, p := range parts {
		if vocab[p] < 5 {
			return nil
		}
		if functionWords[p] {
			small++
		}
	}
	if small < 2 {
		return nil
	}
	return parts
}
