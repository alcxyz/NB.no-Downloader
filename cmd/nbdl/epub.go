package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// now is replaced in tests so dcterms:modified is reproducible.
var now = time.Now

// bookMeta is the catalogue metadata written into the EPUB package document.
type bookMeta struct {
	Title        string
	Subtitle     string   // may be empty
	Authors      []string // display names, "Sigrid Undset"
	AuthorsSort  []string // same length as Authors or empty: "Undset, Sigrid"
	Contributors []string // translators, illustrators etc., display names; may be empty
	Language     string   // BCP 47, e.g. "nb", "nn", "en"; default "nb" if empty
	Publisher    string
	Date         string   // "1994" or "1994-05-01"; may be empty
	ISBNs        []string // digits/X only, may be empty
	Identifier   string   // nb.no URN, e.g. "URN:NBN:no-nb_digibok_2008010300041"; always set
	Subjects     []string
	Description  string // plain text, may contain newlines
	Source       string // URL of the book on nb.no
}

// epubImage is an image file inside the EPUB.
type epubImage struct {
	Name      string // file name, unique, e.g. "p0012.jpg"; chapters reference it as "images/<Name>"
	Data      []byte
	MediaType string // "image/jpeg" or "image/png"
}

// epubChapter is one XHTML content document.
type epubChapter struct {
	Title string // used in the table of contents; may be empty (then omit from TOC but keep in spine)
	Body  string // XHTML fragment that goes inside <body>, already escaped by the caller
}

type epubBook struct {
	Meta     bookMeta
	Cover    *epubImage // may be nil
	Chapters []epubChapter
	Images   []epubImage // images referenced from chapter bodies, excluding Cover
}

const epubCSS = `body { margin: 0 1em; line-height: 1.4; }
p { margin: 0; text-indent: 1.5em; text-align: justify; }
p.noindent, h1 + p, h2 + p, figure + p { text-indent: 0; }
p.note { font-size: 0.85em; text-indent: 0; margin-top: 0.5em; }
h1, h2 { text-align: center; font-weight: bold; line-height: 1.2; }
h1 { font-size: 1.6em; margin: 1.5em 0 1em; }
h2 { font-size: 1.25em; margin: 1.2em 0 0.8em; }
figure { margin: 1em 0; text-align: center; }
img { max-width: 100%; height: auto; }
figcaption { font-size: 0.9em; }
hr.break { border: 0; margin: 1.2em 0; }
a.noteref { text-decoration: none; }
a.noteref sup { font-size: 0.7em; line-height: 0; }
p.endnote { text-indent: 0; margin: 0 0 0.5em 0; }
.pageimage { display: block; margin: 0 auto; max-width: 100%; max-height: 100%; page-break-inside: avoid; }
div.cover { margin: 0; padding: 0; height: 100%; text-align: center; }
div.cover img { max-width: 100%; max-height: 100%; height: 100%; }
`

