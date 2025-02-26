# Norwegian National Library Book Downloader

A Go utility to download and convert books from the Norwegian National Library (Nasjonalbiblioteket) into PDF files.

## Description

This tool allows you to download books from the Norwegian National Library's digital collection and convert them to PDF format. It supports both publicly available books (`digibok_` prefix) and restricted content (`pliktmonografi_` prefix) that requires authentication.

This is a Go port of [akselsd/NB.no-Downloader](https://github.com/akselsd/NB.no-Downloader), originally written in Python, with additional features and improvements.

## Features

- Download complete books from the Norwegian National Library
- Support for both public (`digibok`) and restricted (`pliktmonografi`) document types
- Automatically determine book length if not specified
- Authentication support for restricted content
- Assemble tiled images into complete pages
- Convert all pages into a single PDF file

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

To download restricted content that requires authentication:

```bash
go run main.go -id 000040863 -type pliktmonografi -cookie "your-session-cookie-value"
```

### Command Line Options

| Flag | Description | Default |
|------|-------------|---------|
| `-id` | Book ID to download | Required |
| `-type` | Document type: 'digibok' or 'pliktmonografi' | digibok |
| `-cookie` | Authentication cookie value (required for restricted content) | "" |
| `-cookie-name` | Authentication cookie name | "JSESSIONID" |
| `-length` | Book length (will calculate if not provided) | 0 |

## How to Find Your Authentication Cookie

For restricted content (pliktmonografi), you need to provide an authentication cookie:

1. Log in to www.nb.no in your browser
2. Open developer tools (F12 in most browsers)
3. Go to the Application/Storage tab
4. Find Cookies for the www.nb.no domain
5. Look for authentication cookies (often JSESSIONID or similar)
6. Copy the Value field to use with the `-cookie` flag

![Finding cookies in browser](https://i.imgur.com/example.png)

## Examples

### Download a Public Book

```bash
go run main.go -id 123456789
```

### Download a Restricted Book with Authentication

```bash
go run main.go -id 000040863 -type pliktmonografi -cookie "1A2B3C4D5E6F7G8H9I0J"
```

### Download with Known Page Count

```bash
go run main.go -id 123456789 -length 200
```

### Using a Different Cookie Name

```bash
go run main.go -id 000040863 -type pliktmonografi -cookie "1A2B3C4D5E6F7G8H9I0J" -cookie-name "NB_SESSION"
```

## Output

The script will:

1. Create a temporary folder to store downloaded images
2. Download all pages of the book (including front and back covers)
3. Combine all images into a PDF file named `[book-id].pdf`

## Troubleshooting

### Authentication Issues

If you receive "401 Unauthorized" errors with pliktmonografi documents:
- Ensure you're logged in to www.nb.no in your browser
- Check that you've copied the correct cookie value
- Try using a different cookie (some sites use multiple cookies for authentication)
- Remember that your session may expire, requiring a new cookie value

### Download Failures

If image downloads fail:
- Check your internet connection
- Verify the book ID is correct
- Ensure the document exists and is accessible with your permissions
- Try with the `-length` parameter if auto-detection fails

## Limitations

- The script requires you to be logged in to access restricted content
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
