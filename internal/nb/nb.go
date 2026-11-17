// Package nb talks to the Norwegian National Library (nb.no): it resolves
// book identifiers, reads IIIF manifests and catalogue records, and fetches
// page images and ALTO text.
package nb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alcxyz/NB.no-Downloader/internal/parallel"
)

const (
	manifestBase = "https://api.nb.no/catalog/v1/iiif/"
	tokenURL     = "https://www.nb.no/nbssotoken"
	altoBase     = "https://api.nb.no/catalog/v1/metadata/"
	UserAgent    = "NB.no-Downloader (+https://github.com/alcxyz/NB.no-Downloader)"
)

var (
	urnPattern    = regexp.MustCompile(`URN:NBN:[A-Za-z0-9_\-.]+`)
	itemIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	itemsURLPath  = regexp.MustCompile(`/items/([0-9a-f]{32})`)
)

// ErrAuth marks a page that the server refused to serve, which usually means
// missing or expired cookies.
var ErrAuth = errors.New("access denied (401/403): check your login")

// Page is one canvas of a book, in reading order.
type Page struct {
	ID      string // e.g. URN:NBN:no-nb_digibok_2008010300041_0001
	Label   string
	Service string // IIIF image service base URL
	Width   int    // full-resolution width in pixels
	Height  int
	// InchesPerPixel is the physical scale reported by the library, or 0.
	InchesPerPixel float64
}

// Book is the parsed IIIF manifest of an item.
type Book struct {
	Title string
	URN   string // from the page ids, e.g. URN:NBN:no-nb_digibok_2008010300041
	Pages []Page
}

// ResolveIdentifier turns user input into an identifier the IIIF API
// accepts: a full URN, a 32-character item id, or a bare id combined with
// the document type.
func ResolveIdentifier(input, docType string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("empty book id")
	}
	if urn := urnPattern.FindString(input); urn != "" {
		return urn, nil
	}
	if m := itemsURLPath.FindStringSubmatch(input); m != nil {
		return m[1], nil
	}
	if itemIDPattern.MatchString(input) {
		return input, nil
	}
	if strings.ContainsAny(input, "/:?#& ") {
		return "", fmt.Errorf("cannot find a book id in %q", input)
	}
	if docType == "" {
		docType = "digibok"
	}
	return "URN:NBN:no-nb_" + docType + "_" + input, nil
}

// OutputStem picks the default output name for an identifier, keeping the
// historical "<id>.pdf" name for bare ids.
func OutputStem(input, identifier string) string {
	input = strings.TrimSpace(input)
	if !strings.ContainsAny(input, "/:?#& ") {
		return input
	}
	stem := strings.TrimPrefix(identifier, "URN:NBN:no-nb_")
	return strings.NewReplacer(":", "_", "/", "_").Replace(stem)
}

// Downloader fetches books, page images, text and catalogue records from
// nb.no.
type Downloader struct {
	client     *http.Client
	authDomain string // the access token goes only to this domain
	manifest   string // manifest URL; overridable in tests
	catalog    string // catalogue record URL; overridable in tests
	alto       string // ALTO text URL; overridable in tests
	retries    int
	retryDelay time.Duration

	// tokenURL exchanges the SSO cookie for an access token, which nb.no
	// expects on API and image requests from logged-in users. Empty when
	// not logged in.
	tokenURL string
	tokenMu  sync.Mutex
	token    string
	tokenExp time.Time

	retried atomic.Int64 // failed attempts that were retried
}

// New returns a downloader that sends the given nb.no cookies, and the
// SSO access token when they include a login.
func New(cookies []*http.Cookie) (*Downloader, error) {
	jar, err := newJar(cookies)
	if err != nil {
		return nil, err
	}
	c := Config{
		Client:     &http.Client{Jar: jar, Timeout: 2 * time.Minute},
		AuthDomain: "nb.no",
		Manifest:   manifestBase,
		Retries:    15,
		RetryDelay: 250 * time.Millisecond,
	}
	if HasLogin(cookies) {
		c.TokenURL = tokenURL
	}
	return NewWithConfig(c), nil
}

