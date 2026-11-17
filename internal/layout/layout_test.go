package layout

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"strings"
	"testing"

	"github.com/alcxyz/NB.no-Downloader/internal/ocr"
)

func TestJoinLinesHyphens(t *testing.T) {
	line := func(words ...string) ocr.Line {
		var l ocr.Line
		for _, w := range words {
			l.Words = append(l.Words, ocr.Word{Text: w, Conf: 1})
		}
		return l
	}
	tests := []struct {
		lines []ocr.Line
		want  string
	}{
		{[]ocr.Line{line("sammen-"), line("hengene", "er")}, "sammenhengene er"},
		{[]ocr.Line{line("EU-"), line("landene")}, "EU-landene"},
		{[]ocr.Line{line("1990-"), line("tallet")}, "1990-tallet"},
		{[]ocr.Line{line("Oslo-"), line("Bergen")}, "Oslo- Bergen"},
		{[]ocr.Line{line("Skole-", "og"), line("barnehage")}, "Skole- og barnehage"},
		{[]ocr.Line{line("kapital-"), line("og", "arbeid")}, "kapital- og arbeid"},
	}
	for _, tt := range tests {
		if got := joinLines(tt.lines); got != tt.want {
			t.Errorf("joinLines = %q, want %q", got, tt.want)
		}
	}
}

