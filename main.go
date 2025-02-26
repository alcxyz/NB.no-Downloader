package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jung-kurt/gofpdf"
	_ "image/png" // Register PNG format
)

// Book represents a book to be downloaded
type Book struct {
	id           string
	length       int
	rows         int
	cols         int
	imgSize      [2]int
	params       map[string]string
	retry        int
	path         string
	fullpath     string
	urlTemplate  string
	client       *http.Client
	documentType string // "digibok" or "pliktmonografi"
}

// NewBook creates a new Book instance
func NewBook(bookID string, length int, docType string, cookieValue string, cookieName string) *Book {
	// Default to digibok if not specified
	if docType == "" {
		docType = "digibok"
	}

	// Create cookie jar to maintain session
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
	}

	urlTemplate := "https://www.nb.no/services/image/resolver?url_ver=geneza&urn=URN:NBN:no-nb_{docType}_{book_id}_{long_page_nr}&maxLevel=5&level=5&col={col}&row={row}&resX=9999&resY=9999&tileWidth=1024&tileHeight=1024&pg_id={page_nr}"
	urlTemplate = strings.Replace(urlTemplate, "{docType}", docType, 1)

	b := &Book{
		id:      bookID,
		length:  length,
		rows:    -1,
		cols:    -1,
		imgSize: [2]int{0, 0},
		params: map[string]string{
			"book_id":      bookID,
			"page_nr":      "1",
			"long_page_nr": "0001",
			"col":          "0",
			"row":          "0",
		},
		retry:        2,
		path:         bookID + "_temp_image_folder",
		urlTemplate:  urlTemplate,
		client:       client,
		documentType: docType,
	}

	// Set authentication cookie if provided
	if cookieValue != "" {
		baseURL, _ := url.Parse("https://www.nb.no")

		// Use provided cookie name or default
		if cookieName == "" {
			cookieName = "JSESSIONID" // Default cookie name
		}

		cookie := &http.Cookie{
			Name:  cookieName,
			Value: cookieValue,
			Path:  "/",
		}
		b.client.Jar.SetCookies(baseURL, []*http.Cookie{cookie})
	}

	execPath, err := os.Executable()
	if err == nil {
		b.fullpath = filepath.Join(filepath.Dir(execPath), b.path)
	} else {
		b.fullpath = b.path
	}

	if _, err := os.Stat(b.fullpath); os.IsNotExist(err) {
		os.Mkdir(b.path, 0755)
	}

	b.findRowsColsAndImgSize()
	return b
}

// formatURL replaces template placeholders with actual values
func (b *Book) formatURL() string {
	url := b.urlTemplate
	for key, value := range b.params {
		url = strings.Replace(url, "{"+key+"}", value, -1)
	}
	return url
}

// downloadPage downloads and assembles a single page
func (b *Book) downloadPage(pageNr string, retry int) {
	// Create a new white image with the determined dimensions
	img := image.NewRGBA(image.Rect(0, 0, b.imgSize[0], b.imgSize[1]))
	xOffset := 0
	yOffset := 0

	for row := 0; row < b.rows; row++ {
		col := 0
		for col < b.cols {
			b.updateParams(pageNr, strconv.Itoa(col), strconv.Itoa(row))
			url := b.formatURL()

			resp, err := b.client.Get(url)
			if err != nil || resp.StatusCode != http.StatusOK {
				fmt.Println("Download Error: Is the page number, column or row too high?")
				fmt.Println("Tried to access " + url)

				if resp != nil && resp.StatusCode == http.StatusUnauthorized {
					fmt.Println("Authentication error - please check your cookie value")
					os.Exit(1)
				}

				if b.retry >= 0 {
					fmt.Printf("Retrying.... %d tries remaining.\n", b.retry)
					b.retry--
					col--
				} else {
					fmt.Println("All retries failed")
				}
				if resp != nil {
					resp.Body.Close()
				}
			} else {
				imgData, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					fmt.Println("Error reading response:", err)
					continue
				}

				partialPage, _, err := image.Decode(bytes.NewReader(imgData))
				if err != nil {
					fmt.Println("Error decoding image:", err)
					continue
				}

				// Draw the partial image onto the main image
				bounds := partialPage.Bounds()
				draw.Draw(img, image.Rect(xOffset, yOffset, xOffset+bounds.Dx(), yOffset+bounds.Dy()),
					partialPage, bounds.Min, draw.Src)

				xOffset += bounds.Dx()

				// Finished this row
				if col == b.cols-1 {
					xOffset = 0
					yOffset += bounds.Dy()
				}
			}
			col++
		}
	}

	// Save the assembled image
	outPath := filepath.Join(b.path, pageNr+".jpg")
	outFile, err := os.Create(outPath)
	if err != nil {
		fmt.Println("Error creating output file:", err)
		return
	}
	defer outFile.Close()

	jpeg.Encode(outFile, img, &jpeg.Options{Quality: 90})
}

