# Norwegian National Library Book Downloader

A Go utility to download and convert books from the Norwegian National Library (Nasjonalbiblioteket) into PDF files.

## Description

This tool allows you to download books from the Norwegian National Library's digital collection and convert them to PDF format. It supports both publicly available books (`digibok_` prefix) and restricted content (`pliktmonografi_` prefix) that requires authentication.

This is a Go port of [akselsd/NB.no-Downloader](https://github.com/akselsd/NB.no-Downloader), originally written in Python, with additional features and improvements.

## Features

- Download complete books from the Norwegian National Library
- Support for both public (`digibok`) and restricted (`pliktmonografi`) document types
- Automatically determine book length if not specified
- Authentication support for restricted content (including cookie file support)
- Convert all pages into a single PDF file
- Customizable image quality
- Automatic detection of introduction pages (I1, I2, etc.)

## Prerequisites

- Go 1.16 or higher
- The following Go packages:
  - `github.com/jung-kurt/gofpdf` (for PDF creation)

## Installation

1. Clone this repository or download the script:

```bash
git clone https://github.com/yourusername/nb-book-downloader.git
cd nb-book-downloader
```

2. Install the required dependencies:

```bash
go get github.com/jung-kurt/gofpdf
```

## Usage

### Basic Usage (Public Documents)

To download a publicly available book:

```bash
go run main.go -id 123456789
```

Or simply:

```bash
go run main.go 123456789
```

### Restricted Content (With Authentication)

To download restricted content using a cookie file (recommended):

```bash
go run main.go -id 000040863 -type pliktmonografi -cookie-file cookies.txt
```

Alternative method with direct cookie string:

```bash
go run main.go -id 000040863 -type pliktmonografi -cookies "_nblb=value; nbsso=value; NTID=value"
```

### Command Line Options

| Flag | Description | Default |
|------|-------------|---------|
| `-id` | Book ID to download | Required |
| `-type` | Document type: 'digibok' or 'pliktmonografi' | digibok |
| `-cookie-file` | Path to file containing authentication cookies | "" |
| `-cookies` | Authentication cookies in 'name1=value1; name2=value2' format | "" |
| `-length` | Book length (will calculate if not provided) | 0 |
| `-width` | Image width in pixels for higher quality | 602 |

## How to Create a Cookie File

For restricted content (pliktmonografi), the easiest way to authenticate is with a cookie file:

1. Log in to www.nb.no in your browser
2. Open developer tools (F12)
3. Go to the Network tab and reload the page
4. Click on any request to www.nb.no
5. In the "Headers" tab, find the "Cookie:" header
6. Copy the entire value (without the "Cookie:" prefix)
7. Paste it into a text file (e.g., `cookies.txt`) and save

Example cookie file content:
```
_nblb=value; nbsso=value; NTID=value; nb_dark_mode_enabled=true
```

## Examples

### Download a Public Book

```bash
go run main.go -id 123456789
```

### Download a Restricted Book with Cookie File

```bash
go run main.go -id 000040863 -type pliktmonografi -cookie-file cookies.txt
```

### Download with Known Page Count

```bash
go run main.go -id 123456789 -length 200
```

### Download Higher Quality Images

```bash
go run main.go -id 123456789 -width 1024
```

## Output

The script will:

1. Create a temporary folder to store downloaded images
2. Download all pages of the book (including front and back covers)
3. Combine all images into a PDF file named `[book-id].pdf`

## Troubleshooting

### Authentication Issues

If you receive "401 Unauthorized" or "403 Forbidden" errors with pliktmonografi documents:
- Ensure you're logged in to www.nb.no in your browser when you copy the cookies
- Make sure you've copied the entire cookie string without modifications
- Remember that your session may expire, requiring new cookies

### Download Failures

If image downloads fail:
- Check your internet connection
- Verify the book ID is correct
- Ensure the document exists and is accessible with your permissions
- Try with the `-length` parameter if auto-detection fails

## Limitations

- Session cookies expire, so you may need to update them for long downloads
- Very large books may take significant time and disk space
- The National Library may change their API or structure, requiring updates to this tool

## Legal Note

This tool is intended for personal use within the terms of service of the Norwegian National Library. Please respect copyright laws and the library's usage policies.

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Acknowledgments

- [akselsd/NB.no-Downloader](https://github.com/akselsd/NB.no-Downloader) - The original Python implementation this project is based on
- Norwegian National Library for providing digital access to their collection
- The Go community for excellent libraries and tools
