package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const altoBase = "https://api.nb.no/catalog/v1/metadata/"

// progressFunc reports a phase of the work and how far it has come.
type progressFunc func(phase string, done, total int)

// workDir is where an EPUB's intermediate files are kept until it is done,
// so an interrupted run resumes.
func workDir(outPath string) string {
	return strings.TrimSuffix(outPath, filepath.Ext(outPath)) + "_work"
}

// saveEPUB converts book to an EPUB at outPath. The text comes from nb.no
// when it publishes it for the book, otherwise from OCR of the page images.
func saveEPUB(ctx context.Context, d *downloader, book *Book, identifier, outPath string, opts saveOptions, progress progressFunc) error {
	report := func(phase string, done, total int) {
		if progress != nil {
			progress(phase, done, total)
		}
	}

	if book.URN != "" {
		identifier = book.URN
	}
	meta := bookMeta{Title: book.Title, Identifier: identifier}
	var languages []string
	if info, err := d.fetchCatalog(ctx, identifier); err == nil {
		meta, languages = info.Meta, info.Languages
		if meta.Title == "" {
			meta.Title = book.Title
		}
	} else {
		fmt.Fprintf(os.Stderr, "warning: %v; the EPUB gets the title only\n", err)
	}
	if meta.Title == "" {
		meta.Title = meta.Identifier
	}
	urn := meta.Identifier

	dir := workDir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	pages := make([]layoutPage, len(book.Pages))
	var textPages []int
	for i, p := range book.Pages {
		pages[i].Kind = kindOf(p.ID)
		if pages[i].Kind == pageNormal {
			textPages = append(textPages, i)
		}
	}

	var err error
	if d.altoAvailable(ctx, urn, book, textPages) {
		fmt.Fprintln(os.Stderr, "using nb.no's text")
		err = d.fetchTexts(ctx, urn, book, textPages, dir, opts.workers, pages, report)
	} else {
		fmt.Fprintln(os.Stderr, "nb.no does not publish the text of this book; recognising it from the page images")
		err = ocrTexts(ctx, d, book, textPages, dir, tesseractLang(languages), opts.workers, pages, report)
	}
	if err != nil {
		return fmt.Errorf("%w (work files are kept in %s; try again to resume)", err, dir)
	}

	if st := correctWords(pages); st.corrected > 0 {
		fmt.Fprintf(os.Stderr, "corrected %d of %d doubtful words against the book's vocabulary\n", st.corrected, st.doubtful)
	}

	// Pages with little text are either blank or pictures and title pages,
	// which read better as images.
	candidates := markPictures(pages, textPages)
	err = parallel(ctx, len(candidates), opts.workers, func(done, total int) { report("Checking pictures", done, total) },
		func(ctx context.Context, j int) error {
			i := candidates[j]
			thumb, err := d.cachedImage(ctx, filepath.Join(dir, fmt.Sprintf("%04d.thumb.jpg", i+1)), imageURL(book.Pages[i], 200))
			if err != nil {
				return err
			}
			if blankImage(thumb) {
				pages[i].Blank = true
			} else {
				pages[i].ImageOnly = true
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("%w (work files are kept in %s; try again to resume)", err, dir)
	}

	chapters, figures := layoutBook(pages)
	if len(chapters) == 0 {
		return errors.New("found no text or pictures to put in the EPUB")
	}

	cover := -1
	for i, p := range pages {
		if p.Kind == pageCover {
			cover = i
			break
		}
	}
	if cover < 0 && len(book.Pages) > 0 {
		cover = 0
	}
	type imageJob struct {
		name string
		url  string
	}
	var jobs []imageJob
	if cover >= 0 {
		jobs = append(jobs, imageJob{"cover.jpg", imageURL(book.Pages[cover], opts.width)})
	}
	for _, f := range figures {
		jobs = append(jobs, imageJob{f.Name, figureURL(book.Pages[f.Page], pages[f.Page].Text, f.Region, opts.width)})
	}
	images := make([]epubImage, len(jobs))
	err = parallel(ctx, len(jobs), opts.workers, func(done, total int) { report("Fetching pictures", done, total) },
		func(ctx context.Context, j int) error {
			data, err := d.cachedImage(ctx, filepath.Join(dir, "img-"+jobs[j].name), jobs[j].url)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// One picture nb.no keeps refusing should not cost the
				// whole book; it is left out.
				fmt.Fprintf(os.Stderr, "\nwarning: leaving out picture %s: %v\n", jobs[j].name, err)
				return nil
			}
			images[j] = epubImage{Name: jobs[j].name, Data: data, MediaType: "image/jpeg"}
			return nil
		})
	if err != nil {
		return fmt.Errorf("%w (work files are kept in %s; try again to resume)", err, dir)
	}

	eb := &epubBook{Meta: meta}
	if cover >= 0 {
		if images[0].Data != nil {
			eb.Cover = &images[0]
		}
		images = images[1:]
	}
	var missing []string
	for _, img := range images {
		if img.Data != nil {
			eb.Images = append(eb.Images, img)
		}
	}
	for j, img := range images {
		if img.Data == nil {
			missing = append(missing, jobs[len(jobs)-len(images)+j].name)
		}
	}
	eb.Chapters = withoutFigures(chapters, missing)

	report("Writing EPUB", 0, 1)
	tmp := outPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := writeEPUB(f, eb); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write EPUB: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, outPath); err != nil {
		return err
	}
	report("Writing EPUB", 1, 1)
	if !opts.keepPages {
		return os.RemoveAll(dir)
	}
	return nil
}

