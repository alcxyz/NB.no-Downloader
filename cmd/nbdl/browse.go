package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const startURL = "https://www.nb.no/"

// bindingName is the function the page calls to ask nbdl for a download.
// It goes over the DevTools connection, so nb.no never sees it.
const bindingName = "nbdlDownload"

// buttonScript is buttonJS with the button's label filled in.
func buttonScript(format string) string {
	return strings.Replace(buttonJS, "NBDL_LABEL", strconv.Quote("Download "+strings.ToUpper(format)), 1)
}

// buttonJS adds a download button to nb.no item pages. nb.no is a single
// page app, so it checks the address periodically instead of on load.
// nbdl reports progress through window.nbdlStatus(key, text, done).
const buttonJS = `(() => {
  if (window.top !== window || window.nbdlStatus) return;
  const state = {};
  let btn;
  const itemKey = () => {
    const m = location.pathname.match(/^\/items\/([^/?#]+)/);
    return m ? decodeURIComponent(m[1]) : null;
  };
  const render = () => {
    const key = itemKey();
    if (!key || !document.body) {
      if (btn) btn.style.display = "none";
      return;
    }
    if (!btn) {
      btn = document.createElement("button");
      btn.style.cssText = "position:fixed;right:20px;bottom:20px;z-index:2147483647;" +
        "padding:12px 18px;border:0;border-radius:24px;font:600 15px system-ui,sans-serif;" +
        "color:#fff;background:#1d4ed8;box-shadow:0 2px 8px rgba(0,0,0,.3);cursor:pointer";
      btn.addEventListener("click", () => {
        const k = itemKey();
        if (!k || (state[k] && !state[k].done)) return;
        state[k] = {text: "Queued", done: false};
        render();
        window.` + bindingName + `(JSON.stringify({key: k, url: location.href}));
      });
    }
    if (!btn.isConnected) document.body.appendChild(btn);
    const s = state[key];
    btn.style.display = "";
    btn.textContent = s ? s.text : NBDL_LABEL;
    btn.disabled = !!(s && !s.done);
    btn.style.opacity = btn.disabled ? "0.85" : "1";
    btn.style.cursor = btn.disabled ? "default" : "pointer";
  };
  window.nbdlStatus = (key, text, done) => {
    state[key] = {text, done};
    render();
  };
  setInterval(render, 500);
  render();
})();`

type browseRequest struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

type browseJob struct {
	req  browseRequest
	page *rod.Page
}

// browse opens nb.no in the dedicated browser with a download button on
// item pages, and downloads the books the user asks for one at a time.
func browse(args []string) error {
	fs := flag.NewFlagSet("nbdl browse", flag.ContinueOnError)
	dir := fs.String("dir", defaultDownloadDir(), "folder to save PDFs in")
	width := fs.Int("width", 1200, "page image width in pixels; 0 downloads full resolution")
	workers := fs.Int("workers", 4, "number of pages to download in parallel")
	format := fs.String("format", "epub", "output format: epub (text recognised from the scans) or pdf (the scans)")
	keepPages := fs.Bool("keep-pages", false, "keep the downloaded pages and recognised text after converting")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("browse takes no arguments, got %q", fs.Arg(0))
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	if *format != "epub" && *format != "pdf" {
		return fmt.Errorf("unknown format %q: use epub or pdf", *format)
	}
	opts := saveOptions{format: *format, width: *width, workers: *workers, keepPages: *keepPages}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b, l, err := launchBrowser(false)
	if err != nil {
		return err
	}
	defer waitExit(l)
	defer b.Close()

	first, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Browse nb.no in the browser window; open a book and click \"Download %s\".\n", strings.ToUpper(*format))
	fmt.Fprintf(os.Stderr, "PDFs are saved in %s. Close the browser window to quit.\n", *dir)
	return runBrowse(ctx, b, first, startURL, *dir, opts)
}

