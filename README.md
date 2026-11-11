# NB.no Downloader

`nbdl` downloads a book from the Norwegian National Library
([nb.no](https://www.nb.no), Nasjonalbiblioteket) and saves it as a PDF.

It reads the book's page list from nb.no's IIIF manifest, downloads the pages
in parallel and writes them into a PDF sized from the original scans. For
books that need a login, you log in once with BankID (or any other nb.no login)
in a browser window that `nbdl` opens, and later downloads reuse that session.

This is a Go rewrite of [akselsd/NB.no-Downloader](https://github.com/akselsd/NB.no-Downloader);
the original Python script is kept in [`archived/`](archived/).

## Install

With Nix:

```bash
nix run github:alcxyz/NB.no-Downloader -- <book>
nix profile install github:alcxyz/NB.no-Downloader
```

The Nix package brings its own Chromium for the login.

With Go 1.23 or newer:

```bash
go install github.com/alcxyz/NB.no-Downloader/cmd/nbdl@latest
```

The login needs a Chromium-based browser. `nbdl` uses `$NBDL_BROWSER` if set,
then an installed Google Chrome, Chromium or Microsoft Edge, and otherwise
downloads a Chromium build into its cache. That downloaded build does not run
on NixOS; use the Nix package or set `NBDL_BROWSER` there.

## Usage

```bash
nbdl <book>
```

`<book>` can be any of:

- an nb.no link: `https://www.nb.no/items/URN:NBN:no-nb_digibok_2008010300041`
  or `https://www.nb.no/items/296c0829d64f7ba7b3a684d912920284`
- a URN: `URN:NBN:no-nb_digibok_2008010300041`
- a bare id with its type: `nbdl -type pliktmonografi 000040863`
  (the type defaults to `digibok`)

The PDF is written to `<id>.pdf` in the current directory, or to the path given
with `-o`.

### Logging in

Many books are only available from Norwegian IP addresses or to logged-in
users. To log in:

```bash
nbdl login
```

A browser window opens on the nb.no login page. Log in there; the window
closes by itself once you are logged in. From then on every download uses the
saved session (pass `-anonymous` to skip it). When the session expires,
`nbdl` warns you and you run `nbdl login` again. `nbdl logout` deletes the
saved session.

The session lives in a separate browser profile, by default
`~/.config/nb-downloader/browser-profile` (Linux) or the equivalent
configuration directory on macOS and Windows; set `NBDL_PROFILE` to move it.
Downloads start that profile without a window for about a second to read the
session, then download without the browser.

Without a browser, `-cookie-file` reads nb.no cookies from a file, either a
`Cookie:` header copied from the browser's developer tools or a Netscape
`cookies.txt` export. `-cookie-file -` reads them from standard input.

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `-o` | `<id>.pdf` | Output PDF path |
| `-width` | `1200` | Page image width in pixels; `0` downloads full resolution |
| `-type` | `digibok` | Document type for a bare numeric id |
| `-workers` | `4` | Pages downloaded in parallel |
| `-keep-pages` | off | Keep the page images after creating the PDF |
| `-anonymous` | off | Do not use the saved login |
| `-cookie-file` | | Read cookies from a file instead of the login browser |

### Interrupted downloads

Pages are saved in `<output>_pages/` while downloading. If a download fails or
you stop it, run the same command again: pages already on disk are kept and
only the rest are fetched. The folder is removed once the PDF is written,
unless `-keep-pages` is set.

nb.no rejects a large share of image requests for Norway-only books with a
`403` at random, logged in or not, so `nbdl` retries each page several times
and reports how many requests it retried. A page that is refused every time
is reported as an access problem.

## Development

```bash
nix develop   # Go, gopls and Chromium
go test ./...
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
