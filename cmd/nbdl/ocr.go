package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// The text of a page, from Tesseract or from nb.no's ALTO files. Units are
// whatever the source uses; layout code only compares values within a page
// or as fractions of the page size.

type rect struct{ X0, Y0, X1, Y1 float64 }

func (r rect) W() float64 { return r.X1 - r.X0 }
func (r rect) H() float64 { return r.Y1 - r.Y0 }

func (r rect) union(o rect) rect {
	if r == (rect{}) {
		return o
	}
	return rect{min(r.X0, o.X0), min(r.Y0, o.Y0), max(r.X1, o.X1), max(r.Y1, o.Y1)}
}

type textWord struct {
	Text string
	Box  rect
	Conf float64 // 0 to 1
	// ALTO marks words split across lines: the first part carries the whole
	// word, the second part is skipped.
	HypFull  string
	HypPart2 bool
}

type textLine struct {
	Words []textWord
	Box   rect
}

type textBlock struct {
	Lines  []textLine
	Box    rect
	Margin bool // ALTO page margin: running heads and page numbers
	Image  bool // an illustration, no text
}

type textPage struct {
	Width, Height float64
	Blocks        []textBlock
}

// parseTSV reads Tesseract's TSV output. Tesseract paragraphs become
// blocks; its own blocks are only used to find empty regions, which are
// usually illustrations.
func parseTSV(tsv string) (*textPage, error) {
	page := &textPage{}
	var block *textBlock
	var line *textLine
	var emptyBlock *textBlock
	flushEmpty := func() {
		if emptyBlock != nil {
			page.Blocks = append(page.Blocks, *emptyBlock)
			emptyBlock = nil
		}
	}
	for i, row := range strings.Split(tsv, "\n") {
		if i == 0 && strings.HasPrefix(row, "level") {
			continue
		}
		f := strings.Split(strings.TrimRight(row, "\r"), "\t")
		if len(f) < 11 {
			continue
		}
		level, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("tsv row %d: %w", i, err)
		}
		var n [4]float64
		for j := range n {
			n[j], _ = strconv.ParseFloat(f[6+j], 64)
		}
		box := rect{n[0], n[1], n[0] + n[2], n[1] + n[3]}
		switch level {
		case 1:
			page.Width, page.Height = box.W(), box.H()
		case 2:
			flushEmpty()
			// Tesseract reports a block before its paragraphs; keep it as
			// a candidate image until a paragraph shows up inside it.
			emptyBlock = &textBlock{Box: box, Image: true}
			block, line = nil, nil
		case 3:
			emptyBlock = nil
			page.Blocks = append(page.Blocks, textBlock{Box: box})
			block = &page.Blocks[len(page.Blocks)-1]
			line = nil
		case 4:
			if block == nil {
				continue
			}
			block.Lines = append(block.Lines, textLine{Box: box})
			line = &block.Lines[len(block.Lines)-1]
		case 5:
			text := ""
			if len(f) >= 12 {
				text = strings.TrimSpace(f[11])
			}
			if line == nil || text == "" {
				continue
			}
			conf, _ := strconv.ParseFloat(f[10], 64)
			line.Words = append(line.Words, textWord{Text: text, Box: box, Conf: conf / 100})
		}
	}
	flushEmpty()
	return page, nil
}

// ocrEngine turns a page image into Tesseract TSV.
type ocrEngine interface {
	recognize(ctx context.Context, imagePath string) (string, error)
	name() string
	close()
}

// tesseractLang maps catalogue language codes to Tesseract models.
func tesseractLang(languages []string) string {
	for _, l := range languages {
		switch l {
		case "eng", "en":
			return "eng"
		case "nob", "nno", "nor", "nb", "nn", "no":
			return "nor"
		}
	}
	return "nor"
}

// newOCREngine prefers an installed Tesseract with the right language and
// otherwise runs Tesseract compiled to WebAssembly in a headless browser,
// so no separate install is needed.
func newOCREngine(ctx context.Context, lang string, workers int) (ocrEngine, error) {
	if bin, err := exec.LookPath("tesseract"); err == nil {
		out, err := exec.CommandContext(ctx, bin, "--list-langs").CombinedOutput()
		if err == nil && hasLine(string(out), lang) {
			return &nativeOCR{bin: bin, lang: lang}, nil
		}
		fmt.Fprintf(os.Stderr, "tesseract has no %q language data; using the built-in OCR instead\n", lang)
	}
	return newBrowserOCR(ctx, lang, workers)
}