// runBrowse adds the download button to every tab, opens url in first and
// serves download requests until the browser closes or ctx ends.
func runBrowse(ctx context.Context, b *rod.Browser, first *rod.Page, url, dir string, opts saveOptions) error {
	jobs := make(chan browseJob, 100)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var cookies []*http.Cookie
		for job := range jobs {
			// Read the cookies for each book, so a login made while
			// browsing is used; once the window is closed, queued books
			// use the last cookies read.
			if c, err := nbCookies(b); err == nil {
				cookies = c
			}
			browseDownload(ctx, cookies, job, dir, opts)
		}
	}()

	var mu sync.Mutex
	setUp := map[proto.TargetTargetID]bool{}
	setupPage := func(p *rod.Page) {
		mu.Lock()
		if setUp[p.TargetID] {
			mu.Unlock()
			return
		}
		setUp[p.TargetID] = true
		mu.Unlock()

		if err := (proto.RuntimeAddBinding{Name: bindingName}).Call(p); err != nil {
			return
		}
		script := buttonScript(opts.format)
		if _, err := p.EvalOnNewDocument(script); err != nil {
			return
		}
		_, _ = p.Timeout(2 * time.Second).Eval("() => {" + script + "}")
		go p.EachEvent(func(e *proto.RuntimeBindingCalled) {
			if e.Name != bindingName {
				return
			}
			var req browseRequest
			if json.Unmarshal([]byte(e.Payload), &req) != nil || req.Key == "" {
				return
			}
			fmt.Fprintf(os.Stderr, "queued %s\n", req.URL)
			select {
			case jobs <- browseJob{req: req, page: p}:
			default:
				sendStatus(p, req.Key, "Too many queued downloads", true)
			}
		})()
	}

	// Tabs the user opens later get the button too.
	go b.EachEvent(func(e *proto.TargetTargetCreated) {
		if e.TargetInfo.Type != proto.TargetTargetInfoTypePage {
			return
		}
		if p, err := b.PageFromTarget(e.TargetInfo.TargetID); err == nil {
			go setupPage(p)
		}
	})()
	_ = proto.TargetSetDiscoverTargets{Discover: true}.Call(b)

	if pages, err := b.Pages(); err == nil {
		for _, p := range pages {
			setupPage(p)
		}
	}
	setupPage(first)
	if err := first.Navigate(url); err != nil {
		close(jobs)
		wg.Wait()
		return err
	}

	// Wait until the user closes the browser or interrupts.
	for ctx.Err() == nil {
		time.Sleep(time.Second)
		if _, err := b.Pages(); err != nil {
			break
		}
	}
	close(jobs)
	if ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "browser closed; finishing queued downloads (Ctrl+C to stop)")
	}
	wg.Wait()
	return nil
}

// browseDownload saves one book requested from the browser and reports
// progress on its button.
func browseDownload(ctx context.Context, cookies []*http.Cookie, job browseJob, dir string, opts saveOptions) {
	key := job.req.Key
	updates := newStatusSender(job.page, key)
	defer updates.close()
	status := updates.send
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "failed %s: %v\n", job.req.URL, err)
		msg := "Failed, click to retry"
		if errors.Is(err, errAuth) {
			msg = "Access denied, click to retry"
		}
		status(msg, true)
	}

	identifier, err := resolveIdentifier(key, "")
	if err != nil {
		fail(err)
		return
	}
	d, err := newDownloader(cookies)
	if err != nil {
		fail(err)
		return
	}

	status("Starting", false)
	book, err := d.fetchBook(ctx, identifier)
	if err != nil {
		fail(err)
		return
	}
	name := identifier
	if book.URN != "" {
		name = book.URN
	}
	outPath := filepath.Join(dir, bookFileName(book.Title, name, opts.format))
	fmt.Fprintf(os.Stderr, "downloading %s (%d pages)\n", book.Title, len(book.Pages))

	last := time.Time{}
	err = convertBook(ctx, d, book, identifier, outPath, opts, func(phase string, done, total int) {
		if done == total || time.Since(last) > 300*time.Millisecond {
			last = time.Now()
			status(fmt.Sprintf("%s %d/%d", phase, done, total), false)
		}
	})
	if err != nil {
		fail(err)
		return
	}
	fmt.Fprintf(os.Stderr, "saved %s\n", outPath)
	status("Saved, click to download again", true)
}

// statusSender delivers button updates without blocking the download; when
// the page is slow, older updates are dropped and the latest one is sent.
type statusSender struct {
	page *rod.Page
	key  string
	ch   chan buttonStatus
	done chan struct{}
}

type buttonStatus struct {
	text     string
	finished bool
}

func newStatusSender(p *rod.Page, key string) *statusSender {
	s := &statusSender{page: p, key: key, ch: make(chan buttonStatus, 1), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for st := range s.ch {
			sendStatus(s.page, s.key, st.text, st.finished)
		}
	}()
	return s
}

func (s *statusSender) send(text string, finished bool) {
	st := buttonStatus{text, finished}
	for {
		select {
		case s.ch <- st:
			return
		default:
		}
		select {
		case <-s.ch: // drop the stale update
		default:
		}
	}
}

// close waits until the last update has been delivered.
func (s *statusSender) close() {
	close(s.ch)
	<-s.done
}

// sendStatus updates the button of the page that asked for a download. The
// page may have been closed or navigated, so failures are ignored.
func sendStatus(p *rod.Page, key, text string, done bool) {
	_, _ = p.Timeout(2*time.Second).Eval(`(k, t, d) => window.nbdlStatus && window.nbdlStatus(k, t, d)`, key, text, done)
}

func defaultDownloadDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, "Downloads")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return "."
}

// bookFileName names a file after the book's title, keeping the id so two
// editions with the same title do not overwrite each other.
func bookFileName(title, identifier, ext string) string {
	id := outputStem(identifier, identifier)
	title = strings.ReplaceAll(title, " : ", " - ")
	title = strings.Map(func(r rune) rune {
		switch {
		case strings.ContainsRune(`/\:*?"<>|`, r):
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > 120 {
		title = strings.TrimSpace(string(runes[:120]))
	}
	if title == "" {
		return id + "." + ext
	}
	return title + " (" + id + ")." + ext
}
