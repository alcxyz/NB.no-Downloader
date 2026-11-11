package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTSV(t *testing.T) {
	tsv := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"1\t1\t0\t0\t0\t0\t0\t0\t1000\t1500\t-1\t\n" +
		"2\t1\t1\t0\t0\t0\t100\t100\t800\t300\t-1\t\n" + // block without paragraphs: a picture
		"2\t1\t2\t0\t0\t0\t100\t500\t800\t100\t-1\t\n" +
		"3\t1\t2\t1\t0\t0\t100\t500\t800\t100\t-1\t\n" +
		"4\t1\t2\t1\t1\t0\t100\t500\t800\t40\t-1\t\n" +
		"5\t1\t2\t1\t1\t1\t100\t500\t200\t40\t96.5\tHei\n" +
		"5\t1\t2\t1\t1\t2\t320\t500\t200\t40\t91\tverden\n"
	pg, err := parseTSV(tsv)
	if err != nil {
		t.Fatal(err)
	}
	if pg.Width != 1000 || pg.Height != 1500 || len(pg.Blocks) != 2 {
		t.Fatalf("page %gx%g with %d blocks", pg.Width, pg.Height, len(pg.Blocks))
	}
	if !pg.Blocks[0].Image || pg.Blocks[1].Image {
		t.Error("first block should be a picture, second text")
	}
	words := pg.Blocks[1].Lines[0].Words
	if len(words) != 2 || words[1].Text != "verden" || words[0].Conf != 0.965 {
		t.Errorf("words = %+v", words)
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
	pg, err := parseALTO(strings.NewReader(altoPage("Han gikk i te|1", "ater|2 i går.")))
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

func TestJoinLinesHyphens(t *testing.T) {
	line := func(words ...string) textLine {
		var l textLine
		for _, w := range words {
			l.Words = append(l.Words, textWord{Text: w, Conf: 1})
		}
		return l
	}
	tests := []struct {
		lines []textLine
		want  string
	}{
		{[]textLine{line("sammen-"), line("hengene", "er")}, "sammenhengene er"},
		{[]textLine{line("EU-"), line("landene")}, "EU-landene"},
		{[]textLine{line("1990-"), line("tallet")}, "1990-tallet"},
		{[]textLine{line("Oslo-"), line("Bergen")}, "Oslo- Bergen"},
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
func synthPage(head string, paras ...[]string) *textPage {
	pg := &textPage{Width: 1000, Height: 1500}
	if head != "" {
		pg.Blocks = append(pg.Blocks, textBlock{Lines: []textLine{mkLine(head, 100, 60, 900)}})
	}
	y := 180.0
	for _, p := range paras {
		var b textBlock
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
			b.Box = b.Box.union(line.Box)
			y += 30
		}
		pg.Blocks = append(pg.Blocks, b)
	}
	return pg
}

func mkLine(text string, x0, y, x1 float64) textLine {
	l := textLine{Box: rect{x0, y, x1, y + 25}}
	for _, w := range strings.Fields(text) {
		l.Words = append(l.Words, textWord{Text: w, Box: l.Box, Conf: 0.97})
	}
	return l
}

func TestLayoutBook(t *testing.T) {
	long := "Dette er en lang linje med mange ord i seg"
	pages := []layoutPage{
		{Kind: pageCover},
		{Text: synthPage("12 Jørundgård",
			[]string{long, long, long + "."},
			[]string{"^II."},
			[]string{"Det var en vakker morgen og hun", long, "gikk ut på tunet og"})},
		{Text: synthPage("13 Jørundgård",
			[]string{"så solen skinne over dalen.", long, long + "."},
			[]string{long, long, long, "Slutt."})},
		{Text: synthPage("14 Jørundgård", []string{long, long, long + "."})},
		{Kind: pageSkip},
	}
	chs, figs := layoutBook(pages)
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

func TestCatalogFromItem(t *testing.T) {
	var item catalogItem
	item.Metadata.Title = "Kristin Lavransdatter : Kransen"
	item.Metadata.People = append(item.Metadata.People,
		struct {
			Name  string `json:"name"`
			Usage string `json:"usage"`
			Roles []struct {
				Name string `json:"name"`
			} `json:"roles"`
		}{Name: "Undset, Sigrid", Usage: "primary", Roles: []struct {
			Name string `json:"name"`
		}{{Name: "cre"}}},
		struct {
			Name  string `json:"name"`
			Usage string `json:"usage"`
			Roles []struct {
				Name string `json:"name"`
			} `json:"roles"`
		}{Name: "Grimnes, Finn", Roles: []struct {
			Name string `json:"name"`
		}{{Name: "edt"}}})
	item.Metadata.Identifiers.ISBN13 = []string{"978-82-489-3825-5"}
	item.Metadata.OriginInfo.Issued = "[1960]"
	item.Metadata.Languages = append(item.Metadata.Languages, struct {
		Code string `json:"code"`
	}{"nob"})
	info := catalogFromItem(&item, "URN:NBN:no-nb_digibok_1")
	m := info.Meta
	if len(m.Authors) != 1 || m.Authors[0] != "Sigrid Undset" || m.AuthorsSort[0] != "Undset, Sigrid" {
		t.Errorf("authors = %v / %v", m.Authors, m.AuthorsSort)
	}
	if len(m.Contributors) != 1 || m.Contributors[0] != "Finn Grimnes" {
		t.Errorf("contributors = %v", m.Contributors)
	}
	if m.ISBNs[0] != "9788248938255" || m.Date != "1960" || m.Language != "nb" {
		t.Errorf("isbn %v date %q language %q", m.ISBNs, m.Date, m.Language)
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
	if !blankImage(jpegOf(t, 200, 300, nil)) {
		t.Error("empty page not blank")
	}
	text := jpegOf(t, 200, 300, func(img *image.Gray) {
		for y := 120; y < 130; y++ {
			for x := 60; x < 140; x++ {
				img.SetGray(x, y, color.Gray{Y: 20})
			}
		}
	})
	if blankImage(text) {
		t.Error("page with a line of print counted as blank")
	}
}

func TestFigureURL(t *testing.T) {
	p := Page{Service: "https://x/img/7", Width: 2000, Height: 3000}
	text := &textPage{Width: 1000, Height: 1500}
	got := figureURL(p, text, rect{100, 200, 900, 700}, 1200)
	if want := "https://x/img/7/200,400,1600,1000/1200,/0/default.jpg"; got != want {
		t.Errorf("figureURL = %q, want %q", got, want)
	}
	if got := figureURL(p, text, rect{}, 1200); got != "https://x/img/7/full/1200,/0/default.jpg" {
		t.Errorf("whole page = %q", got)
	}
}

// fakeLibrary serves a small book the way nb.no does, with ALTO text.
func fakeLibrary(t *testing.T) *httptest.Server {
	t.Helper()
	cover := jpegOf(t, 40, 60, func(img *image.Gray) { img.SetGray(5, 5, color.Gray{Y: 0}) })
	long := "Dette er en lang linje med mange ord i seg"
	altos := map[string]string{
		"0001": altoPage(long, long, long+"."),
		"0002": altoPage(long, long, "Slutt."),
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch {
		case strings.HasPrefix(r.URL.Path, "/iiif/"):
			w.Header().Set("Content-Type", "application/json")
			canvas := func(id string) string {
				return fmt.Sprintf(`{"label":"%s","width":1000,"height":1500,"images":[{"resource":{"service":{"@id":"%s/img/URN:NBN:no-nb_digibok_1_%s"}}}]}`, id, base, id)
			}
			fmt.Fprintf(w, `{"label":"Prøvebok","sequences":[{"canvases":[%s,%s,%s,%s]}]}`, canvas("C1"), canvas("0001"), canvas("0002"), canvas("C2"))
		case strings.HasPrefix(r.URL.Path, "/items/"):
			w.Header().Set("Content-Type", "application/hal+json")
			io.WriteString(w, `{"metadata":{"title":"Prøvebok","people":[{"name":"Nordmann, Kari","roles":[{"name":"aut"}]}],
				"identifiers":{"urn":"URN:NBN:no-nb_digibok_1","isbn13":["9788200000000"]},"originInfo":{"publisher":"Forlaget","issued":"2001"},
				"languages":[{"code":"nob"}]}}`)
		case strings.HasPrefix(r.URL.Path, "/metadata/"):
			page := r.URL.Path[strings.LastIndex(r.URL.Path, "_")+1:]
			if a, ok := altos[page]; ok {
				w.Header().Set("Content-Type", "application/xml;charset=UTF-8")
				io.WriteString(w, a)
				return
			}
			http.NotFound(w, r)
		case strings.HasPrefix(r.URL.Path, "/img/"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(cover)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSaveEPUBFromALTO(t *testing.T) {
	srv := fakeLibrary(t)
	defer srv.Close()
	d := newTestDownloader(t, srv, false)
	d.catalog = srv.URL + "/items/"
	d.alto = srv.URL + "/metadata/"
	ctx := context.Background()
	book, err := d.fetchBook(ctx, "URN:NBN:no-nb_digibok_1")
	if err != nil {
		t.Fatal(err)
	}
	if book.URN != "URN:NBN:no-nb_digibok_1" {
		t.Errorf("book URN = %q", book.URN)
	}
	out := filepath.Join(t.TempDir(), "bok.epub")
	if err := saveEPUB(ctx, d, book, "x", out, saveOptions{format: "epub", width: 600, workers: 2}, nil); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(data)
	}
	opf := files["OEBPS/content.opf"]
	for _, want := range []string{"Kari Nordmann", "urn:isbn:9788200000000", "Forlaget", "URN:NBN:no-nb_digibok_1"} {
		if !strings.Contains(opf, want) {
			t.Errorf("OPF lacks %q", want)
		}
	}
	if _, ok := files["OEBPS/images/cover.jpg"]; !ok {
		t.Error("no cover image")
	}
	if ch := files["OEBPS/chapter001.xhtml"]; !strings.Contains(ch, "Slutt.") || strings.Contains(ch, ">12<") {
		t.Errorf("chapter text wrong: %q", ch)
	}
}

func TestCorrectWords(t *testing.T) {
	word := func(text string, conf float64) textWord { return textWord{Text: text, Conf: conf} }
	sure := func(text string) textWord { return word(text, 0.97) }
	pg := &textPage{Width: 1000, Height: 1500, Blocks: []textBlock{{Lines: []textLine{{Words: []textWord{
		sure("næringsstøtte"), sure("næringsstøtte"), sure("næringsstøtte"), sure("Sammenhengene"),
		sure("efter"), sure("efter"), sure("efter"),
		word("næringsstotte,", 0.6), // ø read as o: corrected, comma kept
		word("Naeringsstøtte", 0.6), // no single confusion away: kept
		word("NÆRINGSSTOTTE", 0.5),  // capitals kept
		word("etter", 0.5),          // not one confusion from "efter": the book's old spelling is not forced on it
		word("næringsstotte", 0.95), // read with confidence: kept
	}}}}}}
	st := correctWords([]layoutPage{{Text: pg}})
	got := []string{}
	for _, w := range pg.Blocks[0].Lines[0].Words[7:] {
		got = append(got, w.Text)
	}
	want := []string{"næringsstøtte,", "Naeringsstøtte", "NÆRINGSSTØTTE", "etter", "næringsstotte"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("corrected words = %q, want %q", got, want)
	}
	if st.corrected != 2 {
		t.Errorf("corrected %d words, want 2: %+v", st.corrected, pg.Blocks[0].Lines[0].Words)
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

func TestWithoutFigures(t *testing.T) {
	chs := []epubChapter{
		{Title: "A", Body: "<p>Tekst</p>\n<figure class=\"figure\"><img src=\"images/p0003-1.jpg\" alt=\"\"/></figure>\n<p>Mer</p>\n"},
		{Title: "B", Body: "<figure class=\"pageimage\"><img src=\"images/p0009.jpg\" alt=\"\"/></figure>\n"},
	}
	got := withoutFigures(chs, []string{"p0003-1.jpg", "p0009.jpg"})
	if len(got) != 1 || got[0].Body != "<p>Tekst</p>\n<p>Mer</p>\n" {
		t.Errorf("withoutFigures = %+v", got)
	}
}