func hasLine(text, want string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

type nativeOCR struct {
	bin, lang string
}

func (n *nativeOCR) name() string { return "tesseract" }
func (n *nativeOCR) close()       {}

func (n *nativeOCR) recognize(ctx context.Context, imagePath string) (string, error) {
	cmd := exec.CommandContext(ctx, n.bin, imagePath, "stdout", "-l", n.lang, "tsv")
	// Pages already run in parallel; Tesseract's own threads would only
	// compete with each other.
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tesseract: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// ocrAsset is a pinned file of tesseract.js, fetched once into the cache.
type ocrAsset struct {
	name, url, sha256 string
}

var ocrAssets = []ocrAsset{
	{"tesseract.min.js", "https://cdn.jsdelivr.net/npm/tesseract.js@7.0.0/dist/tesseract.min.js",
		"000c27d9cd0def655f77b36c72a389c0ab13793aa31cb4d7aab56d09c0afbc7e"},
	{"worker.min.js", "https://cdn.jsdelivr.net/npm/tesseract.js@7.0.0/dist/worker.min.js",
		"576b7df7e3393e137e51849357c9adb53fe7ac1bb69bfa06cf3d61520f182c6d"},
	{"tesseract-core-simd-lstm.wasm.js", "https://cdn.jsdelivr.net/npm/tesseract.js-core@7.0.0/tesseract-core-simd-lstm.wasm.js",
		"c58b46a4c796c0b8afccf77591d5b875b6896b45d402bbce8caa6f5362447b38"},
}

var ocrLangAssets = map[string]ocrAsset{
	"nor": {"nor.traineddata.gz", "https://cdn.jsdelivr.net/npm/@tesseract.js-data/nor@1.0.0/4.0.0_best_int/nor.traineddata.gz",
		"6c5d91a5b684da387bfcf16b38bae8d94771812dc9ef706b413c728ca1d37c93"},
	"eng": {"eng.traineddata.gz", "https://cdn.jsdelivr.net/npm/@tesseract.js-data/eng@1.0.0/4.0.0_best_int/eng.traineddata.gz",
		"45b4cb346724ac1774f1c36f42f182b887bcdb28ebe63e6fff90ac41f3fcff91"},
}

// ensureOCRAssets downloads missing tesseract.js files into the cache and
// checks every file against its pinned hash.
func ensureOCRAssets(ctx context.Context, lang string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "nb-downloader", "tesseract.js-7.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	assets := append([]ocrAsset{}, ocrAssets...)
	la, ok := ocrLangAssets[lang]
	if !ok {
		return "", fmt.Errorf("no built-in OCR model for language %q", lang)
	}
	assets = append(assets, la)
	for _, a := range assets {
		path := filepath.Join(dir, a.name)
		if data, err := os.ReadFile(path); err == nil && sha256Hex(data) == a.sha256 {
			continue
		}
		fmt.Fprintf(os.Stderr, "downloading OCR component %s\n", a.name)
		data, err := fetchAsset(ctx, a.url)
		if err != nil {
			return "", fmt.Errorf("download %s: %w", a.name, err)
		}
		if got := sha256Hex(data); got != a.sha256 {
			return "", fmt.Errorf("download %s: checksum mismatch (got %s)", a.name, got)
		}
		if err := os.WriteFile(path+".part", data, 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(path+".part", path); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func fetchAsset(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// browserOCR runs tesseract.js in a headless browser with a throwaway
// profile, so it works next to a running `nbdl browse` window.
type browserOCR struct {
	browser  *rod.Browser
	launcher *launcher.Launcher
	page     *rod.Page
	server   *http.Server
}

const ocrPageHTML = `<!doctype html><meta charset="utf-8"><script src="/a/tesseract.min.js"></script>`

func newBrowserOCR(ctx context.Context, lang string, workers int) (*browserOCR, error) {
	assets, err := ensureOCRAssets(ctx, lang)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/a/", http.StripPrefix("/a/", http.FileServer(http.Dir(assets))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, ocrPageHTML)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	bin, err := browserPath()
	if err != nil {
		srv.Close()
		return nil, err
	}
	l := launcher.New().Bin(bin).Headless(true).Leakless(false)
	controlURL, err := l.Launch()
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("launch browser for OCR: %w", err)
	}
	b := rod.New().ControlURL(controlURL).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		l.Kill()
		srv.Close()
		return nil, err
	}
	o := &browserOCR{browser: b, launcher: l, server: srv}
	o.page, err = b.Page(proto.TargetCreateTarget{URL: "http://" + ln.Addr().String() + "/"})
	if err == nil {
		err = o.page.WaitLoad()
	}
	if err == nil {
		_, err = o.page.Timeout(2 * time.Minute).Evaluate(rod.Eval(`async (lang, n) => {
			const scheduler = Tesseract.createScheduler();
			for (let i = 0; i < n; i++) {
				const w = await Tesseract.createWorker(lang, 1, {
					workerPath: "/a/worker.min.js",
					corePath: "/a/tesseract-core-simd-lstm.wasm.js",
					langPath: "/a/", gzip: true, workerBlobURL: false, cacheMethod: "none",
				});
				scheduler.addWorker(w);
			}
			window.nbdlOCR = scheduler;
		}`, lang, max(workers, 1)).ByPromise())
	}
	if err != nil {
		o.close()
		return nil, fmt.Errorf("start built-in OCR: %w", err)
	}
	return o, nil
}

func (o *browserOCR) name() string { return "built-in tesseract.js" }

func (o *browserOCR) recognize(ctx context.Context, imagePath string) (string, error) {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return "", err
	}
	url := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
	res, err := o.page.Context(ctx).Timeout(5 * time.Minute).Evaluate(rod.Eval(`async (img) => {
		const r = await window.nbdlOCR.addJob("recognize", img, {}, {tsv: true, text: false, blocks: false});
		return r.data.tsv;
	}`, url).ByPromise())
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("built-in OCR: %w", err)
	}
	tsv := res.Value.Str()
	if tsv == "" {
		return "", errors.New("built-in OCR returned no result")
	}
	return tsv, nil
}

func (o *browserOCR) close() {
	if o.browser != nil {
		o.browser.Close()
	}
	if o.launcher != nil {
		o.launcher.Cleanup() // removes the throwaway profile
	}
	if o.server != nil {
		o.server.Close()
	}
}
