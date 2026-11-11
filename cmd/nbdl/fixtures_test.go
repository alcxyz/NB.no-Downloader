package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestFixtureBooks lays out real books from work folders kept with
// -keep-pages and prints what came out, to judge changes to the layout and
// correction rules. Book text is copyrighted, so the folders stay outside
// the repository: set NBDL_FIXTURES to the folder holding them.
func TestFixtureBooks(t *testing.T) {
	root := os.Getenv("NBDL_FIXTURES")
	if root == "" {
		t.Skip("set NBDL_FIXTURES to a folder of *_work directories")
	}
	dirs, _ := filepath.Glob(filepath.Join(root, "*_work"))
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			pages := loadWorkDir(t, dir)
			before := map[*textWord]string{}
			for _, lp := range pages {
				eachWord(lp, func(w *textWord) { before[w] = w.Text })
			}
			st := correctWords(pages)
			var textPages []int
			for i, lp := range pages {
				if lp.Kind == pageNormal {
					textPages = append(textPages, i)
				}
			}
			// Without the page images, take pages that might be blank as
			// blank.
			for _, i := range markPictures(pages, textPages) {
				pages[i].Blank = true
			}
			var changes []string
			for w, old := range before {
				if w.Text != old {
					changes = append(changes, old+" → "+w.Text)
				}
			}
			sort.Strings(changes)

			chs, figs := layoutBook(pages)
			var body strings.Builder
			for _, ch := range chs {
				body.WriteString(ch.Body)
			}
			b := body.String()
			fmt.Printf("== %s: %d pages, %d chapters, %d figures, %d paragraphs, %d notes, %d breaks, %d h2\n",
				filepath.Base(dir), len(pages), len(chs), len(figs), strings.Count(b, "<p"), strings.Count(b, `class="note"`),
				strings.Count(b, `class="break"`), strings.Count(b, "<h2>"))
			fmt.Printf("   corrected %d of %d doubtful words, removed %d note markers\n", st.corrected, st.doubtful, st.markers)
			for i, c := range changes {
				if i == 40 {
					fmt.Printf("   ... %d more\n", len(changes)-40)
					break
				}
				fmt.Printf("   %s\n", c)
			}
			for _, ch := range chs {
				fmt.Printf("   - %s\n", ch.Title)
			}
		})
	}
}

// loadWorkDir reads the cached text of a book in page order.
func loadWorkDir(t *testing.T, dir string) []layoutPage {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	byPage := map[int]*textPage{}
	last := 0
	for _, e := range entries {
		var n int
		name := e.Name()
		if _, err := fmt.Sscanf(name, "%04d.", &n); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var pg *textPage
		switch {
		case strings.HasSuffix(name, ".tsv"):
			pg, err = parseTSV(string(data))
		case strings.HasSuffix(name, ".alto.xml"):
			pg, err = parseALTO(strings.NewReader(string(data)))
		default:
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		byPage[n] = pg
		last = max(last, n)
	}
	pages := make([]layoutPage, last)
	for n, pg := range byPage {
		pages[n-1].Text = pg
	}
	for i := range pages {
		if pages[i].Text == nil {
			pages[i].Kind = pageSkip
		}
	}
	return pages
}