// kindOf tells covers and the spine from text pages by nb.no's page ids.
func kindOf(id string) pageKind {
	switch {
	case strings.HasSuffix(id, "_C1"):
		return pageCover
	case strings.HasSuffix(id, "_C3"):
		return pageBack
	case strings.HasSuffix(id, "_C2"):
		return pageSkip
	}
	return pageNormal
}

func (d *downloader) altoURL(urn, pageID string) string {
	base := d.alto
	if base == "" {
		base = altoBase
	}
	return base + url.PathEscape(urn) + "/altos/" + url.PathEscape(pageID)
}

// altoAvailable checks one page from the middle of the book: nb.no answers
// 401 for books whose text it does not publish.
func (d *downloader) altoAvailable(ctx context.Context, urn string, book *Book, textPages []int) bool {
	if len(textPages) == 0 {
		return false
	}
	i := textPages[len(textPages)/2]
	body, err := d.fetch(ctx, d.altoURL(urn, book.Pages[i].ID), "application/xml")
	if err != nil {
		if !errors.Is(err, errAuth) {
			fmt.Fprintf(os.Stderr, "warning: cannot read nb.no's text: %v\n", err)
		}
		return false
	}
	pg, err := parseALTO(bytes.NewReader(body))
	return err == nil && len(pg.Blocks) > 0
}

// fetchTexts downloads nb.no's ALTO text for the text pages.
func (d *downloader) fetchTexts(ctx context.Context, urn string, book *Book, textPages []int, dir string, workers int, pages []layoutPage, report progressFunc) error {
	return parallel(ctx, len(textPages), workers, func(done, total int) { report("Reading text", done, total) },
		func(ctx context.Context, j int) error {
			i := textPages[j]
			path := filepath.Join(dir, fmt.Sprintf("%04d.alto.xml", i+1))
			data, err := os.ReadFile(path)
			if err != nil {
				data, err = d.fetch(ctx, d.altoURL(urn, book.Pages[i].ID), "application/xml")
				if errors.Is(err, errNotFound) {
					return nil // no text for this page; it is shown as an image
				}
				if err != nil {
					return fmt.Errorf("text of page %d: %w", i+1, err)
				}
				if err := writeFileAtomic(path, data); err != nil {
					return err
				}
			}
			pg, err := parseALTO(bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("text of page %d: %w", i+1, err)
			}
			pages[i].Text = pg
			return nil
		})
}

// ocrTexts downloads the text pages at full resolution and recognises them.
func ocrTexts(ctx context.Context, d *downloader, book *Book, textPages []int, dir, lang string, workers int, pages []layoutPage, report progressFunc) error {
	err := parallel(ctx, len(textPages), workers, func(done, total int) { report("Downloading pages", done, total) },
		func(ctx context.Context, j int) error {
			i := textPages[j]
			tsv := filepath.Join(dir, fmt.Sprintf("%04d.tsv", i+1))
			if _, err := os.Stat(tsv); err == nil {
				return nil
			}
			path := pageFile(dir, i)
			if info, err := os.Stat(path); err == nil && info.Size() > 0 {
				return nil
			}
			if err := d.downloadPage(ctx, imageURL(book.Pages[i], 0), path); err != nil {
				return fmt.Errorf("page %d (%s): %w", i+1, book.Pages[i].Label, err)
			}
			return nil
		})
	if err != nil {
		return err
	}

	ocrWorkers := min(max(runtime.NumCPU()-1, 1), 6)
	engine := &lazyEngine{lang: lang, workers: ocrWorkers}
	defer engine.close()
	return parallel(ctx, len(textPages), ocrWorkers, func(done, total int) { report("Reading text", done, total) },
		func(ctx context.Context, j int) error {
			i := textPages[j]
			path := filepath.Join(dir, fmt.Sprintf("%04d.tsv", i+1))
			data, err := os.ReadFile(path)
			if err != nil {
				e, err := engine.get(ctx)
				if err != nil {
					return err
				}
				tsv, err := e.recognize(ctx, pageFile(dir, i))
				if err != nil {
					return fmt.Errorf("recognise page %d: %w", i+1, err)
				}
				data = []byte(tsv)
				if err := writeFileAtomic(path, data); err != nil {
					return err
				}
				os.Remove(pageFile(dir, i)) // the full-resolution scan is no longer needed
			}
			pg, err := parseTSV(string(data))
			if err != nil {
				return fmt.Errorf("recognise page %d: %w", i+1, err)
			}
			pages[i].Text = pg
			return nil
		})
}

