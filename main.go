package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jung-kurt/gofpdf"
)

// Book represents a book to be downloaded
type Book struct {
	id           string
	length       int
	retry        int
	path         string
	fullpath     string
	urlTemplate  string
	client       *http.Client
	documentType string // "digibok" or "pliktmonografi"
	params       map[string]string
}

// NewBook creates a new Book instance
func NewBook(bookID string, length int, docType string, cookies []*http.Cookie) *Book {
	// Default to digibok if not specified
	if docType == "" {
		docType = "digibok"
	}

	// Create cookie jar to maintain session
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
	}

	// Direct image URL template based on browser requests
	urlTemplate := "https://www.nb.no/services/image/resolver/URN:NBN:no-nb_{docType}_{book_id}_{long_page_nr}/full/602,/0/default.jpg"
	urlTemplate = strings.Replace(urlTemplate, "{docType}", docType, 1)

	b := &Book{
		id:     bookID,
		length: length,
		retry:  2,
		params: map[string]string{
			"book_id":      bookID,
			"page_nr":      "1",
			"long_page_nr": "0001",
		},
		path:         bookID + "_temp_image_folder",
		urlTemplate:  urlTemplate,
		client:       client,
		documentType: docType,
	}

	// Set authentication cookies if provided
	if len(cookies) > 0 {
		baseURL, _ := url.Parse("https://www.nb.no")
		b.client.Jar.SetCookies(baseURL, cookies)
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

// downloadPage downloads a single page directly
func (b *Book) downloadPage(pageNr string, retry int) {
	b.updateParams(pageNr)
	url := b.formatURL()

	fmt.Printf("Downloading page %s: %s\n", pageNr, url)

	resp, err := b.client.Get(url)
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Printf("Download Error: HTTP Status %d\n", resp.StatusCode)
		fmt.Println("Tried to access " + url)

		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			fmt.Println("Authentication failed - check your cookies.")
			fmt.Println("Try using -cookies with all cookies from your authenticated browser session.")
			dumpCookies(b.client, "https://www.nb.no")
		}

		if b.retry >= 0 {
			fmt.Printf("Retrying.... %d tries remaining.\n", b.retry)
			b.retry--
			b.downloadPage(pageNr, retry) // Recursively retry
		} else {
			fmt.Println("All retries failed")
		}
		if resp != nil {
			resp.Body.Close()
		}
		return
	}

	// Download successful, save the image
	imgData, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		fmt.Println("Error reading response:", err)
		return
	}

	// Save the image directly
	outPath := filepath.Join(b.path, pageNr+".jpg")
	outFile, err := os.Create(outPath)
	if err != nil {
		fmt.Println("Error creating output file:", err)
		return
	}
	defer outFile.Close()

	_, err = outFile.Write(imgData)
	if err != nil {
		fmt.Println("Error writing image file:", err)
		return
	}

	fmt.Printf("Page %s downloaded successfully\n", pageNr)
	b.retry = 2 // Reset retry count for next page
}

// dumpCookies prints the current cookies in the client jar (for debugging)
func dumpCookies(client *http.Client, urlStr string) {
	if client.Jar == nil {
		fmt.Println("No cookie jar available")
		return
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		fmt.Println("Error parsing URL for cookie dump:", err)
		return
	}

	cookies := client.Jar.Cookies(parsedURL)
	if len(cookies) == 0 {
		fmt.Println("No cookies found in jar")
		return
	}

	fmt.Println("Current cookies in jar:")
	for _, cookie := range cookies {
		fmt.Printf("  %s = %s\n", cookie.Name, cookie.Value)
	}
}

