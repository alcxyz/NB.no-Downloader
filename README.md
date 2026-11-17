# NB.no Downloader

`nbdl` downloads a book from the Norwegian National Library
([nb.no](https://www.nb.no), Nasjonalbiblioteket) and saves it as an EPUB
with real, reflowable text, or as a PDF of the scanned pages.

`nbdl` keeps its own browser for nb.no: you log in there once with BankID (or
any other nb.no login), find books as usual and click a download button, or
paste a book's address on the command line.

This is a Go rewrite of [akselsd/NB.no-Downloader](https://github.com/akselsd/NB.no-Downloader);
the original Python script is kept in [`archived/`](archived/).

## Install

With Nix:

```bash
nix run github:alcxyz/NB.no-Downloader -- <book>
nix profile install github:alcxyz/NB.no-Downloader
```

The Nix package brings its own Chromium and Tesseract.

With Go 1.23 or newer:

```bash
go install github.com/alcxyz/NB.no-Downloader/cmd/nbdl@latest
```

The login, the browser and the built-in text recognition need a
Chromium-based browser. `nbdl` uses `$NBDL_BROWSER` if set, then an installed
Google Chrome, Chromium or Microsoft Edge, and otherwise downloads a Chromium
build into its cache. That downloaded build does not run on NixOS; use the Nix
package or set `NBDL_BROWSER` there. Nothing else needs installing; see
[EPUB](#epub) for the optional Tesseract.

## Usage

### Browsing

```bash
nbdl browse
```

This opens nb.no in `nbdl`'s own browser window. Log in there if you need
to, search and open books as usual, and click **Download EPUB** in the corner
of a book's page. The button shows the progress; you can keep browsing and
queue more books, which are downloaded one at a time. Books are saved in
`~/Downloads` (or the current folder if there is none), named after the book's
title; `-dir` picks another folder. Close the window to quit; queued books
still finish.

`browse` takes the `-format`, `-width`, `-workers` and `-keep-pages` options
described below.

### From the command line

```bash
nbdl <book>
```

`<book>` can be any of:

- an nb.no link: `https://www.nb.no/items/URN:NBN:no-nb_digibok_2008010300041`
  or `https://www.nb.no/items/296c0829d64f7ba7b3a684d912920284`
- a URN: `URN:NBN:no-nb_digibok_2008010300041`
- a bare id with its type: `nbdl -type pliktmonografi 000040863`
  (the type defaults to `digibok`)

The book is written to `<id>.epub` (or `<id>.pdf` with `-format pdf`) in the
current directory, or to the path given with `-o`.

### EPUB

An EPUB needs the book's text, which nb.no has only as images. `nbdl` gets it
in one of two ways:

- For books nb.no publishes the recognised text of (mostly books in the public
  domain), it uses nb.no's own text.
- For other books it recognises the text itself from the full-resolution
  scans. It uses [Tesseract](https://github.com/tesseract-ocr/tesseract) when
  it is installed with Norwegian (`nor`) or English data, which is fastest.
  Otherwise it runs Tesseract compiled to WebAssembly
  ([tesseract.js](https://github.com/naptha/tesseract.js)) in a hidden
  browser; the first time, it downloads about 7 MB of program and language
  files, checks them against pinned checksums and keeps them in its cache.

`nbdl` then rebuilds the book: paragraphs flow across pages, words hyphenated
at line ends are joined, running heads and page numbers are dropped, headings
and chapters are found (including chapters numbered I, II, III), charts,
tables, maps and illustrations are cut out of the scans as pictures with
their labels, and title pages and picture pages are kept as images. The
front cover becomes the cover.

Where text recognition stumbles, `nbdl` reads small parts of the page images
again: a chapter's large initial letter it missed, and the small raised
numbers that refer to notes. Notes at the back of the book are numbered and
linked to and from the text; a reference is linked only when its number is
read clearly and fits the order of the others, so some stay unlinked rather
than point to the wrong note.

The EPUB carries nb.no's catalogue data: title and subtitle, authors, ISBN,
publisher, year, language, subjects and summary, so e-book managers such as
Calibre fill in their fields on import.

Recognised text is never perfect. Modern print comes out very well; old
Gothic type, tables, footnotes and complex layouts less so, and a chapter
heading or chart title is sometimes taken for the other. Use `-format pdf`
when the layout matters more than reflowing text.

### Logging in

Many books are only available from Norwegian IP addresses or to logged-in
users. To log in:

```bash
nbdl login
```

A browser window opens on the nb.no login page. Log in there; the window
closes by itself once you are logged in. Logging in on nb.no during
`nbdl browse` works the same way. From then on every download uses the
saved session (pass `-anonymous` to skip it). When the session expires,
`nbdl` warns you and you run `nbdl login` again. `nbdl logout` deletes the
saved session.

The session lives in a separate browser profile, by default
`~/.config/nb-downloader/browser-profile` (Linux) or the equivalent
configuration directory on macOS and Windows; set `NBDL_PROFILE` to move it.
Command-line downloads start that profile without a window for about a second
to read the session, then download without the browser. Only one `nbdl`
browser can use the profile at a time, so close the `browse` window before
downloading from the command line.

Without a browser, `-cookie-file` reads nb.no cookies from a file, either a
`Cookie:` header copied from the browser's developer tools or a Netscape
`cookies.txt` export. `-cookie-file -` reads them from standard input.

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `-format` | `epub` | `epub` for recognised text, `pdf` for the page scans |
| `-o` | `<id>.epub` | Output file |
| `-width` | `1200` | Width in pixels of PDF pages and EPUB pictures; `0` downloads full resolution |
| `-type` | `digibok` | Document type for a bare numeric id |
| `-workers` | `4` | Pages downloaded in parallel |
| `-keep-pages` | off | Keep the downloaded pages and recognised text afterwards |
| `-anonymous` | off | Do not use the saved login |
| `-cookie-file` | | Read cookies from a file instead of the login browser |

### Interrupted downloads

Pages and recognised text are saved next to the output (`<name>_work/` for an
EPUB, `<name>_pages/` for a PDF) while `nbdl` works. If a download fails or
you stop it, run the same command again: what is already on disk is kept and
only the rest is fetched or recognised. The folder is removed once the book is
written, unless `-keep-pages` is set.

nb.no rejects a large share of image requests for Norway-only books with a
`403` at random, logged in or not, so `nbdl` retries each page several times
and reports how many requests it retried. A page that is refused every time
is reported as an access problem.

## Development

```bash
nix develop   # Go, gopls, Chromium, Tesseract and epubcheck
go test ./...
NBDL_E2E=1 go test ./cmd/nbdl   # also clicks the button on nb.no in a headless browser
```

`go.mod` changes need the `vendorHash` in [`nix/package.nix`](nix/package.nix)
updated; `nix build` prints the new hash.

## Legal note

This tool is meant for personal use within the
[terms of use](https://www.nb.no/hjelp-og-informasjon/rettigheter/) of the
National Library. Respect copyright and the library's access conditions; a
login gives you access, not the right to redistribute.

## License

MIT, see [LICENSE.md](LICENSE.md).
