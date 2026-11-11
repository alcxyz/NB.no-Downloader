package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for x := 0; x < 20; x++ {
		img.Set(x, 5, color.RGBA{R: 255, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testBook(t *testing.T) *epubBook {
	return &epubBook{
		Meta: bookMeta{
			Title:        "Kristin Lavransdatter",
			Subtitle:     "Kransen",
			Authors:      []string{"Sigrid Undset", "Ola Nordmann"},
			AuthorsSort:  []string{"Undset, Sigrid", "Nordmann, Ola"},
			Contributors: []string{"Kari Tegner"},
			Language:     "nb",
			Publisher:    "Aschehoug",
			Date:         "1994",
			ISBNs:        []string{"8203168761"},
			Identifier:   "URN:NBN:no-nb_digibok_2008010300041",
			Subjects:     []string{"Romaner", "Middelalderen"},
			Description:  "Første bind.\nOm Kristin & Erlend <1300-tallet>.",
			Source:       "https://www.nb.no/items/URN:NBN:no-nb_digibok_2008010300041",
		},
		Cover: &epubImage{Name: "cover.jpg", Data: testJPEG(t, 60, 90, true), MediaType: "image/jpeg"},
		Chapters: []epubChapter{
			{Title: "Første kapittel", Body: "<h1>Første kapittel</h1>\n<p>" + xmlEscape("Å lykkes med æøå & \"sitater\"") + "</p>\n" +
				"<figure><img src=\"images/p0012.png\" alt=\"Kart\"/></figure>\n<p class=\"note\">1) En fotnote.</p>"},
			{Title: "", Body: "<p class=\"noindent\">Fortsettelse uten tittel.</p>"},
			{Title: "Annet kapittel", Body: "<h1>Annet kapittel</h1>\n<p>Blåbærsyltetøy.</p>"},
		},
		Images: []epubImage{{Name: "p0012.png", Data: testPNG(t), MediaType: "image/png"}},
	}
}

func writeTestBook(t *testing.T) []byte {
	t.Helper()
	orig := now
	now = func() time.Time { return time.Date(2026, 11, 11, 12, 30, 45, 999, time.FixedZone("CET", 3600)) }
	t.Cleanup(func() { now = orig })
	var buf bytes.Buffer
	if err := writeEPUB(&buf, testBook(t)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readZipFile(t *testing.T, zr *zip.Reader, name string) string {
	t.Helper()
	f, err := zr.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteEPUB(t *testing.T) {
	data := writeTestBook(t)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	first := zr.File[0]
	if first.Name != "mimetype" || first.Method != zip.Store || len(first.Extra) != 0 {
		t.Fatalf("first entry %q method %d extra %d, want stored mimetype without extra field", first.Name, first.Method, len(first.Extra))
	}
	// Readers sniff the media type at offset 38 of the file.
	if got := string(data[30:38]); got != "mimetype" {
		t.Fatalf("local header name %q", got)
	}
	if got := readZipFile(t, zr, "mimetype"); got != "application/epub+zip" {
		t.Fatalf("mimetype %q", got)
	}

	var container struct {
		Rootfiles []struct {
			FullPath  string `xml:"full-path,attr"`
			MediaType string `xml:"media-type,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal([]byte(readZipFile(t, zr, "META-INF/container.xml")), &container); err != nil {
		t.Fatal(err)
	}
	if len(container.Rootfiles) != 1 || container.Rootfiles[0].FullPath != "OEBPS/content.opf" {
		t.Fatalf("container rootfiles %+v", container.Rootfiles)
	}

	type meta struct {
		Refines  string `xml:"refines,attr"`
		Property string `xml:"property,attr"`
		Name     string `xml:"name,attr"`
		Content  string `xml:"content,attr"`
		Value    string `xml:",chardata"`
	}
	type idText struct {
		ID    string `xml:"id,attr"`
		Value string `xml:",chardata"`
	}
	var opf struct {
		UniqueID string `xml:"unique-identifier,attr"`
		Metadata struct {
			Identifiers  []idText `xml:"http://purl.org/dc/elements/1.1/ identifier"`
			Titles       []idText `xml:"http://purl.org/dc/elements/1.1/ title"`
			Creators     []idText `xml:"http://purl.org/dc/elements/1.1/ creator"`
			Contributors []string `xml:"http://purl.org/dc/elements/1.1/ contributor"`
			Language     string   `xml:"http://purl.org/dc/elements/1.1/ language"`
			Publisher    string   `xml:"http://purl.org/dc/elements/1.1/ publisher"`
			Date         string   `xml:"http://purl.org/dc/elements/1.1/ date"`
			Subjects     []string `xml:"http://purl.org/dc/elements/1.1/ subject"`
			Description  string   `xml:"http://purl.org/dc/elements/1.1/ description"`
			Source       string   `xml:"http://purl.org/dc/elements/1.1/ source"`
			Metas        []meta   `xml:"meta"`
		} `xml:"metadata"`
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"manifest>item"`
		Spine struct {
			Toc      string `xml:"toc,attr"`
			Itemrefs []struct {
				IDRef string `xml:"idref,attr"`
			} `xml:"itemref"`
		} `xml:"spine"`
	}
	if err := xml.Unmarshal([]byte(readZipFile(t, zr, "OEBPS/content.opf")), &opf); err != nil {
		t.Fatal(err)
	}
	md := opf.Metadata
	if opf.UniqueID != "bookid" || len(md.Identifiers) != 2 ||
		md.Identifiers[0] != (idText{"bookid", "URN:NBN:no-nb_digibok_2008010300041"}) ||
		md.Identifiers[1].Value != "urn:isbn:8203168761" {
		t.Errorf("identifiers %q %+v", opf.UniqueID, md.Identifiers)
	}
	if len(md.Titles) != 2 || md.Titles[0].Value != "Kristin Lavransdatter" || md.Titles[1].Value != "Kransen" {
		t.Errorf("titles %+v", md.Titles)
	}
	if len(md.Creators) != 2 || md.Creators[0].Value != "Sigrid Undset" || md.Creators[1].Value != "Ola Nordmann" {
		t.Errorf("creators %+v", md.Creators)
	}
	if len(md.Contributors) != 1 || md.Language != "nb" || md.Publisher != "Aschehoug" || md.Date != "1994" ||
		len(md.Subjects) != 2 || md.Description != "Første bind.\nOm Kristin & Erlend <1300-tallet>." ||
		!strings.HasPrefix(md.Source, "https://www.nb.no/") {
		t.Errorf("metadata %+v", md)
	}
	want := map[string]string{
		"#subtitle title-type": "subtitle",
		"#creator1 role":       "aut",
		"#creator1 file-as":    "Undset, Sigrid",
		"#creator2 file-as":    "Nordmann, Ola",
		" dcterms:modified":    "2026-11-11T11:30:45Z",
	}
	got := make(map[string]string)
	cover := ""
	for _, m := range md.Metas {
		got[m.Refines+" "+m.Property] = m.Value
		if m.Name == "cover" {
			cover = m.Content
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("meta %q = %q, want %q", k, got[k], v)
		}
	}
	if cover != "cover-image" {
		t.Errorf("EPUB 2 cover meta %q", cover)
	}

	ids := make(map[string]bool)
	for _, it := range opf.Items {
		ids[it.ID] = true
		if _, err := zr.Open("OEBPS/" + it.Href); err != nil {
			t.Errorf("manifest item %s: %v", it.Href, err)
		}
		if it.ID == "cover-image" && it.Properties != "cover-image" {
			t.Errorf("cover image properties %q", it.Properties)
		}
	}
	for _, name := range []string{"nav", "ncx", "css", "cover", "cover-image", "chapter001", "chapter003", "img001"} {
		if !ids[name] {
			t.Errorf("manifest lacks %s", name)
		}
	}
	var spine []string
	for _, r := range opf.Spine.Itemrefs {
		spine = append(spine, r.IDRef)
	}
	if opf.Spine.Toc != "ncx" || strings.Join(spine, " ") != "cover chapter001 chapter002 chapter003" {
		t.Errorf("spine toc=%q %v", opf.Spine.Toc, spine)
	}

	var nav struct {
		Navs []struct {
			Type  string `xml:"http://www.idpf.org/2007/ops type,attr"`
			Links []struct {
				Href string `xml:"href,attr"`
				Text string `xml:",chardata"`
			} `xml:"ol>li>a"`
		} `xml:"body>nav"`
	}
	if err := xml.Unmarshal([]byte(readZipFile(t, zr, "OEBPS/nav.xhtml")), &nav); err != nil {
		t.Fatal(err)
	}
	if len(nav.Navs) != 2 || nav.Navs[0].Type != "toc" || nav.Navs[1].Type != "landmarks" {
		t.Fatalf("navs %+v", nav.Navs)
	}
	toc := nav.Navs[0].Links
	if len(toc) != 2 || toc[0].Text != "Første kapittel" || toc[0].Href != "chapter001.xhtml" ||
		toc[1].Text != "Annet kapittel" || toc[1].Href != "chapter003.xhtml" {
		t.Errorf("toc %+v", toc)
	}

	ncx := readZipFile(t, zr, "OEBPS/toc.ncx")
	if strings.Count(ncx, "<navPoint") != 2 || !strings.Contains(ncx, `content="URN:NBN:no-nb_digibok_2008010300041"`) {
		t.Errorf("ncx:\n%s", ncx)
	}

	ch := readZipFile(t, zr, "OEBPS/chapter001.xhtml")
	for _, s := range []string{"<!DOCTYPE html>", `xml:lang="nb" lang="nb"`, "<title>Første kapittel</title>", `href="style.css"`, "Å lykkes med æøå &amp; &quot;sitater&quot;"} {
		if !strings.Contains(ch, s) {
			t.Errorf("chapter001 lacks %q", s)
		}
	}
	if err := xml.Unmarshal([]byte(ch), new(struct{})); err != nil {
		t.Errorf("chapter001 is not well-formed: %v", err)
	}
}

func TestWriteEPUBValidation(t *testing.T) {
	cases := []struct {
		name   string
		modify func(b *epubBook)
		want   string
	}{
		{"empty identifier", func(b *epubBook) { b.Meta.Identifier = " " }, "identifier"},
		{"duplicate image", func(b *epubBook) { b.Images = append(b.Images, b.Images[0]) }, "duplicate"},
		{"duplicate with cover", func(b *epubBook) {
			b.Images[0].Name = "COVER.jpg"
			b.Images[0].MediaType = "image/jpeg"
			b.Images[0].Data = b.Cover.Data
		}, "duplicate"},
		{"media type", func(b *epubBook) { b.Images[0].MediaType = "image/gif" }, "media type"},
		{"cover media type", func(b *epubBook) { b.Cover.MediaType = "image/webp" }, "media type"},
		{"data mismatch", func(b *epubBook) { b.Images[0].MediaType = "image/jpeg"; b.Images[0].Name = "p0012.jpg" }, "not JPEG"},
		{"unsafe name", func(b *epubBook) { b.Images[0].Name = "../x.png" }, "may only contain"},
		{"no chapters", func(b *epubBook) { b.Chapters = nil }, "no chapters"},
		{"malformed body", func(b *epubBook) { b.Chapters[0].Body = "<p>a &nbsp; b</p>" }, "chapter 1"},
		{"sort names", func(b *epubBook) { b.Meta.AuthorsSort = b.Meta.AuthorsSort[:1] }, "sort names"},
		{"date", func(b *epubBook) { b.Meta.Date = "mai 1994" }, "date"},
	}
	for _, c := range cases {
		b := testBook(t)
		c.modify(b)
		var buf bytes.Buffer
		err := writeEPUB(&buf, b)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.want)
		}
		if buf.Len() != 0 {
			t.Errorf("%s: wrote %d bytes despite the error", c.name, buf.Len())
		}
	}
}

func TestWriteEPUBMinimal(t *testing.T) {
	b := &epubBook{
		Meta:     bookMeta{Title: "Uten tittel", Identifier: "URN:NBN:no-nb_digibok_1"},
		Chapters: []epubChapter{{Body: "<p>Tekst.</p>"}},
	}
	var buf bytes.Buffer
	if err := writeEPUB(&buf, b); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	opf := readZipFile(t, zr, "OEBPS/content.opf")
	for _, absent := range []string{"cover", "dc:creator", "dc:publisher", "dc:date", "dc:description", "dc:source", "title-type"} {
		if strings.Contains(opf, absent) {
			t.Errorf("minimal OPF contains %q", absent)
		}
	}
	if !strings.Contains(opf, "<dc:language>nb</dc:language>") {
		t.Errorf("default language missing")
	}
	// Nav and NCX need at least one entry even without chapter titles.
	if nav := readZipFile(t, zr, "OEBPS/nav.xhtml"); !strings.Contains(nav, `<a href="chapter001.xhtml">Uten tittel</a>`) {
		t.Errorf("nav fallback missing:\n%s", nav)
	}
}

func TestXMLEscape(t *testing.T) {
	got := xmlEscape("a&b<c>d\"e'f æøå\x00\x0b\tg")
	want := "a&amp;b&lt;c&gt;d&quot;e&apos;f æøå\tg"
	if got != want {
		t.Errorf("xmlEscape = %q, want %q", got, want)
	}
}

// TestEPUBCheck validates the test book with the real epubcheck. It is
// opt-in because epubcheck needs a JVM: run it with
//
//	NBDL_EPUBCHECK=1 nix-shell -p epubcheck --run 'go test ./cmd/nbdl -run EPUBCheck -v'
func TestEPUBCheck(t *testing.T) {
	if os.Getenv("NBDL_EPUBCHECK") == "" {
		t.Skip("set NBDL_EPUBCHECK=1 to run epubcheck")
	}
	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		t.Skip("epubcheck not on PATH")
	}
	books := map[string]*epubBook{"full.epub": testBook(t), "minimal.epub": {
		Meta:     bookMeta{Title: "Uten tittel", Identifier: "URN:NBN:no-nb_digibok_1"},
		Chapters: []epubChapter{{Body: "<p>Tekst.</p>"}},
	}}
	for name, b := range books {
		path := filepath.Join(t.TempDir(), name)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeEPUB(f, b); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bin, path).CombinedOutput()
		t.Logf("%s:\n%s", name, out)
		if err != nil {
			t.Errorf("%s: epubcheck failed: %v", name, err)
		}
		if bytes.Contains(out, []byte("WARNING")) || bytes.Contains(out, []byte("ERROR")) {
			t.Errorf("%s: epubcheck reported problems", name)
		}
	}
}
