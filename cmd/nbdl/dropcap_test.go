package main

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestFindLostCaps(t *testing.T) {
	long := "Dette er en lang linje med mange ord i seg som fortsetter"
	// "I" missing beside the first two lines, which are indented together.
	lost := blockOf(wordsLine("denne delen av boken skal vi skifte gir. Vi skal", 407, 787, 1966),
		wordsLine("til fremtid. I del 1 og 2 har vi vært gjennom", 406, 867, 1965),
		wordsLine("økonomiske historie. I del 3 skal vi forsøke å se", 348, 943, 1751))
	// An ordinary paragraph: only its first line is indented.
	plain := blockOf(wordsLine("mens historien kan beskrives med fakta, kan ikke", 444, 1023, 1965),
		wordsLine(long, 347, 1100, 1967), wordsLine(long, 347, 1176, 1967))
	pages := []layoutPage{{Text: &textPage{Width: 2409, Height: 3559, Blocks: []textBlock{lost, plain}}}}
	caps := findLostCaps(pages)
	if len(caps) != 1 || caps[0].block != 0 || caps[0].merged {
		t.Fatalf("findLostCaps = %+v", caps)
	}
	if c := caps[0].cap; c.X0 != 348 || c.X1 > 407 || c.Y1 < 867 {
		t.Errorf("cap at %v", c)
	}

	// The cap read with the first word as one tall, doubtful word.
	merged := blockOf(wordsLine("Tønne delen av boken skal vi skifte gir.", 347, 789, 1966),
		wordsLine(long, 347, 867, 1966))
	merged.Lines[0].Words[0].Box.Y1 = 911
	merged.Lines[0].Words[0].Conf = 0.25
	pages[0].Text.Blocks = []textBlock{merged}
	if caps := findLostCaps(pages); len(caps) != 1 || !caps[0].merged {
		t.Fatalf("merged cap: findLostCaps = %+v", caps)
	}
}

func TestCapWords(t *testing.T) {
	vocab := map[string]int{"i": 50, "denne": 30, "dette": 40, "ingen": 9, "delen": 5}
	known := func(s string) bool { return vocab[strings.ToLower(s)] >= minKnown }
	for _, c := range []struct {
		read  []string
		first string
		want  string
	}{
		{[]string{"I", "denne", "delen"}, "denne", "I"},
		{[]string{"Idenne", "delen"}, "denne", "I"},
		{[]string{"1", "denne"}, "denne", "I"},
		{[]string{"denne", "delen"}, "denne", ""}, // nothing read in front
		{[]string{"%", "denne"}, "denne", ""},
	} {
		if got := capLetter(c.read, c.first); got != c.want {
			t.Errorf("capLetter(%q, %q) = %q, want %q", c.read, c.first, got, c.want)
		}
	}

	line := func(first string) textLine { return wordsLine(first+" delen av boken", 400, 100, 1000) }
	for _, c := range []struct{ letter, first, want string }{
		{"I", "denne", "I denne delen av boken"}, // two words the book uses
		{"D", "ette", "Dette delen av boken"},    // one word
		{"I", "ngen", "Ingen delen av boken"},
		{"Q", "zzz", "zzz delen av boken"}, // neither: left alone
	} {
		l := line(c.first)
		putCap(&l, c.letter, rect{340, 90, 395, 200}, known)
		if got := lineText(l); got != c.want {
			t.Errorf("putCap(%q, %q) = %q, want %q", c.letter, c.first, got, c.want)
		}
	}

	for _, c := range []struct {
		read []string
		want string
	}{
		{[]string{"I", "denne", "delen", "av"}, "I denne"},
		{[]string{"Idenne", "delen"}, "I denne"},
		{[]string{"Dette", "delen"}, "Dette"},
		{[]string{"Tønne", "delen"}, ""}, // not a word the book uses
		{[]string{"I", "2001", "delen"}, ""},
	} {
		if got := strings.Join(mergedWords(c.read, "delen", known), " "); got != c.want {
			t.Errorf("mergedWords(%q) = %q, want %q", c.read, got, c.want)
		}
	}
}

// A tall bar for the cap, then short letters on the first line and the
// start of the second line below them.
func TestCapRightEdge(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 300, 140))
	for i := range img.Pix {
		img.Pix[i] = 250
	}
	fill := func(x0, y0, x1, y1 int) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetGray(x, y, color.Gray{Y: 20})
			}
		}
	}
	fill(5, 10, 25, 130)   // the cap, two lines tall
	fill(40, 20, 290, 50)  // the first line
	fill(40, 90, 290, 120) // the second line
	region := rect{0, 0, 300, 140}
	edge, ok := capRightEdge(img, region, rect{0, 10, 290, 130}, rect{40, 20, 290, 50})
	if !ok || edge < 25 || edge > 40 {
		t.Fatalf("capRightEdge = %v, %v; want between 25 and 40", edge, ok)
	}
	out := capLine(img, region, rect{0, 10, edge, 130}, rect{edge, 20, 290, 50})
	if out == nil || out.Bounds().Dy() > 80 {
		t.Fatalf("capLine gave %v", out)
	}
}
