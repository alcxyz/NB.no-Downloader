package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/alcxyz/NB.no-Downloader/internal/epub"
	"github.com/alcxyz/NB.no-Downloader/internal/layout"
	"github.com/alcxyz/NB.no-Downloader/internal/nb"
	"github.com/alcxyz/NB.no-Downloader/internal/ocr"
	"github.com/alcxyz/NB.no-Downloader/internal/parallel"
)

// progressFunc reports a phase of the work and how far it has come.
type progressFunc func(phase string, done, total int)

// workDir is where an EPUB's intermediate files are kept until it is done,
// so an interrupted run resumes.
func workDir(outPath string) string {
	return strings.TrimSuffix(outPath, filepath.Ext(outPath)) + "_work"
}

// saveEPUB converts book to an EPUB at outPath. The text comes from nb.no
// when it publishes it for the book, otherwise from OCR of the page images.
func saveEPUB(ctx context.Context, d *nb.Downloader, book *nb.Book, identifier, outPath string, opts saveOptions, progress progressFunc) error {
	report := func(phase string, done, total int) {
		if progress != nil {
			progress(phase, done, total)
		}
	}

	if book.URN != "" {
		identifier = book.URN
	}
	meta := epub.Meta{Title: book.Title, Identifier: identifier}
	var languages []string
	if info, err := d.FetchCatalog(ctx, identifier); err == nil {
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

	pages := make([]layout.Page, len(book.Pages))
	var textPages []int
	for i, p := range book.Pages {
		pages[i].Kind = kindOf(p.ID)
		if pages[i].Kind == layout.Normal {
			textPages = append(textPages, i)
		}
	}

	var err error
	if altoAvailable(ctx, d, urn, book, textPages) {
		fmt.Fprintln(os.Stderr, "using nb.no's text")
		err = fetchTexts(ctx, d, urn, book, textPages, dir, opts.workers, pages, report)
	} else {
		fmt.Fprintln(os.Stderr, "nb.no does not publish the text of this book; recognising it from the page images")
		err = ocrTexts(ctx, d, book, textPages, dir, ocr.Language(languages), opts.workers, pages, report)
	}
	if err != nil {
		return fmt.Errorf("%w (work files are kept in %s; try again to resume)", err, dir)
	}

	// Drop caps and note numbers OCR missed are read again from parts of
	// the page images.
	crops := pageImages{d, book}
	engine := &lazyEngine{lang: ocr.Language(languages), workers: opts.workers}
	defer engine.close()
	if n := layout.RestoreCaps(ctx, crops, pages, dir, engine); n > 0 {
		fmt.Fprintf(os.Stderr, "read %d drop caps OCR had missed\n", n)
	}

	if st := layout.CorrectWords(pages); st.Corrected > 0 {
		fmt.Fprintf(os.Stderr, "corrected %d of %d doubtful words against the book's vocabulary\n", st.Corrected, st.Doubtful)
	}
	if n := layout.ReadNoteNumbers(ctx, crops, pages, dir, engine, opts.workers, report); n > 0 {
		fmt.Fprintf(os.Stderr, "\nread %d note numbers OCR had missed\n", n)
	}
	if n := layout.ReadNoteRefs(ctx, crops, pages, dir, engine, opts.workers, report); n > 0 {
		fmt.Fprintf(os.Stderr, "\nread %d note references\n", n)
	}
	if n := layout.DropRemnants(pages); n > 0 {
		fmt.Fprintf(os.Stderr, "removed %d unreadable note markers\n", n)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Pages with little text are either blank or pictures and title pages,
	// which read better as images.
	candidates := layout.MarkPictures(pages, textPages)
	err = parallel.Run(ctx, len(candidates), opts.workers, func(done, total int) { report("Checking pictures", done, total) },
		func(ctx context.Context, j int) error {
			i := candidates[j]
			thumb, err := d.CachedImage(ctx, filepath.Join(dir, fmt.Sprintf("%04d.thumb.jpg", i+1)), nb.ImageURL(book.Pages[i], 200))
			if err != nil {
				return err
			}
			if layout.BlankImage(thumb) {
				pages[i].Blank = true
			} else {
				pages[i].ImageOnly = true
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("%w (work files are kept in %s; try again to resume)", err, dir)
	}

	chapters, figures := layout.Book(pages)
	if len(chapters) == 0 {
		return errors.New("found no text or pictures to put in the EPUB")
	}

	cover := -1
	for i, p := range pages {
		if p.Kind == layout.Cover {
			cover = i
			break
		}
	}
	if cover < 0 && len(book.Pages) > 0 {
		cover = 0
	}
	type imageJob struct {
		name   string
		url    string
		region bool // a part of a page, which may turn out to be empty
	}
	var jobs []imageJob
	if cover >= 0 {
		jobs = append(jobs, imageJob{"cover.jpg", nb.ImageURL(book.Pages[cover], opts.width), false})
	}
	for _, f := range figures {
		jobs = append(jobs, imageJob{f.Name, figureURL(book.Pages[f.Page], pages[f.Page].Text, f.Region, opts.width), f.Region != ocr.Rect{}})
	}
	images := make([]epub.Image, len(jobs))
	err = parallel.Run(ctx, len(jobs), opts.workers, func(done, total int) { report("Fetching pictures", done, total) },
		func(ctx context.Context, j int) error {
			// Named by the address, which holds the region, so a picture
			// cut differently by a newer version is fetched again.
			key := sha256.Sum256([]byte(jobs[j].url))
			data, err := d.CachedImage(ctx, filepath.Join(dir, fmt.Sprintf("img-%x.jpg", key[:8])), jobs[j].url)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// One picture nb.no keeps refusing should not cost the
				// whole book; it is left out.
				fmt.Fprintf(os.Stderr, "\nwarning: leaving out picture %s: %v\n", jobs[j].name, err)
				return nil
			}
			// Pictures are also guessed from empty space between text;
			// where the space really is empty, there is no picture.
			if jobs[j].region && layout.BlankImage(data) {
				return nil
			}
			images[j] = epub.Image{Name: jobs[j].name, Data: data, MediaType: "image/jpeg"}
			return nil
		})
	if err != nil {
		return fmt.Errorf("%w (work files are kept in %s; try again to resume)", err, dir)
	}

	eb := &epub.Book{Meta: meta}
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
	layout.LinkAcross(eb.Chapters)

	report("Writing EPUB", 0, 1)
	tmp := outPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := epub.Write(f, eb); err != nil {
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
// Pages scanned from the dust jacket (_V01, _V02, ...) are kept as pictures
// like the back cover: their text is set around photos and quotes.
var jacketPage = regexp.MustCompile(`_V\d+$`)

func kindOf(id string) layout.Kind {
	switch {
	case strings.HasSuffix(id, "_C1"):
		return layout.Cover
	case strings.HasSuffix(id, "_C3") || jacketPage.MatchString(id):
		return layout.Back
	case strings.HasSuffix(id, "_C2"):
		return layout.Skip
	}
	return layout.Normal
}

// altoAvailable checks one page from the middle of the book: nb.no answers
// 401 for books whose text it does not publish.
func altoAvailable(ctx context.Context, d *nb.Downloader, urn string, book *nb.Book, textPages []int) bool {
	if len(textPages) == 0 {
		return false
	}
	i := textPages[len(textPages)/2]
	body, err := d.FetchALTO(ctx, urn, book.Pages[i].ID)
	if err != nil {
		if !errors.Is(err, nb.ErrAuth) {
			fmt.Fprintf(os.Stderr, "warning: cannot read nb.no's text: %v\n", err)
		}
		return false
	}
	pg, err := ocr.ParseALTO(bytes.NewReader(body))
	return err == nil && len(pg.Blocks) > 0
}

// fetchTexts downloads nb.no's ALTO text for the text pages.
func fetchTexts(ctx context.Context, d *nb.Downloader, urn string, book *nb.Book, textPages []int, dir string, workers int, pages []layout.Page, report progressFunc) error {
	return parallel.Run(ctx, len(textPages), workers, func(done, total int) { report("Reading text", done, total) },
		func(ctx context.Context, j int) error {
			i := textPages[j]
			path := filepath.Join(dir, fmt.Sprintf("%04d.alto.xml", i+1))
			data, err := os.ReadFile(path)
			if err != nil {
				data, err = d.FetchALTO(ctx, urn, book.Pages[i].ID)
				if errors.Is(err, nb.ErrNotFound) {
					return nil // no text for this page; it is shown as an image
				}
				if err != nil {
					return fmt.Errorf("text of page %d: %w", i+1, err)
				}
				if err := writeFileAtomic(path, data); err != nil {
					return err
				}
			}
			pg, err := ocr.ParseALTO(bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("text of page %d: %w", i+1, err)
			}
			pages[i].Text = pg
			return nil
		})
}

// ocrTexts downloads the text pages at full resolution and recognises them.
func ocrTexts(ctx context.Context, d *nb.Downloader, book *nb.Book, textPages []int, dir, lang string, workers int, pages []layout.Page, report progressFunc) error {
	err := parallel.Run(ctx, len(textPages), workers, func(done, total int) { report("Downloading pages", done, total) },
		func(ctx context.Context, j int) error {
			i := textPages[j]
			tsv := filepath.Join(dir, fmt.Sprintf("%04d.tsv", i+1))
			if _, err := os.Stat(tsv); err == nil {
				return nil
			}
			path := nb.PageFile(dir, i)
			if info, err := os.Stat(path); err == nil && info.Size() > 0 {
				return nil
			}
			if err := d.DownloadPage(ctx, nb.ImageURL(book.Pages[i], 0), path); err != nil {
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
	return parallel.Run(ctx, len(textPages), ocrWorkers, func(done, total int) { report("Reading text", done, total) },
		func(ctx context.Context, j int) error {
			i := textPages[j]
			path := filepath.Join(dir, fmt.Sprintf("%04d.tsv", i+1))
			data, err := os.ReadFile(path)
			if err != nil {
				e, err := engine.Engine(ctx)
				if err != nil {
					return err
				}
				tsv, err := e.Recognize(ctx, nb.PageFile(dir, i))
				if err != nil {
					return fmt.Errorf("recognise page %d: %w", i+1, err)
				}
				data = []byte(tsv)
				if err := writeFileAtomic(path, data); err != nil {
					return err
				}
				os.Remove(nb.PageFile(dir, i)) // the full-resolution scan is no longer needed
			}
			pg, err := ocr.ParseTSV(string(data))
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
	engine  ocr.Engine
}

func (l *lazyEngine) Engine(ctx context.Context) (ocr.Engine, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.engine == nil {
		e, err := ocr.NewEngine(ctx, l.lang, l.workers)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "recognising text with %s\n", e.Name())
		l.engine = e
	}
	return l.engine, nil
}

func (l *lazyEngine) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.engine != nil {
		l.engine.Close()
	}
}

// pageImages cuts the regions layout reads again out of a book's page
// images.
type pageImages struct {
	d    *nb.Downloader
	book *nb.Book
}

func (p pageImages) Crop(ctx context.Context, page int, text *ocr.Page, region ocr.Rect, path string) ([]byte, error) {
	return p.d.CachedImage(ctx, path, figureURL(p.book.Pages[page], text, region, 0))
}

// figureURL asks nb.no's image service for a whole page or a region of it.
// The region is in the page's text units, which map onto the full-size
// image by the ratio of their widths.
func figureURL(p nb.Page, text *ocr.Page, region ocr.Rect, width int) string {
	if region == (ocr.Rect{}) || text == nil || text.Width == 0 || p.Width == 0 {
		return nb.ImageURL(p, width)
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

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// withoutFigures removes the figures whose pictures could not be fetched.
func withoutFigures(chapters []epub.Chapter, names []string) []epub.Chapter {
	if len(names) == 0 {
		return chapters
	}
	out := make([]epub.Chapter, 0, len(chapters))
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
