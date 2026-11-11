// Command NB.no-Downloader downloads a book from the Norwegian National
// Library (Nasjonalbiblioteket, nb.no) and saves it as a PDF.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
			fmt.Fprintln(os.Stderr, "If this item needs a login, run: nbdl login")
		}
		os.Exit(1)
	}
}

const usage = `Usage:
  nbdl login                 log in to nb.no in a dedicated browser window
  nbdl logout                forget the saved nb.no login
  nbdl [download] [flags] <book id | URN | nb.no URL>

Download flags:
`

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "login":
			return login()
		case "logout":
			return logout()
		case "download":
			args = args[1:]
		}
	}

	fs := flag.NewFlagSet("nbdl", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	bookID := fs.String("id", "", "book id, URN or nb.no item URL (or pass it as the first argument)")
	docType := fs.String("type", "digibok", "document type for a bare numeric id, such as digibok or pliktmonografi")
	cookieFile := fs.String("cookie-file", "", "read nb.no cookies from this file (Cookie header or Netscape cookies.txt; - reads stdin) instead of the login browser")
	anonymous := fs.Bool("anonymous", false, "do not use the saved login")
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

	var cookies []*http.Cookie
	switch {
	case *cookieFile != "":
		cookies, err = readCookies(*cookieFile)
		if err != nil {
			return err
		}
	case !*anonymous:
		cookies, err = browserSession()
		if err != nil {
			return fmt.Errorf("read login from browser profile: %w", err)
		}
		if len(cookies) > 0 && !hasLogin(cookies) {
			fmt.Fprintln(os.Stderr, "warning: the saved nb.no login has expired; run nbdl login to renew it")
		}
	}
	loggedIn := hasLogin(cookies)
	if loggedIn {
		fmt.Fprintln(os.Stderr, "using nb.no login")
	}
	jar, err := newJar(cookies)
	if err != nil {
		return err
	}

	pdfPath := *output
	if pdfPath == "" {
		pdfPath = outputStem(input, identifier) + ".pdf"
	}
	pagesDir := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath)) + "_pages"

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := &downloader{
		client:     &http.Client{Jar: jar, Timeout: 2 * time.Minute},
		authDomain: "nb.no",
		manifest:   manifestBase,
		retries:    15,
		retryDelay: 250 * time.Millisecond,
	}
	if loggedIn {
		d.tokenURL = tokenURL
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
	if strings.Contains(identifier, "pliktmonografi") && !loggedIn {
		fmt.Fprintln(os.Stderr, "warning: pliktmonografi items usually need a login (nbdl login)")
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
	if n := d.retried.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "(%d requests were retried after transient errors)\n", n)
	}

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