// findBookLength attempts to determine the book's length
func (b *Book) findBookLength() int {
	delta := 100
	j := 100

	for {
		b.updateParams(strconv.Itoa(j), "0", "0")
		url := b.formatURL()

		resp, err := b.client.Get(url)
		if err != nil || resp.StatusCode != http.StatusOK {
			// Too far
			if delta == 1 {
				return j - 1
			}
			j -= delta
			delta = delta / 10
			if delta < 1 {
				delta = 1
			}
			j += delta
		} else {
			resp.Body.Close()
			j += delta
		}
	}
}

// downloadBook downloads all pages and creates a PDF
func (b *Book) downloadBook() {
	// Create PDF
	pdf := gofpdf.New("P", "mm", "Letter", "")

	if b.length == 0 {
		fmt.Println("Length not specified, calculating book length")
		b.length = b.findBookLength()
		fmt.Println("Book length found:", b.length)
	}

	fmt.Printf("Downloading book %s (type: %s)\n", b.id, b.documentType)
	retry := 2

	// Front Cover
	b.downloadPage("C1", retry)
	b.retry = 2
	pdf.AddPage()
	pdf.Image(filepath.Join(b.path, "C1.jpg"), 0, 0, 210, 297, false, "", 0, "")

	// Download all pages
	for page := 1; page <= b.length; page++ {
		pageStr := strconv.Itoa(page)
		b.downloadPage(pageStr, retry)
		fmt.Println("Page", page, "download complete")
		b.retry = 2
	}

	// Add all pages to PDF
	for page := 1; page <= b.length; page++ {
		pageStr := strconv.Itoa(page)
		pdf.AddPage()
		pdf.Image(filepath.Join(b.path, pageStr+".jpg"), 0, 0, 210, 297, false, "", 0, "")
	}

	// Back Cover
	b.downloadPage("C3", retry)
	pdf.AddPage()
	pdf.Image(filepath.Join(b.path, "C3.jpg"), 0, 0, 210, 297, false, "", 0, "")

	// Save the PDF
	err := pdf.OutputFileAndClose(b.id + ".pdf")
	if err != nil {
		fmt.Println("Error saving PDF:", err)
		return
	}
	fmt.Println("PDF saved of book", b.id)
}

// updateParams updates the request parameters
func (b *Book) updateParams(pageNr, col, row string) {
	if pageNr != "" {
		b.params["page_nr"] = pageNr
		if _, err := strconv.Atoi(pageNr); err == nil {
			// If pageNr is a number, pad it with zeros
			b.params["long_page_nr"] = fmt.Sprintf("%04s", pageNr)
		} else {
			b.params["long_page_nr"] = pageNr
		}
	}

	if col != "" {
		b.params["col"] = col
	}

	if row != "" {
		b.params["row"] = row
	}
}

// findRowsColsAndImgSize determines the grid size and image dimensions
func (b *Book) findRowsColsAndImgSize() {
	// Find rows
	for {
		b.rows++
		b.updateParams("1", "0", strconv.Itoa(b.rows))
		url := b.formatURL()

		resp, err := b.client.Get(url)
		if err != nil || resp.StatusCode != http.StatusOK {
			break
		}

		imgData, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			break
		}

		img, _, err := image.Decode(bytes.NewReader(imgData))
		if err != nil {
			break
		}

		bounds := img.Bounds()
		b.imgSize[1] += bounds.Dy()
	}

	// Find columns
	for {
		b.cols++
		b.updateParams("1", strconv.Itoa(b.cols), "0")
		url := b.formatURL()

		resp, err := b.client.Get(url)
		if err != nil || resp.StatusCode != http.StatusOK {
			break
		}

		imgData, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			break
		}

		img, _, err := image.Decode(bytes.NewReader(imgData))
		if err != nil {
			break
		}

		bounds := img.Bounds()
		b.imgSize[0] += bounds.Dx()
	}
}

func main() {
	// Define command-line flags
	bookID := flag.String("id", "", "Book ID to download")
	docType := flag.String("type", "digibok", "Document type: 'digibok' or 'pliktmonografi'")
	cookieValue := flag.String("cookie", "", "Authentication cookie value (required for pliktmonografi)")
	cookieName := flag.String("cookie-name", "JSESSIONID", "Authentication cookie name")
	bookLength := flag.Int("length", 0, "Book length (will calculate if not provided)")

	flag.Parse()

	// Check for required book ID
	if *bookID == "" {
		// Check if book ID was provided as a positional argument
		if flag.NArg() > 0 {
			*bookID = flag.Arg(0)
		} else {
			fmt.Println("Please provide a book ID with -id flag or as first argument")
			flag.Usage()
			os.Exit(1)
		}
	}

	// Warn if trying to download pliktmonografi without cookie
	if *docType == "pliktmonografi" && *cookieValue == "" {
		fmt.Println("Warning: pliktmonografi documents typically require authentication.")
		fmt.Println("If download fails, please provide an authentication cookie value with -cookie flag.")
	}

	b := NewBook(*bookID, *bookLength, *docType, *cookieValue, *cookieName)
	b.downloadBook()
}
