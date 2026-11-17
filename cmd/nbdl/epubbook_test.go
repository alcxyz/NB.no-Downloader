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
	"time"

	"github.com/alcxyz/NB.no-Downloader/internal/nb"

	"github.com/alcxyz/NB.no-Downloader/internal/epub"
	"github.com/alcxyz/NB.no-Downloader/internal/layout"
	"github.com/alcxyz/NB.no-Downloader/internal/ocr"
)

func TestFigureURL(t *testing.T) {
	p := nb.Page{Service: "https://x/img/7", Width: 2000, Height: 3000}
	text := &ocr.Page{Width: 1000, Height: 1500}
	got := figureURL(p, text, ocr.Rect{X0: 100, Y0: 200, X1: 900, Y1: 700}, 1200)
	if want := "https://x/img/7/200,400,1600,1000/1200,/0/default.jpg"; got != want {
		t.Errorf("figureURL = %q, want %q", got, want)
	}
	if got := figureURL(p, text, ocr.Rect{}, 1200); got != "https://x/img/7/full/1200,/0/default.jpg" {
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
	d := nb.NewWithConfig(nb.Config{
		Client:     &http.Client{},
		AuthDomain: "127.0.0.1",
		Manifest:   srv.URL + "/iiif/",
		Catalog:    srv.URL + "/items/",
		ALTO:       srv.URL + "/metadata/",
		Retries:    4,
		RetryDelay: time.Millisecond,
	})
	ctx := context.Background()
	book, err := d.FetchBook(ctx, "URN:NBN:no-nb_digibok_1")
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

func TestWithoutFigures(t *testing.T) {
	chs := []epub.Chapter{
		{Title: "A", Body: "<p>Tekst</p>\n<figure class=\"figure\"><img src=\"images/p0003-1.jpg\" alt=\"\"/></figure>\n<p>Mer</p>\n"},
		{Title: "B", Body: "<figure class=\"pageimage\"><img src=\"images/p0009.jpg\" alt=\"\"/></figure>\n"},
	}
	got := withoutFigures(chs, []string{"p0003-1.jpg", "p0009.jpg"})
	if len(got) != 1 || got[0].Body != "<p>Tekst</p>\n<p>Mer</p>\n" {
		t.Errorf("withoutFigures = %+v", got)
	}
}

func TestKindOf(t *testing.T) {
	for id, want := range map[string]layout.Kind{
		"URN:NBN:no-nb_digibok_2008_C1":   layout.Cover,
		"URN:NBN:no-nb_digibok_2008_C3":   layout.Back,
		"URN:NBN:no-nb_digibok_2008_V01":  layout.Back,
		"URN:NBN:no-nb_digibok_2008_I1":   layout.Normal,
		"URN:NBN:no-nb_digibok_2008_0012": layout.Normal,
	} {
		if got := kindOf(id); got != want {
			t.Errorf("kindOf(%q) = %v, want %v", id, got, want)
		}
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
