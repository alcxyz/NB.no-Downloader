package main

import (
	"strings"
	"testing"
)

// wordsLine lays out a line's words evenly between x0 and x1, each with
// its own box, as OCR reports them.
func wordsLine(text string, x0, y, x1 float64) textLine {
	ws := strings.Fields(text)
	l := textLine{Box: rect{x0, y, x1, y + 25}}
	slot := (x1 - x0) / float64(len(ws))
	for i, w := range ws {
		l.Words = append(l.Words, textWord{Text: w, Conf: 0.95,
			Box: rect{x0 + float64(i)*slot, y, x0 + float64(i+1)*slot - 10, y + 25}})
	}
	return l
}

func blockOf(lines ...textLine) textBlock {
	return textBlock{Lines: lines, Box: boxOf(lines)}
}

func blockTexts(blocks []textBlock) []string {
	var out []string
	for _, b := range blocks {
		if b.Image {
			out = append(out, "[picture]")
			continue
		}
		out = append(out, textOf(b))
	}
	return out
}

// A chart as OCR returns it: the drawing as an empty block, its labels in
// blocks of their own, and an axis value set on the last line of the title.
func TestTextBlocksAssembleChart(t *testing.T) {
	long := "Dette er en lang linje med mange ord i seg som fortsetter"
	body := func(y float64) textBlock {
		return blockOf(wordsLine(long, 350, y, 1960), wordsLine(long, 350, y+78, 1960), wordsLine(long, 350, y+156, 1960))
	}
	title2 := wordsLine("1890-2020, prosent", 570, 700, 980)
	value := wordsLine("20%", 484, 750, 544) // far left of the title's text
	pg := &textPage{Width: 2409, Height: 3559, Blocks: []textBlock{
		body(450),
		blockOf(wordsLine("FIGUR 5", 444, 640, 612)),
		blockOf(wordsLine("Nettoeksport som andel av BNP, gjennomsnitt per tiår,", 573, 650, 1666), title2, value),
		blockOf(wordsLine("15%", 491, 950, 544)),
		{Lines: []textLine{{Box: rect{560, 900, 1900, 1600}}}, Box: rect{560, 900, 1900, 1600}},
		blockOf(wordsLine("10%", 490, 1150, 544)),
		blockOf(wordsLine("1890 1900 1910 1920 1930 1940", 560, 1650, 1900)),
		blockOf(wordsLine("—— Norge - ------- Norge fastland", 900, 1760, 1500)),
		blockOf(wordsLine("Kilder: SSB Nasjonalregnskap", 1400, 1850, 1960)),
		body(2000),
	}}
	st := bookStats{bodyLH: 0.0126}
	got := blockTexts(textBlocks(pg, st))
	want := []string{
		long + " " + long + " " + long,
		"FIGUR 5",
		"Nettoeksport som andel av BNP, gjennomsnitt per tiår, 1890-2020, prosent",
		"[picture]",
		"Kilder: SSB Nasjonalregnskap",
		long + " " + long + " " + long,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, b := range textBlocks(pg, st) {
		if b.Image && (b.Box.X0 > 484 || b.Box.Y0 > 750 || b.Box.Y1 < 1760) {
			t.Errorf("chart %v leaves out its labels", b.Box)
		}
	}
}

// A list of references with its numbers in a column of their own: the
// numbers go, and lines of web addresses read with little confidence stay
// text rather than becoming a chart.
func TestTextBlocksNoteNumbers(t *testing.T) {
	ref := blockOf(wordsLine("Wikipedia: «Bond event», tilgjengelig her", 473, 900, 2000),
		wordsLine("https://en.wikipedia.org/wiki/Bond_event lest 2024", 473, 967, 2000),
		wordsLine("https://www.nature.com/articles/ab12 og mer", 473, 1034, 1900))
	for li := range ref.Lines {
		for wi := range ref.Lines[li].Words {
			ref.Lines[li].Words[wi].Conf = 0.6
		}
	}
	pg := &textPage{Width: 2409, Height: 3559, Blocks: []textBlock{
		blockOf(wordsLine("51", 365, 900, 410)),
		blockOf(wordsLine("52", 365, 1200, 410)),
		blockOf(wordsLine("53", 365, 1500, 410)),
		ref,
		blockOf(wordsLine("Krause, Johannes og Thomas Trappe: A Short History of Humanity,", 473, 1200, 2020)),
		blockOf(wordsLine("Ibid.", 473, 1500, 587)),
	}}
	got := blockTexts(textBlocks(pg, bookStats{bodyLH: 0.0126}))
	want := []string{textOf(ref), "Krause, Johannes og Thomas Trappe: A Short History of Humanity,", "Ibid."}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A legend OCR read as words, between a chart and its source, is part of
// the chart, while its title stays text; so is a scale set under the
// figure's number.
func TestTextBlocksLegendInFrame(t *testing.T) {
	long := "Dette er en lang linje med mange ord i seg som fortsetter"
	body := blockOf(wordsLine(long, 350, 2000, 1960), wordsLine(long, 350, 2078, 1960), wordsLine(long, 350, 2156, 1960))
	pg := &textPage{Width: 2409, Height: 3559, Blocks: []textBlock{
		blockOf(wordsLine("FIGUR 1", 366, 408, 547)),
		blockOf(wordsLine("Budsjettlekkasjen i offentlige utgifter,", 366, 513, 1200),
			wordsLine("rebasert: oktoberbudsjett = O, milliarder kroner", 366, 586, 1400)),
		{Lines: []textLine{{Box: rect{420, 700, 2030, 1700}}}, Box: rect{420, 700, 2030, 1700}},
		blockOf(wordsLine("Statsbudsjett [saldert, des.) M Statsregnskap", 1320, 1750, 2050)),
		blockOf(wordsLine("Kilde: Statsbudsjetter, SSB", 1500, 1850, 2090)),
		body,
	}}
	got := blockTexts(textBlocks(pg, bookStats{bodyLH: 0.0126}))
	want := []string{"FIGUR 1", "Budsjettlekkasjen i offentlige utgifter, rebasert: oktoberbudsjett = O, milliarder kroner",
		"[picture]", "Kilde: Statsbudsjetter, SSB", textOf(body)}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// A scale under the number is split off to join the chart.
	parts := splitCaptions(blockOf(wordsLine("FIGUR 3a", 366, 408, 547), wordsLine("Minimum Medium Maksimum", 900, 470, 1900)))
	if len(parts) != 2 || textOf(parts[0]) != "FIGUR 3a" || textOf(parts[1]) != "Minimum Medium Maksimum" {
		t.Errorf("splitCaptions = %q", blockTexts(parts))
	}
}

// A photo OCR did not see leaves a tall stretch of the column with only
// its labels in it; a section break's space holds its number, which stays.
func TestTextBlocksGapPicture(t *testing.T) {
	long := "Dette er en lang linje med mange ord i seg som fortsetter"
	body := func(y float64) textBlock {
		return blockOf(wordsLine(long, 350, y, 1960), wordsLine(long, 350, y+78, 1960), wordsLine(long, 350, y+156, 1960))
	}
	st := bookStats{bodyLH: 0.0126}
	pg := &textPage{Width: 2409, Height: 3559, Blocks: []textBlock{
		body(400),
		blockOf(wordsLine("EBA", 400, 900, 492)),
		blockOf(wordsLine("LBA", 400, 1400, 486)),
		blockOf(wordsLine("Forskjellen på båttyper mellom eldre bronsealder (øverst) og yngre", 560, 2000, 1960)),
		body(2200),
	}}
	got := blockTexts(textBlocks(pg, st))
	want := []string{textOf(body(400)), "[picture]",
		"Forskjellen på båttyper mellom eldre bronsealder (øverst) og yngre", textOf(body(2200))}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	pg.Blocks = []textBlock{body(400), blockOf(wordsLine("III.", 1100, 800, 1160)), body(1050)}
	got = blockTexts(textBlocks(pg, st))
	want = []string{textOf(body(400)), "III.", textOf(body(1050))}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("section break: got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
