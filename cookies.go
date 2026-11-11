package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
)

// Cookies follow the browser convention for Domain: a leading dot marks a
// cookie for the domain and its subdomains, no dot a host-only cookie.

const ssoCookieName = "nbsso"

func hostWithin(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func nbCookie(c *http.Cookie) bool {
	return hostWithin(strings.TrimPrefix(c.Domain, "."), "nb.no")
}

func hasLogin(cookies []*http.Cookie) bool {
	for _, c := range cookies {
		if c.Name == ssoCookieName && c.Value != "" {
			return true
		}
	}
	return false
}

// newJar loads cookies into a jar, so each request carries only the cookies
// whose domain and path match it.
func newJar(cookies []*http.Cookie) (http.CookieJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	for _, c := range cookies {
		host := strings.TrimPrefix(c.Domain, ".")
		if host == "" {
			continue
		}
		cc := *c
		if strings.HasPrefix(c.Domain, ".") {
			cc.Domain = host
		} else {
			cc.Domain = "" // host-only
		}
		if cc.Path == "" {
			cc.Path = "/"
		}
		jar.SetCookies(&url.URL{Scheme: "https", Host: host, Path: "/"}, []*http.Cookie{&cc})
	}
	return jar, nil
}

// readCookies loads nb.no cookies from a file ("-" is stdin) holding either
// a browser Cookie header value ("a=1; b=2") or a Netscape cookies.txt
// export.
func readCookies(path string) ([]*http.Cookie, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read cookie file: %w", err)
	}
	cookies := parseCookies(string(data))
	if len(cookies) == 0 {
		return nil, errors.New("cookie file contains no nb.no cookies")
	}
	return cookies, nil
}

func parseCookies(text string) []*http.Cookie {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "Cookie:"))
	var cookies []*http.Cookie
	if strings.Contains(text, "\t") {
		// Netscape format: domain, include subdomains, path, secure,
		// expiry, name, value.
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimRight(line, "\r")
			line = strings.TrimPrefix(line, "#HttpOnly_")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Split(line, "\t")
			if len(f) != 7 {
				continue
			}
			domain := strings.TrimPrefix(f[0], ".")
			if f[1] == "TRUE" {
				domain = "." + domain
			}
			c := &http.Cookie{Name: f[5], Value: f[6], Domain: domain, Path: f[2]}
			if nbCookie(c) {
				cookies = append(cookies, c)
			}
		}
		return cookies
	}
	// A Cookie header does not say where each cookie came from; send them
	// to all of nb.no.
	for _, part := range strings.Split(text, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && name != "" {
			cookies = append(cookies, &http.Cookie{Name: name, Value: value, Domain: ".nb.no", Path: "/"})
		}
	}
	return cookies
}
