package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestMarkerNumber(t *testing.T) {
	for in, want := range map[string]int{
		".»120":   120,
		";.»36":   36,
		"e.»12O":  120, // an old-style zero read as a letter
		"tider.l": 1,
		"»":       0,
		"kroner":  0,
		"også":    0,
		"lo":      0, // letters, not after punctuation
		"1234":    0,
		"ler.!9":  0, // a figure read as "!"
		"wow!9":   9,
	} {
		if got := markerNumber(in); got != want {
			t.Errorf("markerNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTrimMarker(t *testing.T) {
	for _, c := range []struct {
		word string
		num  int
		want string
	}{
		{"prosent.'", 102, "prosent."},
		{"stabile.?", 40, "stabile."},
		{"over.*%", 260, "over."},
		{"kroner5", 56, "kroner"},
		{"COVID19", 56, "COVID19"}, // figures that are not the number stay
		{"«nei»", 3, "«nei»"},
	} {
		if got := trimMarker(c.word, c.num, "'\"’?!*%”"); got != c.want {
			t.Errorf("trimMarker(%q, %d) = %q, want %q", c.word, c.num, got, c.want)
		}
	}
}

// A notes page as OCR returns it: the numbers in a column of their own,
// some in one block, one unreadable ("$2").
func notesPage() *textPage {
	return &textPage{Width: 2409, Height: 3559, Blocks: []textBlock{
		blockOf(wordsLine("Sluttnoter", 348, 300, 700)),
		blockOf(wordsLine("50", 441, 484, 480), wordsLine("51", 441, 554, 480), wordsLine("$2", 441, 622, 480),
			wordsLine("53", 441, 900, 480)),
		blockOf(wordsLine("Dette er detaljert fremstilt i Gram (2011).", 553, 473, 1500)),
		blockOf(wordsLine("Se Moen og Sivertsen (2007), side 13.", 553, 542, 1500)),
		blockOf(wordsLine("I dette kapittelet vil gevinstene sammenlignes med", 553, 611, 2000),
			wordsLine("verdien av oljefondet.", 553, 680, 1200)),
		blockOf(wordsLine("Historien om hvordan det skjedde, er beskrevet i Fjetland", 553, 889, 2000)),
	}}
}

func TestNotesSection(t *testing.T) {
	st := bookStats{bodyLH: 0.0126}
	pg := withNoteNumbers(notesPage(), st)
	var starts []string
	for _, b := range pg.Blocks {
		for _, l := range b.Lines {
			if n := noteNumber(l); n != 0 {
				starts = append(starts, l.Words[0].Text)
			}
		}
	}
	if strings.Join(starts, " ") != "50 51 "+unknownNote+" 53" {
		t.Fatalf("note numbers placed: %q", starts)
	}

	var paras []para
	for _, b := range pg.Blocks[1:] {
		paras = append(paras, noteParas(b, 0)...)
	}
	nums := numberNotes(paras, func(para) bool { return true })
	var got []string
	for _, p := range paras {
		got = append(got, p.kind+" "+strings.Fields(p.text)[0])
	}
	want := "endnote Dette|endnote Se|endnote I|endnote Historien"
	if strings.Join(got, "|") != want || !nums[52] || paras[2].note != 52 {
		t.Errorf("notes = %q, want %q (numbers %v)", got, want, nums)
	}
}

func TestNumberNotesKeepsOrder(t *testing.T) {
	paras := []para{
		{kind: "endnote", note: 1, text: "a"},
		{kind: "endnote", note: 2, text: "b"},
		{kind: "endnote", note: 70, text: "prosent av alt"}, // a line of a note starting with a figure
		{kind: "endnote", note: 3, text: "c"},
		{kind: "p", text: "unnumbered"},
		{kind: "p", text: "and another"}, // two paragraphs for one missing number: not filled
		{kind: "endnote", note: 5, text: "e"},
	}
	nums := numberNotes(paras, func(para) bool { return true })
	if nums[70] || !nums[3] || nums[4] || paras[2].kind != "p" || paras[2].text != "70 prosent av alt" {
		t.Errorf("numbers %v, paras %+v", nums, paras)
	}
}

func TestKeepRefs(t *testing.T) {
	paras := []para{
		{kind: "p", text: "a" + refMark(1) + " b" + refMark(9) + " c" + refMark(2)},
		{kind: "p", text: "d" + refMark(3) + " e" + refMark(7)}, // 7 has no note
		{kind: "endnote", note: 1, text: "note"},
	}
	kept := keepRefs(paras, map[int]bool{1: true, 2: true, 3: true, 9: true})
	if paras[0].text != "a"+refMark(1)+" b c"+refMark(2) || paras[1].text != "d"+refMark(3)+" e" {
		t.Errorf("texts %q %q", paras[0].text, paras[1].text)
	}
	if !kept[1] || !kept[2] || !kept[3] || kept[9] || kept[7] {
		t.Errorf("kept %v", kept)
	}
}

func TestLinkAcross(t *testing.T) {
	chs := []epubChapter{
		{Body: noteLinks(xmlEscape("Text" + refMark(4) + " & more"))},
		{Body: `<p class="endnote" id="note-4"><a class="noteback" href="#ref-4">4</a> Se Smith.</p>`},
	}
	linkAcross(chs)
	if !strings.Contains(chs[0].Body, `href="chapter002.xhtml#note-4"`) || !strings.Contains(chs[1].Body, `href="chapter001.xhtml#ref-4"`) {
		t.Errorf("bodies:\n%s\n%s", chs[0].Body, chs[1].Body)
	}
	if !strings.Contains(chs[0].Body, "&amp; more") {
		t.Errorf("text not escaped: %s", chs[0].Body)
	}
}

func TestBestReading(t *testing.T) {
	for _, c := range []struct {
		whole, raised, want int
		alts                bool
	}{
		{120, 0, 120, false}, {0, 16, 16, false}, {3, 53, 53, false}, {44, 4, 44, false},
		{12, 12, 12, false}, {0, 0, 0, false}, {34, 14, 0, true},
	} {
		got, alts := bestReading(c.whole, c.raised)
		if got != c.want || (alts != nil) != c.alts {
			t.Errorf("bestReading(%d, %d) = %d, %v", c.whole, c.raised, got, alts)
		}
	}
}

func TestSettleReadings(t *testing.T) {
	sure := []int{12, 0, 16, 22, 0, 24, 69, 0, 76, 0}
	alts := [][]int{nil, {34, 14}, nil, nil, {33, 23}, nil, nil, {71, 73}, nil, {5, 6}}
	settleReadings(sure, alts)
	want := []int{12, 14, 16, 22, 23, 24, 69, 0, 76, 0}
	for i := range want {
		if sure[i] != want[i] {
			t.Fatalf("settled %v, want %v", sure, want)
		}
	}
}

func TestNumberNotesGapsWithSplitNotes(t *testing.T) {
	paras := mergeContinued([]para{
		{kind: "endnote", note: 1, text: "Wicks: «The impact on western Scotland:", page: 1},
		{kind: "p", text: "a Bayesian analysis», 2014.", page: 1}, // the rest of note 1
		{kind: "p", text: "By DeWikiMan, 2008.", page: 1},
		{kind: "endnote", note: 3, text: "Denne formuleringen.", page: 1},
		{kind: "p", text: "Allentoft: «Population Genomics», 2022.", page: 1},
		{kind: "endnote", note: 5, text: "Günther: «Population genomics of", page: 1},
		{kind: "p", text: "Mesolithic Scandinavia», 2018.", page: 1}, // OCR split the note
		{kind: "p", text: "Wikipedia.", page: 1},
		{kind: "note", text: "Databasen i Family Tree DNA.", page: 1},
		{kind: "p", text: "Kart hentet fra nettet.", page: 1},
		{kind: "endnote", note: 9, text: "Cohn, 2019.", page: 1},
	})
	numberNotes(paras, func(para) bool { return true })
	var got []string
	for _, p := range paras {
		got = append(got, strconv.Itoa(p.note)+" "+strings.Fields(p.text)[0])
	}
	want := "1 Wicks:|2 By|3 Denne|4 Allentoft:|5 Günther:|6 Wikipedia.|7 Databasen|8 Kart|9 Cohn,"
	if strings.Join(got, "|") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
}