// writeEPUB writes b as an EPUB 3 file that also works in EPUB 2 readers.
func writeEPUB(w io.Writer, b *epubBook) error {
	if err := validateEPUB(b); err != nil {
		return err
	}
	lang := b.Meta.Language
	if lang == "" {
		lang = "nb"
	}

	z := zip.NewWriter(w)

	// OCF requires mimetype first, stored, with no extra field so readers
	// can sniff it at a fixed offset. CreateRaw with precomputed sizes avoids
	// both the data descriptor and, with a zero Modified, the timestamp
	// extra field that CreateHeader would add.
	mimetype := []byte("application/epub+zip")
	mw, err := z.CreateRaw(&zip.FileHeader{
		Name:               "mimetype",
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(mimetype),
		CompressedSize64:   uint64(len(mimetype)),
		UncompressedSize64: uint64(len(mimetype)),
	})
	if err != nil {
		return err
	}
	if _, err := mw.Write(mimetype); err != nil {
		return err
	}

	modified := now().UTC()
	add := func(name string, data []byte) error {
		fw, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modified})
		if err != nil {
			return err
		}
		_, err = fw.Write(data)
		return err
	}

	files := []struct {
		name string
		data string
	}{
		{"META-INF/container.xml", containerXML},
		{"OEBPS/content.opf", buildOPF(b, lang, modified)},
		{"OEBPS/nav.xhtml", buildNav(b, lang)},
		{"OEBPS/toc.ncx", buildNCX(b, lang)},
		{"OEBPS/style.css", epubCSS},
	}
	if b.Cover != nil {
		files = append(files, struct {
			name string
			data string
		}{"OEBPS/cover.xhtml", buildCoverPage(b, lang)})
	}
	for _, f := range files {
		if err := add(f.name, []byte(f.data)); err != nil {
			return err
		}
	}
	for i, ch := range b.Chapters {
		if err := add("OEBPS/"+chapterFile(i), []byte(buildChapter(b, ch, lang))); err != nil {
			return err
		}
	}
	// Images are already compressed; deflating them again only costs time.
	addStored := func(img epubImage) error {
		fw, err := z.CreateHeader(&zip.FileHeader{Name: "OEBPS/images/" + img.Name, Method: zip.Store, Modified: modified})
		if err != nil {
			return err
		}
		_, err = fw.Write(img.Data)
		return err
	}
	if b.Cover != nil {
		if err := addStored(*b.Cover); err != nil {
			return err
		}
	}
	for _, img := range b.Images {
		if err := addStored(img); err != nil {
			return err
		}
	}
	return z.Close()
}

func validateEPUB(b *epubBook) error {
	if b == nil {
		return errors.New("epub: nil book")
	}
	if strings.TrimSpace(b.Meta.Identifier) == "" {
		return errors.New("epub: empty identifier")
	}
	if strings.TrimSpace(b.Meta.Title) == "" {
		return errors.New("epub: empty title")
	}
	if len(b.Chapters) == 0 {
		return errors.New("epub: no chapters")
	}
	if len(b.Meta.AuthorsSort) != 0 && len(b.Meta.AuthorsSort) != len(b.Meta.Authors) {
		return fmt.Errorf("epub: %d sort names for %d authors", len(b.Meta.AuthorsSort), len(b.Meta.Authors))
	}
	if d := b.Meta.Date; d != "" && !validW3CDate(d) {
		return fmt.Errorf("epub: date %q is not YYYY, YYYY-MM or YYYY-MM-DD", d)
	}

	seen := make(map[string]bool)
	check := func(img epubImage) error {
		if err := validateImage(img); err != nil {
			return err
		}
		// Names are compared case-insensitively because ZIP entries that
		// differ only in case collide on case-insensitive file systems.
		key := strings.ToLower(img.Name)
		if seen[key] {
			return fmt.Errorf("epub: duplicate image name %q", img.Name)
		}
		seen[key] = true
		return nil
	}
	if b.Cover != nil {
		if err := check(*b.Cover); err != nil {
			return err
		}
	}
	for _, img := range b.Images {
		if err := check(img); err != nil {
			return err
		}
	}

	for i, ch := range b.Chapters {
		if err := checkXHTMLFragment(ch.Body); err != nil {
			return fmt.Errorf("epub: chapter %d body: %w", i+1, err)
		}
	}
	return nil
}

func validateImage(img epubImage) error {
	if img.Name == "" {
		return errors.New("epub: image with empty name")
	}
	// The name becomes both a ZIP path and a URL in the manifest; limiting
	// it to a safe set avoids percent-encoding and path traversal.
	for _, r := range img.Name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("epub: image name %q may only contain letters, digits, '.', '_' and '-'", img.Name)
		}
	}
	lower := strings.ToLower(img.Name)
	switch img.MediaType {
	case "image/jpeg":
		if !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") {
			return fmt.Errorf("epub: JPEG image %q needs a .jpg or .jpeg extension", img.Name)
		}
		if !bytes.HasPrefix(img.Data, []byte{0xFF, 0xD8, 0xFF}) {
			return fmt.Errorf("epub: image %q is not JPEG data", img.Name)
		}
	case "image/png":
		if !strings.HasSuffix(lower, ".png") {
			return fmt.Errorf("epub: PNG image %q needs a .png extension", img.Name)
		}
		if !bytes.HasPrefix(img.Data, []byte("\x89PNG\r\n\x1a\n")) {
			return fmt.Errorf("epub: image %q is not PNG data", img.Name)
		}
	default:
		return fmt.Errorf("epub: image %q has unsupported media type %q", img.Name, img.MediaType)
	}
	return nil
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