func TestChapterNumber(t *testing.T) {
	for in, want := range map[string]string{
		"II.": "II.", "11.": "II.", "111": "III", "VIL": "VII", "IV.": "IV.", "12": "12", "1": "1",
		"Hei": "", "VIL.x": "", "": "", "IIII": "",
	} {
		if got := chapterNumber(in); got != want {
			t.Errorf("chapterNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFixOCR(t *testing.T) {
	for in, want := range map[string]string{
		"inn 1 sitt arbeid":         "inn i sitt arbeid",
		"det var 1 1990":            "det var 1 1990",
		"e Sykelønnsordningen er":   "• Sykelønnsordningen er",
		"e-post er nyttig":          "e-post er nyttig",
		"kapittel 1 handler om alt": "kapittel 1 handler om alt",
	} {
		if got := fixOCR(in); got != want {
			t.Errorf("fixOCR(%q) = %q, want %q", in, got, want)
		}
	}
}

// synthPage lays out paragraphs as an ALTO-like page: a running head and a
// page number in the top tenth, then text lines of equal height.
func synthPage(head string, paras ...[]string) *ocr.Page {
	pg := &ocr.Page{Width: 1000, Height: 1500}
	if head != "" {
		pg.Blocks = append(pg.Blocks, ocr.Block{Lines: []ocr.Line{mkLine(head, 100, 60, 900)}})
	}
	y := 180.0
	for _, p := range paras {
		var b ocr.Block
		for i, l := range p {
			x0 := 100.0
			if i == 0 {
				x0 = 130 // first line indented
			}
			line := mkLine(l, x0, y, 900)
			if strings.HasPrefix(l, "^") { // centred heading line
				line = mkLine(strings.TrimPrefix(l, "^"), 480, y, 520)
			}
			b.Lines = append(b.Lines, line)
			b.Box = b.Box.Union(line.Box)
			y += 30
		}
		pg.Blocks = append(pg.Blocks, b)
	}
	return pg
}

func mkLine(text string, x0, y, x1 float64) ocr.Line {
	l := ocr.Line{Box: ocr.Rect{X0: x0, Y0: y, X1: x1, Y1: y + 25}}
	for _, w := range strings.Fields(text) {
		l.Words = append(l.Words, ocr.Word{Text: w, Box: l.Box, Conf: 0.97})
	}
	return l
}

func TestLayoutBook(t *testing.T) {
	long := "Dette er en lang linje med mange ord i seg"
	pages := []Page{
		{Kind: Cover},
		{Text: synthPage("12 Jørundgård",
			[]string{long, long, long + "."},
			[]string{"^II."},
			[]string{"Det var en vakker morgen og hun", long, "gikk ut på tunet og"})},
		{Text: synthPage("13 Jørundgård",
			[]string{"så solen skinne over dalen.", long, long + "."},
			[]string{long, long, long, "Slutt."})},
		{Text: synthPage("14 Jørundgård", []string{long, long, long + "."})},
		{Kind: Skip},
	}
	chs, figs := Book(pages)
	if len(figs) != 0 {
		t.Errorf("figures = %+v", figs)
	}
	if len(chs) != 2 {
		t.Fatalf("got %d chapters, want 2: %+v", len(chs), chs)
	}
	if !strings.HasPrefix(chs[1].Title, "II. Det var en vakker morgen") {
		t.Errorf("chapter title = %q", chs[1].Title)
	}
	body := chs[1].Body
	if strings.Contains(body, "Jørundgård") {
		t.Error("running head left in the text")
	}
	if !strings.Contains(body, "<h1>II.</h1>") {
		t.Errorf("no chapter heading in %q", body)
	}
	if !strings.Contains(body, "gikk ut på tunet og så solen skinne over dalen.") {
		t.Errorf("paragraph across the page break not joined: %q", body)
	}
	if err := checkXHTMLFragment(body); err != nil {
		t.Error(err)
	}
}

func jpegOf(t *testing.T, w, h int, draw func(img *image.Gray)) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 235
	}
	if draw != nil {
		draw(img)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBlankImage(t *testing.T) {
	if !BlankImage(jpegOf(t, 200, 300, nil)) {
		t.Error("empty page not blank")
	}
	text := jpegOf(t, 200, 300, func(img *image.Gray) {
		for y := 120; y < 130; y++ {
			for x := 60; x < 140; x++ {
				img.SetGray(x, y, color.Gray{Y: 20})
			}
		}
	})
	if BlankImage(text) {
		t.Error("page with a line of print counted as blank")
	}
}

func TestCorrectWords(t *testing.T) {
	word := func(text string, conf float64) ocr.Word { return ocr.Word{Text: text, Conf: conf} }
	sure := func(text string) ocr.Word { return word(text, 0.97) }
	pg := &ocr.Page{Width: 1000, Height: 1500, Blocks: []ocr.Block{{Lines: []ocr.Line{{Words: []ocr.Word{
		sure("næringsstøtte"), sure("næringsstøtte"), sure("næringsstøtte"), sure("Sammenhengene"),
		sure("efter"), sure("efter"), sure("efter"),
		word("næringsstotte,", 0.6), // ø read as o: corrected, comma kept
		word("Naeringsstøtte", 0.6), // no single confusion away: kept
		word("NÆRINGSSTOTTE", 0.5),  // capitals kept
		word("etter", 0.5),          // not one confusion from "efter": the book's old spelling is not forced on it
		word("næringsstotte", 0.95), // read with confidence: kept
	}}}}}}
	st := CorrectWords([]Page{{Text: pg}})
	got := []string{}
	for _, w := range pg.Blocks[0].Lines[0].Words[7:] {
		got = append(got, w.Text)
	}
	want := []string{"næringsstøtte,", "Naeringsstøtte", "NÆRINGSSTØTTE", "etter", "næringsstotte"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("corrected words = %q, want %q", got, want)
	}
	if st.Corrected != 2 {
		t.Errorf("corrected %d words, want 2: %+v", st.Corrected, pg.Blocks[0].Lines[0].Words)
	}
}

func TestStripNoteMarker(t *testing.T) {
	for in, want := range map[string]string{
		"tider.**9": "tider.", "forfektet.3*": "forfektet.", "tynne*": "tynne", `være."*`: `være."`,
		"1960.": "1960.", "nr.3": "nr.3", "side": "side", "2,5": "2,5", "fallende.**": "fallende.",
	} {
		if got := stripNoteMarker(in); got != want {
			t.Errorf("stripNoteMarker(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitRunTogether(t *testing.T) {
	vocab := map[string]int{"har": 9, "til": 9, "hørt": 5, "en": 9, "svært": 6, "liten": 6, "kroner": 6, "i": 50,
		"arbeid": 5, "s": 7, "avklaring": 3, "penger": 4, "for": 9, "tid": 4,
		"marginal": 6, "ser": 8, "ingen": 9, "effektiv": 7, "it": 3, "et": 20}
	for in, want := range map[string]string{
		"hartilhørtensværtliten":  "har til hørt en svært liten",
		"kroneri":                 "kroner i",
		"arbeidsavklaringspenger": "",
		"fortiden":                "",
		"marginaliseringen":       "",
		"effektiviteten":          "",
	} {
		if got := strings.Join(splitRunTogether(in, vocab), " "); got != want {
			t.Errorf("splitRunTogether(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeContinuedPageTurn(t *testing.T) {
	out := mergeContinued([]para{
		{kind: "p", text: "Det samlede tallet på", page: 4},
		{kind: "p", text: "469 i Norge, og familier i", page: 5},
		{kind: "p", text: "Norge, som før.", page: 6},
		{kind: "p", text: "Ny avsnitt", page: 6},
		{kind: "h1", text: "Fra olje til fond", page: 6},
		{kind: "p", text: "Norge er rikt.", page: 7},
		{kind: "p", text: "Slutt på siden", page: 7},
		{kind: "p", text: "Ny side.", page: 7},
	})
	var got []string
	for _, p := range out {
		got = append(got, p.kind+":"+p.text)
	}
	want := "p:Det samlede tallet på 469 i Norge, og familier i Norge, som før.|p:Ny avsnitt|h1:Fra olje til fond|p:Norge er rikt.|p:Slutt på siden|p:Ny side."
	if strings.Join(got, "|") != want {
		t.Errorf("got  %q\nwant %q", strings.Join(got, "|"), want)
	}
}

func TestMergeContinuedSmallPrint(t *testing.T) {
	out := mergeContinued([]para{
		{kind: "note", text: "Kilde: Federal Reserve Economic Data basert på data fra Bank for", page: 3},
		{kind: "note", text: "International Settlements, Dataserie RBNOBIS", page: 3},
		{kind: "note", text: "Kilde: SSB.", page: 3},
		{kind: "note", text: "Kilde: OECD.", page: 3},
	})
	if len(out) != 3 || out[0].text != "Kilde: Federal Reserve Economic Data basert på data fra Bank for International Settlements, Dataserie RBNOBIS" {
		t.Errorf("got %+v", out)
	}
}

func TestFixPrepositionI(t *testing.T) {
	for in, want := range map[string]string{
		"TK Whitaker var født 1 1916 og var sjef.":            "TK Whitaker var født i 1916 og var sjef.",
		"en økning 1I beskatningen av det som er igjen":       "en økning i beskatningen av det som er igjen",
		"det var en økonomi I rask utvikling i landet":        "det var en økonomi i rask utvikling i landet",
		"og valget 12005 var det som avgjorde":                "og valget i 2005 var det som avgjorde",
		"det er 12000 år siden isen gikk":                     "det er 12000 år siden isen gikk",
		"han gikk fra dag 1 til dag 2 i løpet av året":        "han gikk fra dag 1 til dag 2 i løpet av året",
		"det er modell 1 som er den beste av dem":             "det er modell 1 som er den beste av dem",
		"det er gitt utdelende, 1 motsetning til det som var": "det er gitt utdelende, i motsetning til det som var",
		"det var Kong Olav I som styrte og det var godt":      "det var Kong Olav I som styrte og det var godt",
		"and then I will say that it is the end of it":        "and then I will say that it is the end of it",
		"det er bare 1 prosent som har det slik":              "det er bare 1 prosent som har det slik",
		"en ny jobb 1 Norge er det som teller":                "en ny jobb i Norge er det som teller",
	} {
		if got := fixPrepositionI(in); got != want {
			t.Errorf("fixPrepositionI(%q)\n = %q\nwant %q", in, got, want)
		}
	}
}

// The space under a photo's caption is layout, not a scene break, though
// the caption names no figure; the same space between paragraphs is one.
func TestNoBreakAfterPhotoCaption(t *testing.T) {
	long := "Dette er en lang linje med mange ord i seg som fortsetter"
	body := func(y float64, last string) ocr.Block {
		return blockOf(wordsLine(long, 362, y, 2094), wordsLine(long, 362, y+66, 2094), wordsLine(last, 362, y+132, 1200))
	}
	photo := ocr.Block{Lines: []ocr.Line{{Box: ocr.Rect{X0: 362, Y0: 700, X1: 2094, Y1: 1500}}}, Box: ocr.Rect{X0: 362, Y0: 700, X1: 2094, Y1: 1500}}
	pg := &ocr.Page{Width: 2409, Height: 3559, Blocks: []ocr.Block{
		body(400, "slutt på avsnittet."),
		photo,
		blockOf(wordsLine("Arkeologiske utgravingsplasser, alle årstall før vår tidsregning.", 362, 1530, 1700)),
		body(1800, "slutt på avsnittet."),
		body(2150, "slutt."),
	}}
	chs, _ := Book([]Page{{Text: pg}})
	if n := strings.Count(chs[0].Body, "<hr"); n != 1 {
		t.Errorf("%d scene breaks, want 1 (between the two last paragraphs):\n%s", n, chs[0].Body)
	}
}

func TestMergeContinuedNotPastPicture(t *testing.T) {
	out := mergeContinued([]para{
		{kind: "p", text: "Utgifter per år, milliarder kroner", page: 4},
		{kind: "figure", image: "p0005-1.jpg", page: 4},
		{kind: "p", text: "Figur 7 viser hvordan det gikk.", page: 5},
	})
	if len(out) != 3 {
		t.Errorf("title joined to the text past its picture: %+v", out)
	}
}

func TestMergeContinuedNotFigureNumber(t *testing.T) {
	out := mergeContinued([]para{
		{kind: "p", text: "Det går tilbake til år", page: 4},
		{kind: "p", text: "FIGUR 15", page: 5},
	})
	if len(out) != 2 {
		t.Errorf("figure number joined to the text: %+v", out)
	}
}

// altoPage builds an ALTO file whose blocks use units 2.5 times larger
// than lines and words, as some of nb.no's files do.
func altoPage(lines ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><alto><Layout><Page WIDTH="1000" HEIGHT="1500">`)
	b.WriteString(`<TopMargin HPOS="0" VPOS="0" WIDTH="2500" HEIGHT="250"><TextBlock HPOS="1200" VPOS="100" WIDTH="100" HEIGHT="50">` +
		`<TextLine HPOS="480" VPOS="40" WIDTH="40" HEIGHT="20"><String CONTENT="12" HPOS="480" VPOS="40" WIDTH="40" HEIGHT="20" WC="1"/></TextLine></TextBlock></TopMargin>`)
	b.WriteString(`<PrintSpace HPOS="250" VPOS="250" WIDTH="2000" HEIGHT="3250">`)
	for i, l := range lines {
		y := 200 + i*30
		fmt.Fprintf(&b, `<TextBlock HPOS="250" VPOS="%d" WIDTH="2000" HEIGHT="62"><TextLine HPOS="100" VPOS="%d" WIDTH="800" HEIGHT="25">`, int(float64(y)*2.5), y)
		for j, w := range strings.Fields(l) {
			attrs := ""
			if strings.HasSuffix(w, "|1") {
				w = strings.TrimSuffix(w, "|1")
				attrs = ` SUBS_TYPE="HypPart1" SUBS_CONTENT="teater"`
			} else if strings.HasSuffix(w, "|2") {
				w = strings.TrimSuffix(w, "|2")
				attrs = ` SUBS_TYPE="HypPart2" SUBS_CONTENT="teater"`
			}
			fmt.Fprintf(&b, `<String CONTENT="%s" HPOS="%d" VPOS="%d" WIDTH="60" HEIGHT="25" WC="0.98"%s/>`, w, 100+j*70, y, attrs)
		}
		b.WriteString(`</TextLine></TextBlock>`)
	}
	b.WriteString(`</PrintSpace></Page></Layout></alto>`)
	return b.String()
}

func TestParseALTO(t *testing.T) {
	pg, err := ocr.ParseALTO(strings.NewReader(altoPage("Han gikk i te|1", "ater|2 i går.")))
	if err != nil {
		t.Fatal(err)
	}
	if pg.Width != 1000 || pg.Height != 1500 {
		t.Errorf("page size %gx%g, want the Page element's 1000x1500", pg.Width, pg.Height)
	}
	if len(pg.Blocks) != 3 || !pg.Blocks[0].Margin {
		t.Fatalf("got %d blocks, first margin=%v", len(pg.Blocks), pg.Blocks[0].Margin)
	}
	if b := pg.Blocks[1].Box; b.X0 != 100 || b.Y0 != 200 {
		t.Errorf("block box %+v not in word units", b)
	}
	if got := joinLines(append(pg.Blocks[1].Lines, pg.Blocks[2].Lines...)); got != "Han gikk i teater i går." {
		t.Errorf("joined text = %q", got)
	}
}

// checkXHTMLFragment catches bodies that are not well-formed XML, which
// strict EPUB readers refuse to open at all.
func checkXHTMLFragment(body string) error {
	d := xml.NewDecoder(strings.NewReader("<body>" + body + "</body>"))
	d.Strict = true
	for {
		_, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
