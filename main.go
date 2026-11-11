// Command NB.no-Downloader downloads a book from the Norwegian National
// Library (Nasjonalbiblioteket, nb.no) and saves it as a PDF.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if errors.Is(err, errAuth) {
			fmt.Fprintln(os.Stderr, "Log in at www.nb.no, export fresh cookies and pass them with -cookie-file.")
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("NB.no-Downloader", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [flags] <book id | URN | nb.no URL>\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
	}
	bookID := fs.String("id", "", "book id, URN or nb.no item URL (or pass it as the first argument)")
	docType := fs.String("type", "digibok", "document type for a bare numeric id, such as digibok or pliktmonografi")
	cookieFile := fs.String("cookie-file", "", "file with nb.no cookies (Cookie header or Netscape cookies.txt); - reads stdin")
	width := fs.Int("width", 1200, "page image width in pixels; 0 downloads full resolution")
	output := fs.String("o", "", "output PDF path (default <id>.pdf)")
	workers := fs.Int("workers", 4, "number of pages to download in parallel")
	keepPages := fs.Bool("keep-pages", false, "keep the downloaded page images after creating the PDF")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	input := *bookID
	if input == "" {
		if fs.NArg() == 0 {
			fs.Usage()
			return errors.New("missing book id")
		}
		input = fs.Arg(0)
	}
	identifier, err := resolveIdentifier(input, *docType)
	if err != nil {
		return err
	}

	var cookies string
	if *cookieFile != "" {
		cookies, err = readCookies(*cookieFile)
		if err != nil {
			return err
		}
	}

	pdfPath := *output
	if pdfPath == "" {
		pdfPath = outputStem(input, identifier) + ".pdf"
	}
	pagesDir := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath)) + "_pages"

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := &downloader{
		client:     &http.Client{Timeout: 2 * time.Minute},
		cookies:    cookies,
		cookieHost: "nb.no",
		manifest:   manifestBase,
		retries:    15,
		retryDelay: 250 * time.Millisecond,
	}

	book, err := d.fetchBook(ctx, identifier)
	if err != nil {
		return err
	}
	title := book.Title
	if title == "" {
		title = identifier
	}
	fmt.Fprintf(os.Stderr, "%s: %d pages\n", title, len(book.Pages))
	if strings.Contains(identifier, "pliktmonografi") && cookies == "" {
		fmt.Fprintln(os.Stderr, "warning: pliktmonografi items usually need login cookies (-cookie-file)")
	}

	err = d.downloadPages(ctx, book, pagesDir, *width, *workers, func(done, total int) {
		fmt.Fprintf(os.Stderr, "\rdownloaded %d/%d pages", done, total)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("%w (downloaded pages are kept in %s; run again to resume)", err, pagesDir)
	}

	pages := make([]pdfPage, len(book.Pages))
	for i, p := range book.Pages {
		pages[i] = pdfPage{Path: pageFile(pagesDir, i), InchesPerPixel: p.InchesPerPixel, OriginalWidth: p.Width}
	}
	if err := savePDF(pdfPath, book.Title, pages); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "saved %s\n", pdfPath)

	if !*keepPages {
		return os.RemoveAll(pagesDir)
	}
	return nil
}

func savePDF(path, title string, pages []pdfPage) error {
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := writePDF(f, title, pages); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write PDF: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// readCookies loads nb.no cookies from a file holding either a browser
// Cookie header value ("a=1; b=2") or a Netscape cookies.txt export, and
// returns them as a Cookie header value.
func readCookies(path string) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("read cookie file: %w", err)
	}
	cookies := parseCookies(string(data))
	if cookies == "" {
		return "", errors.New("cookie file contains no cookies")
	}
	return cookies, nil
}

func parseCookies(text string) string {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "Cookie:"))
	var pairs []string
	if strings.Contains(text, "\t") {
		// Netscape format: domain, subdomains, path, secure, expiry, name, value.
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimRight(line, "\r")
			line = strings.TrimPrefix(line, "#HttpOnly_")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Split(line, "\t")
			if len(f) != 7 {
				continue
			}
			if !hostWithin(strings.TrimPrefix(f[0], "."), "nb.no") {
				continue
			}
			pairs = append(pairs, f[5]+"="+f[6])
		}
		return strings.Join(pairs, "; ")
	}
	for _, part := range strings.Split(text, ";") {
		part = strings.TrimSpace(part)
		if name, _, ok := strings.Cut(part, "="); ok && name != "" {
			pairs = append(pairs, part)
		}
	}
	return strings.Join(pairs, "; ")
}
