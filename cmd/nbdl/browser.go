package main

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

// browserPath finds a Chromium-based browser: $NBDL_BROWSER, then an
// installed Chrome, Chromium or Edge, and finally a Chromium build that
// go-rod downloads into its cache.
func browserPath() (string, error) {
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

// launchBrowser starts the browser on the dedicated profile.
func launchBrowser(headless bool) (*rod.Browser, *launcher.Launcher, error) {
	bin, err := browserPath()
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
	b := rod.New().ControlURL(controlURL)
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, nil, err
	}
	return b, l, nil
}

// waitExit waits for the browser process to exit, so it has written the
// profile and released its lock. launcher.Cleanup also deletes the user data
// directory, which holds the login; clearing the flag first (it is only read
// at launch) makes Cleanup only wait.
func waitExit(l *launcher.Launcher) {
	l.Delete(flags.UserDataDir)
	l.Cleanup()
}

// nbCookies returns the browser's nb.no cookies.
func nbCookies(b *rod.Browser) ([]*http.Cookie, error) {
	all, err := b.GetCookies()
	if err != nil {
		return nil, err
	}
	var cookies []*http.Cookie
	for _, c := range all {
		hc := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path}
		if nbCookie(hc) {
			cookies = append(cookies, hc)
		}
	}
	return cookies, nil
}

// browserSession reads the saved nb.no cookies from the dedicated profile
// without showing a window. It returns none when no profile exists yet.
func browserSession() ([]*http.Cookie, error) {
	dir, err := profileDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	b, l, err := launchBrowser(true)
	if err != nil {
		return nil, err
	}
	defer func() {
		b.Close()
		waitExit(l)
	}()
	cookies, err := nbCookies(b)
	if err != nil || hasLogin(cookies) {
		return cookies, err
	}
	// The nbsso cookie may not outlive the browser; visiting the login page
	// renews it silently while the identity provider session is still valid.
	page, err := b.Page(proto.TargetCreateTarget{URL: loginURL})
	if err != nil {
		return cookies, nil
	}
	defer page.Close()
	for i := 0; i < 10 && !hasLogin(cookies); i++ {
		time.Sleep(time.Second)
		if cookies, err = nbCookies(b); err != nil {
			return nil, err
		}
	}
	return cookies, nil
}

// login opens a browser window on the nb.no login page and waits until the
// SSO cookie appears or the user closes the window.
func login() error {
	b, l, err := launchBrowser(false)
	if err != nil {
		return err
	}
	defer waitExit(l)

	if cookies, err := nbCookies(b); err == nil && hasLogin(cookies) {
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
		cookies, err := nbCookies(b)
		if err != nil {
			// The user closed the window.
			return errors.New("browser closed before login finished")
		}
		if hasLogin(cookies) {
			fmt.Fprintln(os.Stderr, "logged in; the session is saved for later downloads")
			time.Sleep(2 * time.Second) // let the browser persist the cookies
			return b.Close()
		}
	}
}

// logout deletes the dedicated browser profile.
func logout() error {
	dir, err := profileDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "removed", dir)
	return nil
}
