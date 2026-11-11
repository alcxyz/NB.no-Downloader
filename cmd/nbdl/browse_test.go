package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

func TestBookFileName(t *testing.T) {
	tests := []struct{ title, id, want string }{
		{"Gengangere : av Henrik Ibsen", "URN:NBN:no-nb_digibok_2020062548675", "Gengangere - av Henrik Ibsen (digibok_2020062548675).pdf"},
		{"A/B: \"C\"\t?", "296c0829d64f7ba7b3a684d912920284", "A B C (296c0829d64f7ba7b3a684d912920284).pdf"},
		{"", "URN:NBN:no-nb_digibok_1", "digibok_1.pdf"},
	}
	for _, tt := range tests {
		if got := bookFileName(tt.title, tt.id, "pdf"); got != tt.want {
			t.Errorf("bookFileName(%q, %q) = %q, want %q", tt.title, tt.id, got, tt.want)
		}
	}
}

// TestBrowseEndToEnd clicks the download button on a public nb.no book in a
// headless browser. It needs network access and a Chromium-based browser,
// so it runs only with NBDL_E2E=1.
func TestBrowseEndToEnd(t *testing.T) {
	if os.Getenv("NBDL_E2E") == "" {
		t.Skip("set NBDL_E2E=1 to run against nb.no")
	}
	t.Setenv("NBDL_PROFILE", t.TempDir())
	dir := t.TempDir()

	b, l, err := launchBrowser(true)
	if err != nil {
		t.Fatal(err)
	}
	defer waitExit(l)
	defer b.Close()
	first, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- runBrowse(ctx, b, first, "https://www.nb.no/items/URN:NBN:no-nb_digibok_2020062548675",
			dir, saveOptions{format: "pdf", width: 300, workers: 4})
	}()

	page := first.Timeout(2 * time.Minute)
	btn, err := page.ElementR("button", "Download PDF")
	if err != nil {
		t.Fatalf("no download button: %v", err)
	}
	if err := btn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := page.ElementR("button", "Saved"); err != nil {
		text, _ := btn.Text()
		t.Fatalf("download did not finish (button says %q): %v", text, err)
	}

	want := filepath.Join(dir, "Gengangere - av Henrik Ibsen (digibok_2020062548675).pdf")
	info, err := os.Stat(want)
	if err != nil {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("PDF missing: %v (dir has %v)", err, entries)
	}
	if info.Size() < 10_000 {
		t.Errorf("PDF is only %d bytes", info.Size())
	}

	// A tab opened later, as when opening a book in a new tab, gets the
	// button too.
	second, err := b.Page(proto.TargetCreateTarget{URL: "https://www.nb.no/items/URN:NBN:no-nb_digibok_2008010300041"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Timeout(time.Minute).ElementR("button", "Download PDF"); err != nil {
		t.Errorf("no download button in a new tab: %v", err)
	}

	cancel()
	if err := <-errc; err != nil {
		t.Errorf("runBrowse: %v", err)
	}
}