func validW3CDate(s string) bool {
	digits := func(s string) bool {
		for _, r := range s {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	switch len(s) {
	case 4:
		return digits(s)
	case 7:
		return digits(s[:4]) && s[4] == '-' && digits(s[5:])
	case 10:
		return digits(s[:4]) && s[4] == '-' && digits(s[5:7]) && s[7] == '-' && digits(s[8:])
	}
	return false
}

// xmlEscape escapes s for XML text and attribute values. Characters that
// XML 1.0 forbids (most C0 controls, which OCR output sometimes contains)
// are dropped, since no escape can make them legal.
func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			if r == utf8.RuneError || !validXMLChar(r) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validXMLChar(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D ||
		r >= 0x20 && r <= 0xD7FF ||
		r >= 0xE000 && r <= 0xFFFD ||
		r >= 0x10000 && r <= 0x10FFFF
}

func chapterFile(i int) string {
	return fmt.Sprintf("chapter%03d.xhtml", i+1)
}

func chapterID(i int) string {
	return fmt.Sprintf("chapter%03d", i+1)
}

func imageID(i int) string {
	return fmt.Sprintf("img%03d", i+1)
}

const containerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

func buildOPF(b *epubBook, lang string, modified time.Time) string {
	m := b.Meta
	var s strings.Builder
	s.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&s, `<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="%s">`+"\n", xmlEscape(lang))
	s.WriteString(`  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">` + "\n")

	fmt.Fprintf(&s, "    <dc:identifier id=\"bookid\">%s</dc:identifier>\n", xmlEscape(m.Identifier))
	for i, isbn := range m.ISBNs {
		fmt.Fprintf(&s, "    <dc:identifier id=\"isbn%d\">urn:isbn:%s</dc:identifier>\n", i+1, xmlEscape(isbn))
	}

	fmt.Fprintf(&s, "    <dc:title id=\"title\">%s</dc:title>\n", xmlEscape(m.Title))
	if m.Subtitle != "" {
		s.WriteString("    <meta refines=\"#title\" property=\"title-type\">main</meta>\n")
		fmt.Fprintf(&s, "    <dc:title id=\"subtitle\">%s</dc:title>\n", xmlEscape(m.Subtitle))
		s.WriteString("    <meta refines=\"#subtitle\" property=\"title-type\">subtitle</meta>\n")
	}

	for i, a := range m.Authors {
		id := fmt.Sprintf("creator%d", i+1)
		fmt.Fprintf(&s, "    <dc:creator id=\"%s\">%s</dc:creator>\n", id, xmlEscape(a))
		fmt.Fprintf(&s, "    <meta refines=\"#%s\" property=\"role\" scheme=\"marc:relators\">aut</meta>\n", id)
		if len(m.AuthorsSort) > 0 && m.AuthorsSort[i] != "" {
			fmt.Fprintf(&s, "    <meta refines=\"#%s\" property=\"file-as\">%s</meta>\n", id, xmlEscape(m.AuthorsSort[i]))
		}
	}
	for _, c := range m.Contributors {
		fmt.Fprintf(&s, "    <dc:contributor>%s</dc:contributor>\n", xmlEscape(c))
	}

	fmt.Fprintf(&s, "    <dc:language>%s</dc:language>\n", xmlEscape(lang))
	if m.Publisher != "" {
		fmt.Fprintf(&s, "    <dc:publisher>%s</dc:publisher>\n", xmlEscape(m.Publisher))
	}
	if m.Date != "" {
		fmt.Fprintf(&s, "    <dc:date>%s</dc:date>\n", xmlEscape(m.Date))
	}
	for _, subj := range m.Subjects {
		if subj != "" {
			fmt.Fprintf(&s, "    <dc:subject>%s</dc:subject>\n", xmlEscape(subj))
		}
	}
	if m.Description != "" {
		fmt.Fprintf(&s, "    <dc:description>%s</dc:description>\n", xmlEscape(m.Description))
	}
	if m.Source != "" {
		fmt.Fprintf(&s, "    <dc:source>%s</dc:source>\n", xmlEscape(m.Source))
	}
	fmt.Fprintf(&s, "    <meta property=\"dcterms:modified\">%s</meta>\n", modified.Format("2006-01-02T15:04:05Z"))
	if b.Cover != nil {
		// EPUB 2 cover convention, which Calibre and older readers use.
		s.WriteString("    <meta name=\"cover\" content=\"cover-image\"/>\n")
	}
	s.WriteString("  </metadata>\n")

	s.WriteString("  <manifest>\n")
	s.WriteString("    <item id=\"nav\" href=\"nav.xhtml\" media-type=\"application/xhtml+xml\" properties=\"nav\"/>\n")
	s.WriteString("    <item id=\"ncx\" href=\"toc.ncx\" media-type=\"application/x-dtbncx+xml\"/>\n")
	s.WriteString("    <item id=\"css\" href=\"style.css\" media-type=\"text/css\"/>\n")
	if b.Cover != nil {
		fmt.Fprintf(&s, "    <item id=\"cover-image\" href=\"images/%s\" media-type=\"%s\" properties=\"cover-image\"/>\n", b.Cover.Name, b.Cover.MediaType)
		s.WriteString("    <item id=\"cover\" href=\"cover.xhtml\" media-type=\"application/xhtml+xml\"/>\n")
	}
	for i := range b.Chapters {
		fmt.Fprintf(&s, "    <item id=\"%s\" href=\"%s\" media-type=\"application/xhtml+xml\"/>\n", chapterID(i), chapterFile(i))
	}
	for i, img := range b.Images {
		fmt.Fprintf(&s, "    <item id=\"%s\" href=\"images/%s\" media-type=\"%s\"/>\n", imageID(i), img.Name, img.MediaType)
	}
	s.WriteString("  </manifest>\n")

	s.WriteString("  <spine toc=\"ncx\">\n")
	if b.Cover != nil {
		s.WriteString("    <itemref idref=\"cover\"/>\n")
	}
	for i := range b.Chapters {
		fmt.Fprintf(&s, "    <itemref idref=\"%s\"/>\n", chapterID(i))
	}
	s.WriteString("  </spine>\n")

	// The guide is the EPUB 2 counterpart of the nav landmarks.
	s.WriteString("  <guide>\n")
	if b.Cover != nil {
		s.WriteString("    <reference type=\"cover\" title=\"Cover\" href=\"cover.xhtml\"/>\n")
	}
	fmt.Fprintf(&s, "    <reference type=\"text\" title=\"%s\" href=\"%s\"/>\n", xmlEscape(m.Title), chapterFile(0))
	s.WriteString("  </guide>\n")
	s.WriteString("</package>\n")
	return s.String()
}

// tocEntry is one table-of-contents line, shared by nav.xhtml and toc.ncx.
type tocEntry struct {
	title string
	href  string
}

// tocEntries lists titled chapters. Both nav and NCX must contain at least
// one entry, so a book without chapter titles gets one pointing at its start.
func tocEntries(b *epubBook) []tocEntry {
	var entries []tocEntry
	for i, ch := range b.Chapters {
		if ch.Title != "" {
			entries = append(entries, tocEntry{title: ch.Title, href: chapterFile(i)})
		}
	}
	if len(entries) == 0 {
		entries = append(entries, tocEntry{title: b.Meta.Title, href: chapterFile(0)})
	}
	return entries
}

// xhtmlHead always declares the epub namespace so chapter bodies may use
// epub:type (footnotes, page breaks) without the caller declaring it.
func xhtmlHead(s *strings.Builder, lang, title string) {
	s.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE html>\n")
	s.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"`)
	fmt.Fprintf(s, " xml:lang=\"%s\" lang=\"%s\">\n", xmlEscape(lang), xmlEscape(lang))
	fmt.Fprintf(s, "<head>\n  <meta charset=\"utf-8\"/>\n  <title>%s</title>\n", xmlEscape(title))
	s.WriteString("  <link rel=\"stylesheet\" type=\"text/css\" href=\"style.css\"/>\n</head>\n")
}

func buildNav(b *epubBook, lang string) string {
	var s strings.Builder
	xhtmlHead(&s, lang, b.Meta.Title)
	s.WriteString("<body>\n  <nav epub:type=\"toc\" id=\"toc\">\n")
	fmt.Fprintf(&s, "    <h1>%s</h1>\n    <ol>\n", xmlEscape(b.Meta.Title))
	for _, e := range tocEntries(b) {
		fmt.Fprintf(&s, "      <li><a href=\"%s\">%s</a></li>\n", e.href, xmlEscape(e.title))
	}
	s.WriteString("    </ol>\n  </nav>\n")
	s.WriteString("  <nav epub:type=\"landmarks\" id=\"landmarks\" hidden=\"hidden\">\n    <ol>\n")
	if b.Cover != nil {
		s.WriteString("      <li><a epub:type=\"cover\" href=\"cover.xhtml\">Cover</a></li>\n")
	}
	fmt.Fprintf(&s, "      <li><a epub:type=\"bodymatter\" href=\"%s\">%s</a></li>\n", chapterFile(0), xmlEscape(b.Meta.Title))
	s.WriteString("    </ol>\n  </nav>\n</body>\n</html>\n")
	return s.String()
}

func buildNCX(b *epubBook, lang string) string {
	var s strings.Builder
	s.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&s, `<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1" xml:lang="%s">`+"\n", xmlEscape(lang))
	s.WriteString("  <head>\n")
	// dtb:uid must equal the package's unique identifier.
	fmt.Fprintf(&s, "    <meta name=\"dtb:uid\" content=\"%s\"/>\n", xmlEscape(b.Meta.Identifier))
	s.WriteString("    <meta name=\"dtb:depth\" content=\"1\"/>\n")
	s.WriteString("    <meta name=\"dtb:totalPageCount\" content=\"0\"/>\n")
	s.WriteString("    <meta name=\"dtb:maxPageNumber\" content=\"0\"/>\n")
	s.WriteString("  </head>\n")
	fmt.Fprintf(&s, "  <docTitle><text>%s</text></docTitle>\n", xmlEscape(b.Meta.Title))
	s.WriteString("  <navMap>\n")
	for i, e := range tocEntries(b) {
		fmt.Fprintf(&s, "    <navPoint id=\"navpoint%d\" playOrder=\"%d\">\n", i+1, i+1)
		fmt.Fprintf(&s, "      <navLabel><text>%s</text></navLabel>\n", xmlEscape(e.title))
		fmt.Fprintf(&s, "      <content src=\"%s\"/>\n    </navPoint>\n", e.href)
	}
	s.WriteString("  </navMap>\n</ncx>\n")
	return s.String()
}

func buildCoverPage(b *epubBook, lang string) string {
	var s strings.Builder
	xhtmlHead(&s, lang, b.Meta.Title)
	s.WriteString("<body epub:type=\"cover\">\n")
	fmt.Fprintf(&s, "  <div class=\"cover\"><img src=\"images/%s\" alt=\"%s\"/></div>\n", b.Cover.Name, xmlEscape(b.Meta.Title))
	s.WriteString("</body>\n</html>\n")
	return s.String()
}

func buildChapter(b *epubBook, ch epubChapter, lang string) string {
	// An empty <title> is reported by epubcheck, so untitled chapters
	// borrow the book title.
	title := ch.Title
	if title == "" {
		title = b.Meta.Title
	}
	var s strings.Builder
	xhtmlHead(&s, lang, title)
	s.WriteString("<body>\n")
	s.WriteString(ch.Body)
	s.WriteString("\n</body>\n</html>\n")
	return s.String()
}