// Config says where a Downloader finds nb.no's services and how it
// retries; tests point it at a fake server.
type Config struct {
	Client     *http.Client
	AuthDomain string // the access token goes only to this domain
	Manifest   string // manifest URL
	Catalog    string // catalogue record URL; empty for nb.no's
	ALTO       string // ALTO text URL; empty for nb.no's
	TokenURL   string // exchanges the SSO cookie for an access token; empty when not logged in
	Retries    int
	RetryDelay time.Duration
}

// NewWithConfig returns a downloader set up as c says.
func NewWithConfig(c Config) *Downloader {
	return &Downloader{
		client:     c.Client,
		authDomain: c.AuthDomain,
		manifest:   c.Manifest,
		catalog:    c.Catalog,
		alto:       c.ALTO,
		retries:    c.Retries,
		retryDelay: c.RetryDelay,
		tokenURL:   c.TokenURL,
	}
}

// Retried returns how many failed requests were retried.
func (d *Downloader) Retried() int64 {
	return d.retried.Load()
}

func (d *Downloader) get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if d.tokenURL != "" && rawURL != d.tokenURL && hostWithin(req.URL.Hostname(), d.authDomain) {
		token, err := d.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return d.client.Do(req)
}

// accessToken returns a cached access token, renewing it shortly before it
// expires.
func (d *Downloader) accessToken(ctx context.Context) (string, error) {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	if d.token != "" && time.Now().Before(d.tokenExp) {
		return d.token, nil
	}
	resp, err := d.get(ctx, d.tokenURL)
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get access token: HTTP %d", resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}
	if t.AccessToken == "" {
		return "", errors.New("get access token: empty token")
	}
	d.token = t.AccessToken
	d.tokenExp = time.Now().Add(time.Duration(t.ExpiresIn)*time.Second - 30*time.Second)
	return d.token, nil
}

// ErrNotFound marks a 404, which retrying will not fix.
var ErrNotFound = errors.New("not found")

// errUnauthorized is a 401: unlike nb.no's random 403s it is final.
var errUnauthorized = fmt.Errorf("%w (401)", ErrAuth)

// fetch GETs rawURL and returns the body, retrying transient failures.
// nb.no intermittently answers valid requests with 403, sometimes as an HTML
// error page with status 200, so 403s and responses of the wrong content
// type are retried too; a 403 that persists is reported as ErrAuth.
func (d *Downloader) fetch(ctx context.Context, rawURL, wantType string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= d.retries; attempt++ {
		if attempt > 0 {
			delay := d.retryDelay << min(attempt-1, 3) // back off up to 8x
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		body, err := d.tryFetch(ctx, rawURL, wantType)
		if err == nil {
			return body, nil
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, errUnauthorized) || ctx.Err() != nil {
			return nil, err
		}
		if attempt < d.retries {
			d.retried.Add(1)
		}
		lastErr = err
	}
	if errors.Is(lastErr, ErrAuth) {
		return nil, lastErr
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", d.retries+1, lastErr)
}

func (d *Downloader) tryFetch(ctx context.Context, rawURL, wantType string) ([]byte, error) {
	resp, err := d.get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, rawURL)
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return nil, ErrAuth
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, rawURL)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, wantType) {
		return nil, fmt.Errorf("got %q instead of %s from %s", ct, wantType, rawURL)
	}
	return io.ReadAll(resp.Body)
}

// FetchBook downloads and parses the IIIF presentation manifest.
func (d *Downloader) FetchBook(ctx context.Context, identifier string) (*Book, error) {
	body, err := d.fetch(ctx, d.manifest+url.PathEscape(identifier)+"/manifest", "application/json")
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("no item found for %s", identifier)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	return parseManifest(bytes.NewReader(body))
}

type iiifManifest struct {
	Label     json.RawMessage `json:"label"`
	Sequences []struct {
		Canvases []struct {
			Label  json.RawMessage `json:"label"`
			Width  int             `json:"width"`
			Height int             `json:"height"`
			Images []struct {
				Resource struct {
					Service struct {
						ID      string `json:"@id"`
						Service struct {
							PhysicalScale float64 `json:"physicalScale"`
							PhysicalUnits string  `json:"physicalUnits"`
						} `json:"service"`
					} `json:"service"`
				} `json:"resource"`
			} `json:"images"`
		} `json:"canvases"`
	} `json:"sequences"`
}

