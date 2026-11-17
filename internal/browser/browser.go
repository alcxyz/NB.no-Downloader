// Package browser runs the dedicated Chromium profile that holds the nb.no
// login.
package browser

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"

	"github.com/alcxyz/NB.no-Downloader/internal/nb"
)

const (
	loginURL = "https://www.nb.no/auth/login.html?returnTo=https://www.nb.no/"
)

// profileDir is the dedicated browser profile that holds the nb.no login.
func profileDir() (string, error) {
	if dir := os.Getenv("NBDL_PROFILE"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "nb-downloader", "browser-profile"), nil
}

// Path finds a Chromium-based browser: $NBDL_BROWSER, then an
// installed Chrome, Chromium or Edge, and finally a Chromium build that
// go-rod downloads into its cache.
func Path() (string, error) {
	if path := os.Getenv("NBDL_BROWSER"); path != "" {
		return path, nil
	}
	if path, ok := launcher.LookPath(); ok {
		return path, nil
	}
	fmt.Fprintln(os.Stderr, "no Chrome, Chromium or Edge found; downloading Chromium (set NBDL_BROWSER to use another browser)")
	path, err := launcher.NewBrowser().Get()
	if err != nil {
		return "", fmt.Errorf("download Chromium: %w", err)
	}
	return path, nil
}

// Launch starts the browser on the dedicated profile.
func Launch(headless bool) (*rod.Browser, *launcher.Launcher, error) {
	bin, err := Path()
	if err != nil {
		return nil, nil, err
	}
	dir, err := profileDir()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	l := launcher.New().
		Bin(bin).
		UserDataDir(dir).
		Headless(headless).
		Leakless(false).
		Set("no-first-run").
		// Keep session cookies such as nbsso across browser restarts.
		Set("restore-last-session").
		Set("no-default-browser-check")
	// rod's launcher defaults suit automation; a login window should look
	// and behave like a normal browser.
	l.Delete("enable-automation")
	controlURL, err := l.Launch()
	if err != nil {
		if strings.Contains(err.Error(), "ProcessSingleton") || strings.Contains(err.Error(), "profile appears to be in use") {
			return nil, nil, fmt.Errorf("the nb.no browser profile is in use; close the nbdl browser window first")
		}
		return nil, nil, fmt.Errorf("launch browser %s: %w", bin, err)
	}
	// rod emulates a fixed laptop screen on new pages by default, which
	// leaves most of a larger window empty; let pages fill the window.
	b := rod.New().ControlURL(controlURL).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, nil, err
	}
	return b, l, nil
}

// WaitExit waits for the browser process to exit, so it has written the
// profile and released its lock. launcher.Cleanup also deletes the user data
// directory, which holds the login; clearing the flag first (it is only read
// at launch) makes Cleanup only wait.
func WaitExit(l *launcher.Launcher) {
	l.Delete(flags.UserDataDir)
	l.Cleanup()
}

// Cookies returns the browser's nb.no cookies.
func Cookies(b *rod.Browser) ([]*http.Cookie, error) {
	all, err := b.GetCookies()
	if err != nil {
		return nil, err
	}
	var cookies []*http.Cookie
	for _, c := range all {
		hc := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path}
		if nb.OwnsCookie(hc) {
			cookies = append(cookies, hc)
		}
	}
	return cookies, nil
}

// Session reads the saved nb.no cookies from the dedicated profile
// without showing a window. It returns none when no profile exists yet.
func Session() ([]*http.Cookie, error) {
	dir, err := profileDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	b, l, err := Launch(true)
	if err != nil {
		return nil, err
	}
	defer func() {
		b.Close()
		WaitExit(l)
	}()
	cookies, err := Cookies(b)
	if err != nil || nb.HasLogin(cookies) {
		return cookies, err
	}
	// The nbsso cookie may not outlive the browser; visiting the login page
	// renews it silently while the identity provider session is still valid.
	page, err := b.Page(proto.TargetCreateTarget{URL: loginURL})
	if err != nil {
		return cookies, nil
	}
	defer page.Close()
	for i := 0; i < 10 && !nb.HasLogin(cookies); i++ {
		time.Sleep(time.Second)
		if cookies, err = Cookies(b); err != nil {
			return nil, err
		}
	}
	return cookies, nil
}

// Login opens a browser window on the nb.no login page and waits until the
// SSO cookie appears or the user closes the window.
func Login() error {
	b, l, err := Launch(false)
	if err != nil {
		return err
	}
	defer WaitExit(l)

	if cookies, err := Cookies(b); err == nil && nb.HasLogin(cookies) {
		fmt.Fprintln(os.Stderr, "already logged in; opening nb.no so you can check or log out")
	}
	if _, err := b.Page(proto.TargetCreateTarget{URL: loginURL}); err != nil {
		b.Close()
		return err
	}
	fmt.Fprintln(os.Stderr, "Log in to nb.no in the browser window (for example with BankID).")
	fmt.Fprintln(os.Stderr, "The window closes by itself once you are logged in.")

	for {
		time.Sleep(time.Second)
		cookies, err := Cookies(b)
		if err != nil {
			// The user closed the window.
			return errors.New("browser closed before login finished")
		}
		if nb.HasLogin(cookies) {
			fmt.Fprintln(os.Stderr, "logged in; the session is saved for later downloads")
			time.Sleep(2 * time.Second) // let the browser persist the cookies
			return b.Close()
		}
	}
}

// Logout deletes the dedicated browser profile.
func Logout() error {
	dir, err := profileDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "removed", dir)
	// Chromium keeps the disk cache of a profile under ~/.config/X in
	// ~/.cache/X.
	if config, err := os.UserConfigDir(); err == nil && os.Getenv("NBDL_PROFILE") == "" {
		if cache, err := os.UserCacheDir(); err == nil {
			if rel, err := filepath.Rel(config, dir); err == nil {
				os.RemoveAll(filepath.Join(cache, rel))
			}
		}
	}
	return nil
}
