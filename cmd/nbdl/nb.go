package main

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
)

const (
	manifestBase = "https://api.nb.no/catalog/v1/iiif/"
	tokenURL     = "https://www.nb.no/nbssotoken"
	userAgent    = "NB.no-Downloader (+https://github.com/alcxyz/NB.no-Downloader)"
)

var (
	urnPattern    = regexp.MustCompile(`URN:NBN:[A-Za-z0-9_\-.]+`)
	itemIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	itemsURLPath  = regexp.MustCompile(`/items/([0-9a-f]{32})`)
)

// errAuth marks a page that the server refused to serve, which usually means
// missing or expired cookies.
var errAuth = errors.New("access denied (401/403): check your login")

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

// resolveIdentifier turns user input into an identifier the IIIF API
// accepts: a full URN, a 32-character item id, or a bare id combined with
// the document type.
func resolveIdentifier(input, docType string) (string, error) {
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

// outputStem picks the default output name for an identifier, keeping the
// historical "<id>.pdf" name for bare ids.
func outputStem(input, identifier string) string {
	input = strings.TrimSpace(input)
	if !strings.ContainsAny(input, "/:?#& ") {
		return input
	}
	stem := strings.TrimPrefix(identifier, "URN:NBN:no-nb_")
	return strings.NewReplacer(":", "_", "/", "_").Replace(stem)
}

type downloader struct {
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

func (d *downloader) get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
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
func (d *downloader) accessToken(ctx context.Context) (string, error) {
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

// errNotFound marks a 404, which retrying will not fix.
var errNotFound = errors.New("not found")

// errUnauthorized is a 401: unlike nb.no's random 403s it is final.
var errUnauthorized = fmt.Errorf("%w (401)", errAuth)

// fetch GETs rawURL and returns the body, retrying transient failures.
// nb.no intermittently answers valid requests with 403, sometimes as an HTML
// error page with status 200, so 403s and responses of the wrong content
// type are retried too; a 403 that persists is reported as errAuth.
func (d *downloader) fetch(ctx context.Context, rawURL, wantType string) ([]byte, error) {
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
		if errors.Is(err, errNotFound) || errors.Is(err, errUnauthorized) || ctx.Err() != nil {
			return nil, err
		}
		if attempt < d.retries {
			d.retried.Add(1)
		}
		lastErr = err
	}
	if errors.Is(lastErr, errAuth) {
		return nil, lastErr
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", d.retries+1, lastErr)
}

func (d *downloader) tryFetch(ctx context.Context, rawURL, wantType string) ([]byte, error) {
	resp, err := d.get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", errNotFound, rawURL)
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return nil, errAuth
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, rawURL)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, wantType) {
		return nil, fmt.Errorf("got %q instead of %s from %s", ct, wantType, rawURL)
	}
	return io.ReadAll(resp.Body)
}

// fetchBook downloads and parses the IIIF presentation manifest.
func (d *downloader) fetchBook(ctx context.Context, identifier string) (*Book, error) {
	body, err := d.fetch(ctx, d.manifest+url.PathEscape(identifier)+"/manifest", "application/json")
	if errors.Is(err, errNotFound) {
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

// imageURL builds the IIIF image request for a page at the requested width;
// width <= 0 or wider than the original asks for full resolution.
func imageURL(p Page, width int) string {
	size := "full"
	if width > 0 && (p.Width == 0 || width < p.Width) {
		size = fmt.Sprintf("%d,", width)
	}
	return p.Service + "/full/" + size + "/0/default.jpg"
}

func pageFile(dir string, index int) string {
	return filepath.Join(dir, fmt.Sprintf("%04d.jpg", index+1))
}

// downloadPages fetches every page into dir with a pool of workers. Pages
// already present in dir are kept, so an interrupted run resumes.
func (d *downloader) downloadPages(ctx context.Context, book *Book, dir string, width, workers int, progress func(done, total int)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return parallel(ctx, len(book.Pages), workers, progress, func(ctx context.Context, i int) error {
		path := pageFile(dir, i)
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return nil
		}
		if err := d.downloadPage(ctx, imageURL(book.Pages[i], width), path); err != nil {
			return fmt.Errorf("page %d (%s): %w", i+1, book.Pages[i].Label, err)
		}
		return nil
	})
}

// parallel runs fn for 0..n-1 on a pool of workers and stops at the first
// error.
func parallel(ctx context.Context, n, workers int, progress func(done, total int), fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int)
	var (
		mu       sync.Mutex
		done     int
		firstErr error
		wg       sync.WaitGroup
	)
	finish := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if firstErr == nil {
				firstErr = err
				cancel()
			}
			return
		}
		done++
		if progress != nil {
			progress(done, n)
		}
	}

	for w := 0; w < max(workers, 1); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				finish(fn(ctx, i))
			}
		}()
	}

feed:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// downloadPage fetches one image and writes it atomically to path.
func (d *downloader) downloadPage(ctx context.Context, rawURL, path string) error {
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