// lazyEngine starts the OCR engine on first use, so a book whose text is
// already cached needs none.
type lazyEngine struct {
	lang    string
	workers int
	mu      sync.Mutex
	engine  ocrEngine
}

func (l *lazyEngine) get(ctx context.Context) (ocrEngine, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.engine == nil {
		e, err := newOCREngine(ctx, l.lang, l.workers)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "recognising text with %s\n", e.name())
		l.engine = e
	}
	return l.engine, nil
}

func (l *lazyEngine) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.engine != nil {
		l.engine.close()
	}
}

// cachedImage fetches an image once into path.
func (d *downloader) cachedImage(ctx context.Context, path, rawURL string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return data, nil
	}
	data, err := d.fetch(ctx, rawURL, "image/jpeg")
	if err != nil {
		return nil, err
	}
	return data, writeFileAtomic(path, data)
}

// figureURL asks nb.no's image service for a whole page or a region of it.
// The region is in the page's text units, which map onto the full-size
// image by the ratio of their widths.
func figureURL(p Page, text *textPage, region rect, width int) string {
	if region == (rect{}) || text == nil || text.Width == 0 || p.Width == 0 {
		return imageURL(p, width)
	}
	scale := float64(p.Width) / text.Width
	x := max(0, int(region.X0*scale))
	y := max(0, int(region.Y0*scale))
	w := int(region.W() * scale)
	h := int(region.H() * scale)
	if p.Width > 0 {
		w = min(w, p.Width-x)
	}
	if p.Height > 0 {
		h = min(h, p.Height-y)
	}
	size := "full"
	if width > 0 && w > width {
		size = fmt.Sprintf("%d,", width)
	}
	return fmt.Sprintf("%s/%d,%d,%d,%d/%s/0/default.jpg", p.Service, x, y, w, h, size)
}

// blankImage reports whether a page thumbnail is empty paper: almost no
// pixels are much darker than the page around them.
func blankImage(data []byte) bool {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	b := img.Bounds()
	// Ignore a margin where scans show the page edge and the scanner bed.
	mx, my := b.Dx()/12, b.Dy()/12
	var lum []float64
	for y := b.Min.Y + my; y < b.Max.Y-my; y++ {
		for x := b.Min.X + mx; x < b.Max.X-mx; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			lum = append(lum, (0.299*float64(r)+0.587*float64(g)+0.114*float64(bl))/257)
		}
	}
	if len(lum) == 0 {
		return true
	}
	paper := median(lum)
	dark := 0
	for _, l := range lum {
		if l < paper-math.Max(40, paper*0.25) {
			dark++
		}
	}
	return float64(dark)/float64(len(lum)) < blankDarkShare
}

// blankDarkShare is the share of dark pixels below which a page counts as
// blank; tuned on scans of blank, title and text pages.
var blankDarkShare = 0.002

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// withoutFigures removes the figures whose pictures could not be fetched.
func withoutFigures(chapters []epubChapter, names []string) []epubChapter {
	if len(names) == 0 {
		return chapters
	}
	out := make([]epubChapter, 0, len(chapters))
	for _, ch := range chapters {
		for _, name := range names {
			src := `src="images/` + name + `"`
			for {
				i := strings.Index(ch.Body, src)
				if i < 0 {
					break
				}
				start := strings.LastIndex(ch.Body[:i], "<figure")
				end := strings.Index(ch.Body[i:], "</figure>\n")
				if start < 0 || end < 0 {
					break
				}
				ch.Body = ch.Body[:start] + ch.Body[i+end+len("</figure>\n"):]
			}
		}
		if strings.TrimSpace(ch.Body) != "" {
			out = append(out, ch)
		}
	}
	return out
}
