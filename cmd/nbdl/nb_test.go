package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResolveIdentifier(t *testing.T) {
	tests := []struct {
		input, docType, want string
	}{
		{"2008010300041", "digibok", "URN:NBN:no-nb_digibok_2008010300041"},
		{"000040863", "pliktmonografi", "URN:NBN:no-nb_pliktmonografi_000040863"},
		{"URN:NBN:no-nb_digibok_2008010300041", "", "URN:NBN:no-nb_digibok_2008010300041"},
		{"https://www.nb.no/items/URN:NBN:no-nb_digibok_2008010300041?page=5", "", "URN:NBN:no-nb_digibok_2008010300041"},
		{"https://www.nb.no/items/296c0829d64f7ba7b3a684d912920284", "", "296c0829d64f7ba7b3a684d912920284"},
		{"296c0829d64f7ba7b3a684d912920284", "", "296c0829d64f7ba7b3a684d912920284"},
	}
	for _, tt := range tests {
		got, err := resolveIdentifier(tt.input, tt.docType)
		if err != nil || got != tt.want {
			t.Errorf("resolveIdentifier(%q, %q) = %q, %v; want %q", tt.input, tt.docType, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "https://www.nb.no/search?q=ibsen"} {
		if got, err := resolveIdentifier(bad, "digibok"); err == nil {
			t.Errorf("resolveIdentifier(%q) = %q, want error", bad, got)
		}
	}
}

func TestOutputStem(t *testing.T) {
	if got := outputStem("2008010300041", "URN:NBN:no-nb_digibok_2008010300041"); got != "2008010300041" {
		t.Errorf("bare id: got %q", got)
	}
	if got := outputStem("https://www.nb.no/items/URN:NBN:no-nb_digibok_1", "URN:NBN:no-nb_digibok_1"); got != "digibok_1" {
		t.Errorf("URL: got %q", got)
	}
}

const testManifest = `{
  "label": "Gengangere",
  "sequences": [{"canvases": [
    {"label": "C1", "width": 1000, "height": 1500, "images": [{"resource": {"service": {
      "@id": "%[1]s/img/C1", "service": {"physicalScale": 0.005, "physicalUnits": "in"}}}}]},
    {"label": ["1"], "width": 1000, "height": 1500, "images": [{"resource": {"service": {"@id": "%[1]s/img/0001"}}}]},
    {"label": [{"@value": "2"}], "width": 1000, "height": 1500, "images": [{"resource": {"service": {"@id": "%[1]s/img/0002/"}}}]}
  ]}]
}`

func TestParseManifest(t *testing.T) {
	book, err := parseManifest(strings.NewReader(fmt.Sprintf(testManifest, "https://x")))
	if err != nil {
		t.Fatal(err)
	}
	if book.Title != "Gengangere" || len(book.Pages) != 3 {
		t.Fatalf("got title %q, %d pages", book.Title, len(book.Pages))
	}
	labels := []string{book.Pages[0].Label, book.Pages[1].Label, book.Pages[2].Label}
	if strings.Join(labels, ",") != "C1,1,2" {
		t.Errorf("labels = %v", labels)
	}
	if book.Pages[2].Service != "https://x/img/0002" {
		t.Errorf("service = %q, want trailing slash trimmed", book.Pages[2].Service)
	}
	if book.Pages[0].InchesPerPixel != 0.005 || book.Pages[1].InchesPerPixel != 0 {
		t.Errorf("scale = %v, %v", book.Pages[0].InchesPerPixel, book.Pages[1].InchesPerPixel)
	}
	if _, err := parseManifest(strings.NewReader(`{"label": "metadata only"}`)); err == nil {
		t.Error("manifest without sequences: want error")
	}
}

func TestImageURL(t *testing.T) {
	p := Page{Service: "https://x/img/1", Width: 2000}
	for width, want := range map[int]string{
		0:    "https://x/img/1/full/full/0/default.jpg",
		1200: "https://x/img/1/full/1200,/0/default.jpg",
		3000: "https://x/img/1/full/full/0/default.jpg",
	} {
		if got := imageURL(p, width); got != want {
			t.Errorf("imageURL(%d) = %q, want %q", width, got, want)
		}
	}
}

func TestParseCookies(t *testing.T) {
	header := parseCookies("Cookie: nbsso=abc; _nblb=x=y; ; bad")
	if len(header) != 2 || header[0].Name != "nbsso" || header[1].Value != "x=y" || header[0].Domain != ".nb.no" {
		t.Errorf("header format: %+v", header)
	}
	netscape := parseCookies("# Netscape HTTP Cookie File\n" +
		".nb.no\tTRUE\t/\tTRUE\t0\tnbsso\tabc\n" +
		"#HttpOnly_www.nb.no\tFALSE\t/auth\tTRUE\t0\tJSESSIONID\tj\n" +
		".example.com\tTRUE\t/\tFALSE\t0\tother\tz\n")
	if len(netscape) != 2 {
		t.Fatalf("netscape: got %d cookies, want 2 (other domains dropped)", len(netscape))
	}
	if c := netscape[1]; c.Domain != "www.nb.no" || c.Path != "/auth" {
		t.Errorf("host-only cookie: %+v", c)
	}
	if !hasLogin(netscape) {
		t.Error("hasLogin = false, want true")
	}
}

func TestNewJarScopesCookies(t *testing.T) {
	jar, err := newJar([]*http.Cookie{
		{Name: "all", Value: "1", Domain: ".nb.no", Path: "/"},
		{Name: "www", Value: "2", Domain: "www.nb.no", Path: "/"},
		{Name: "auth", Value: "3", Domain: "www.nb.no", Path: "/auth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	names := func(raw string) string {
		u, _ := url.Parse(raw)
		var n []string
		for _, c := range jar.Cookies(u) {
			n = append(n, c.Name)
		}
		return strings.Join(n, ",")
	}
	if got := names("https://api.nb.no/catalog"); got != "all" {
		t.Errorf("api.nb.no gets %q, want all", got)
	}
	if got := names("https://www.nb.no/services/image"); got != "www,all" && got != "all,www" {
		t.Errorf("www.nb.no/services gets %q", got)
	}
	if got := names("https://www.nb.no/auth/login"); !strings.Contains(got, "auth") {
		t.Errorf("www.nb.no/auth gets %q, want auth included", got)
	}
}

func testJPEG(t *testing.T, w, h int, gray bool) []byte {
	t.Helper()
	var img image.Image
	if gray {
		g := image.NewGray(image.Rect(0, 0, w, h))
		g.SetGray(1, 1, color.Gray{Y: 200})
		img = g
	} else {
		rgba := image.NewRGBA(image.Rect(0, 0, w, h))
		rgba.Set(1, 1, color.RGBA{R: 200, A: 255})
		img = rgba
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeNB serves a manifest, a token endpoint and page images. Each page
// fails its first requests the way nb.no does (a 403, then an HTML error
// page with status 200) before serving the image.
type fakeNB struct {
	t        *testing.T
	jpeg     []byte
	failures int  // failed responses per page before success
	deny     bool // always answer 403

	mu       sync.Mutex
	attempts map[string]int
	tokens   int
	authSeen map[string]bool
}

func (f *fakeNB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/token":
		f.tokens++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token": "tok%d", "expires_in": 300}`, f.tokens)
	case strings.HasPrefix(r.URL.Path, "/iiif/"):
		w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		fmt.Fprintf(w, testManifest, "http://"+r.Host)
	case strings.HasPrefix(r.URL.Path, "/img/"):
		page := strings.Split(r.URL.Path, "/")[2]
		if c, err := r.Cookie("nbsso"); err == nil && c.Value == "sso" {
			f.authSeen[page+" cookie"] = true
		}
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok") {
			f.authSeen[page+" token"] = true
		}
		n := f.attempts[page]
		f.attempts[page]++
		switch {
		case f.deny || n < f.failures && n%2 == 0:
			w.WriteHeader(http.StatusForbidden)
		case n < f.failures:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<img src="/errorAssets/403.png"/>`)
		default:
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(f.jpeg)
		}
	default:
		http.NotFound(w, r)
	}
}

func newTestDownloader(t *testing.T, srv *httptest.Server, login bool) *downloader {
	t.Helper()
	var cookies []*http.Cookie
	if login {
		cookies = []*http.Cookie{{Name: "nbsso", Value: "sso", Domain: "127.0.0.1", Path: "/"}}
	}
	jar, err := newJar(cookies)
	if err != nil {
		t.Fatal(err)
	}
	d := &downloader{
		client:     &http.Client{Jar: jar},
		manifest:   srv.URL + "/iiif/",
		authDomain: "127.0.0.1",
		retries:    4,
		retryDelay: time.Millisecond,
	}
	if login {
		d.tokenURL = srv.URL + "/token"
	}
	return d
}

func TestDownloadRetriesAndResumes(t *testing.T) {
	fake := &fakeNB{t: t, jpeg: testJPEG(t, 40, 60, false), failures: 2, attempts: map[string]int{}, authSeen: map[string]bool{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	d := newTestDownloader(t, srv, true)
	ctx := context.Background()

	book, err := d.fetchBook(ctx, "URN:NBN:no-nb_digibok_1")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// A page from an earlier run is kept, not fetched again.
	if err := os.WriteFile(pageFile(dir, 0), fake.jpeg, 0o644); err != nil {
		t.Fatal(err)
	}
	var progress []int
	err = d.downloadPages(ctx, book, dir, 0, 2, func(done, total int) { progress = append(progress, done) })
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) != 3 || progress[2] != 3 {
		t.Errorf("progress = %v", progress)
	}
	if fake.attempts["C1"] != 0 {
		t.Errorf("existing page was downloaded again")
	}
	for _, page := range []string{"0001", "0002"} {
		if fake.attempts[page] != 3 {
			t.Errorf("page %s: %d attempts, want 3", page, fake.attempts[page])
		}
		if !fake.authSeen[page+" cookie"] || !fake.authSeen[page+" token"] {
			t.Errorf("page %s: cookie or token missing", page)
		}
	}
	if got := d.retried.Load(); got != 4 {
		t.Errorf("retried = %d, want 4", got)
	}
	if fake.tokens != 1 {
		t.Errorf("token fetched %d times, want once", fake.tokens)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("%d files in page dir, want 3 (no .part leftovers)", len(entries))
	}
}

func TestDownloadPersistentDenial(t *testing.T) {
	fake := &fakeNB{t: t, jpeg: testJPEG(t, 4, 4, true), deny: true, attempts: map[string]int{}, authSeen: map[string]bool{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	d := newTestDownloader(t, srv, false)
	book, err := d.fetchBook(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	err = d.downloadPages(context.Background(), book, t.TempDir(), 0, 1, nil)
	if !errors.Is(err, errAuth) {
		t.Fatalf("err = %v, want errAuth", err)
	}
	if fake.authSeen["C1 token"] || fake.authSeen["C1 cookie"] {
		t.Error("anonymous download sent credentials")
	}
}

func TestWritePDF(t *testing.T) {
	dir := t.TempDir()
	rgb := filepath.Join(dir, "1.jpg")
	gray := filepath.Join(dir, "2.jpg")
	os.WriteFile(rgb, testJPEG(t, 100, 150, false), 0o644)
	os.WriteFile(gray, testJPEG(t, 200, 100, true), 0o644)

	var buf bytes.Buffer
	err := writePDF(&buf, "Å lykkes", []pdfPage{
		{Path: rgb, InchesPerPixel: 0.01, OriginalWidth: 1000}, // 10 in wide
		{Path: gray},
	})
	if err != nil {
		t.Fatal(err)
	}
	pdf := buf.String()

	if !strings.HasPrefix(pdf, "%PDF-1.4\n") || !strings.HasSuffix(pdf, "%%EOF\n") {
		t.Fatal("missing PDF header or trailer")
	}
	for _, want := range []string{
		"/Count 2",
		"/MediaBox [0 0 720.00 1080.00]", // 10 in at 72 pt/in, image aspect 2:3
		"/MediaBox [0 0 595.28 297.64]",  // no scale: A4 width
		"/ColorSpace /DeviceRGB",
		"/ColorSpace /DeviceGray",
		"/Title <FEFF00C5",
	} {
		if !strings.Contains(pdf, want) {
			t.Errorf("PDF lacks %q", want)
		}
	}

	// Every xref entry must point at the start of its object.
	m := regexp.MustCompile(`(?s)xref\n0 (\d+)\n0000000000 65535 f \n(.*?)trailer`).FindStringSubmatch(pdf)
	if m == nil {
		t.Fatal("no xref table")
	}
	n, _ := strconv.Atoi(m[1])
	entries := strings.Split(strings.TrimSpace(m[2]), "\n")
	if len(entries) != n-1 {
		t.Fatalf("%d xref entries, header says %d", len(entries), n-1)
	}
	for i, e := range entries {
		off, _ := strconv.Atoi(e[:10])
		if want := fmt.Sprintf("%d 0 obj", i+1); !strings.HasPrefix(pdf[off:], want) {
			t.Errorf("xref %d points at %q", i+1, pdf[off:off+12])
		}
	}
	start := regexp.MustCompile(`startxref\n(\d+)`).FindStringSubmatch(pdf)
	if off, _ := strconv.Atoi(start[1]); !strings.HasPrefix(pdf[off:], "xref") {
		t.Error("startxref does not point at xref")
	}
}