// findBookLength attempts to determine the book's length
func (b *Book) findBookLength() int {
	delta := 100
	j := 100

	for {
		b.updateParams(strconv.Itoa(j))
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

	// Front Cover
	b.downloadPage("C1", b.retry)

	// Check for Introduction pages (I1, I2, etc.)
	introPageNum := 1
	for {
		introPage := fmt.Sprintf("I%d", introPageNum)
		tempRetry := b.retry

		b.updateParams(introPage)
		url := b.formatURL()

		resp, err := b.client.Head(url)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			break
		}
		resp.Body.Close()

		// The page exists, download it
		b.downloadPage(introPage, tempRetry)
		introPageNum++
	}

	// Download all numbered pages
	for page := 1; page <= b.length; page++ {
		pageStr := strconv.Itoa(page)
		b.downloadPage(pageStr, b.retry)
	}

	// Back Cover
	b.downloadPage("C3", b.retry)

	// Now create the PDF
	fmt.Println("Creating PDF...")

	// Add front cover
	pdfPath := filepath.Join(b.path, "C1.jpg")
	if _, err := os.Stat(pdfPath); err == nil {
		pdf.AddPage()
		pdf.Image(pdfPath, 0, 0, 210, 297, false, "", 0, "")
	}

	// Add intro pages
	for i := 1; i <= introPageNum-1; i++ {
		introPage := fmt.Sprintf("I%d", i)
		pdfPath := filepath.Join(b.path, introPage+".jpg")
		if _, err := os.Stat(pdfPath); err == nil {
			pdf.AddPage()
			pdf.Image(pdfPath, 0, 0, 210, 297, false, "", 0, "")
		}
	}

	// Add all numbered pages
	for page := 1; page <= b.length; page++ {
		pageStr := strconv.Itoa(page)
		pdfPath := filepath.Join(b.path, pageStr+".jpg")
		if _, err := os.Stat(pdfPath); err == nil {
			pdf.AddPage()
			pdf.Image(pdfPath, 0, 0, 210, 297, false, "", 0, "")
		}
	}

	// Add back cover
	pdfPath = filepath.Join(b.path, "C3.jpg")
	if _, err := os.Stat(pdfPath); err == nil {
		pdf.AddPage()
		pdf.Image(pdfPath, 0, 0, 210, 297, false, "", 0, "")
	}

	// Save the PDF
	err := pdf.OutputFileAndClose(b.id + ".pdf")
	if err != nil {
		fmt.Println("Error saving PDF:", err)
		return
	}
	fmt.Println("PDF saved of book", b.id)
}

// updateParams updates the request parameters
func (b *Book) updateParams(pageNr string) {
	if pageNr != "" {
		b.params["page_nr"] = pageNr
		if _, err := strconv.Atoi(pageNr); err == nil {
			// If pageNr is a number, pad it with zeros
			b.params["long_page_nr"] = fmt.Sprintf("%04s", pageNr)
		} else {
			b.params["long_page_nr"] = pageNr
		}
	}
}

// parseCookiesString parses a cookie string into http.Cookie objects
func parseCookiesString(cookiesStr string) []*http.Cookie {
	var cookies []*http.Cookie

	if cookiesStr == "" {
		return cookies
	}

	cookiePairs := strings.Split(cookiesStr, ";")
	for _, pair := range cookiePairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			cookies = append(cookies, &http.Cookie{
				Name:  parts[0],
				Value: parts[1],
				Path:  "/", // Set path to root
			})
		}
	}

	return cookies
}

func main() {
	// Define command-line flags
	bookID := flag.String("id", "", "Book ID to download")
	docType := flag.String("type", "digibok", "Document type: 'digibok' or 'pliktmonografi'")
	cookiesStr := flag.String("cookies", "", "Authentication cookies in 'name1=value1; name2=value2' format")
	bookLength := flag.Int("length", 0, "Book length (will calculate if not provided)")
	imageWidth := flag.Int("width", 602, "Image width to request (default is 602px)")

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

	// Parse cookie string
	var cookies []*http.Cookie
	if *cookiesStr != "" {
		cookies = parseCookiesString(*cookiesStr)
		fmt.Printf("Using %d cookies from provided cookie string\n", len(cookies))

		// Print cookie names for debugging
		cookieNames := make([]string, len(cookies))
		for i, cookie := range cookies {
			cookieNames[i] = cookie.Name
		}
		fmt.Printf("Cookie names: %s\n", strings.Join(cookieNames, ", "))
	}

	// Warn if trying to download pliktmonografi without cookies
	if *docType == "pliktmonografi" && len(cookies) == 0 {
		fmt.Println("WARNING: pliktmonografi documents typically require authentication.")
		fmt.Println("If download fails, please provide authentication cookies with -cookies flag.")
	}

	b := NewBook(*bookID, *bookLength, *docType, cookies)

	// Update image width in URL template if specified
	if *imageWidth != 602 {
		b.urlTemplate = strings.Replace(b.urlTemplate, "602,", fmt.Sprintf("%d,", *imageWidth), 1)
		fmt.Printf("Using custom image width: %dpx\n", *imageWidth)
	}

	b.downloadBook()
}