func parseManifest(r io.Reader) (*Book, error) {
	var m iiifManifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if len(m.Sequences) == 0 {
		return nil, errors.New("manifest has no page sequence")
	}
	book := &Book{Title: labelString(m.Label)}
	for i, c := range m.Sequences[0].Canvases {
		if len(c.Images) == 0 || c.Images[0].Resource.Service.ID == "" {
			return nil, fmt.Errorf("manifest canvas %d has no image service", i+1)
		}
		svc := c.Images[0].Resource.Service
		service := strings.TrimSuffix(svc.ID, "/")
		p := Page{
			ID:      service[strings.LastIndex(service, "/")+1:],
			Label:   labelString(c.Label),
			Service: service,
			Width:   c.Width,
			Height:  c.Height,
		}
		switch svc.Service.PhysicalUnits {
		case "in":
			p.InchesPerPixel = svc.Service.PhysicalScale
		case "cm":
			p.InchesPerPixel = svc.Service.PhysicalScale / 2.54
		case "mm":
			p.InchesPerPixel = svc.Service.PhysicalScale / 25.4
		}
		book.Pages = append(book.Pages, p)
	}
	if len(book.Pages) == 0 {
		return nil, errors.New("manifest lists no pages")
	}
	if id := book.Pages[0].ID; strings.HasPrefix(id, "URN:NBN:") && strings.Contains(id, "_") {
		book.URN = id[:strings.LastIndex(id, "_")]
	}
	return book, nil
}

// labelString accepts the IIIF v2 label forms: a string, or a list of
// strings or {"@value": ...} objects.
func labelString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		if json.Unmarshal(list[0], &s) == nil {
			return s
		}
		var v struct {
			Value string `json:"@value"`
		}
		if json.Unmarshal(list[0], &v) == nil {
			return v.Value
		}
	}
	return ""
}

// ImageURL builds the IIIF image request for a page at the requested width;
// width <= 0 or wider than the original asks for full resolution.
func ImageURL(p Page, width int) string {
	size := "full"
	if width > 0 && (p.Width == 0 || width < p.Width) {
		size = fmt.Sprintf("%d,", width)
	}
	return p.Service + "/full/" + size + "/0/default.jpg"
}

// PageFile is where page index is kept in dir.
func PageFile(dir string, index int) string {
	return filepath.Join(dir, fmt.Sprintf("%04d.jpg", index+1))
}

// DownloadPages fetches every page into dir with a pool of workers. Pages
// already present in dir are kept, so an interrupted run resumes.
func (d *Downloader) DownloadPages(ctx context.Context, book *Book, dir string, width, workers int, progress func(done, total int)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return parallel.Run(ctx, len(book.Pages), workers, progress, func(ctx context.Context, i int) error {
		path := PageFile(dir, i)
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return nil
		}
		if err := d.DownloadPage(ctx, ImageURL(book.Pages[i], width), path); err != nil {
			return fmt.Errorf("page %d (%s): %w", i+1, book.Pages[i].Label, err)
		}
		return nil
	})
}

// DownloadPage fetches one image and writes it atomically to path.
func (d *Downloader) DownloadPage(ctx context.Context, rawURL, path string) error {
	data, err := d.fetch(ctx, rawURL, "image/jpeg")
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// CachedImage fetches an image once into path.
func (d *Downloader) CachedImage(ctx context.Context, path, rawURL string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return data, nil
	}
	data, err := d.fetch(ctx, rawURL, "image/jpeg")
	if err != nil {
		return nil, err
	}
	return data, writeFileAtomic(path, data)
}

// FetchALTO fetches nb.no's ALTO text of one page. nb.no answers 401 for
// books whose text it does not publish.
func (d *Downloader) FetchALTO(ctx context.Context, urn, pageID string) ([]byte, error) {
	return d.fetch(ctx, d.altoURL(urn, pageID), "application/xml")
}

func (d *Downloader) altoURL(urn, pageID string) string {
	base := d.alto
	if base == "" {
		base = altoBase
	}
	return base + url.PathEscape(urn) + "/altos/" + url.PathEscape(pageID)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
