package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const catalogBase = "https://api.nb.no/catalog/v1/items/"

type catalogItem struct {
	Metadata struct {
		Title      string `json:"title"`
		TitleInfos []struct {
			Title    string `json:"title"`
			SubTitle string `json:"subTitle"`
			Type     string `json:"type"`
		} `json:"titleInfos"`
		People []struct {
			Name  string `json:"name"`
			Usage string `json:"usage"`
			Roles []struct {
				Name string `json:"name"`
			} `json:"roles"`
		} `json:"people"`
		Identifiers struct {
			URN    string   `json:"urn"`
			ISBN13 []string `json:"isbn13"`
			ISBN10 []string `json:"isbn10"`
		} `json:"identifiers"`
		OriginInfo struct {
			Publisher string `json:"publisher"`
			Issued    string `json:"issued"`
		} `json:"originInfo"`
		Languages []struct {
			Code string `json:"code"`
		} `json:"languages"`
		Subject struct {
			Topics []string `json:"topics"`
		} `json:"subject"`
		Summary string `json:"summary"`
	} `json:"metadata"`
	AccessInfo struct {
		IsPublicDomain bool `json:"isPublicDomain"`
	} `json:"accessInfo"`
}

// catalogInfo is what the EPUB needs from nb.no's catalogue.
type catalogInfo struct {
	Meta      bookMeta
	Languages []string // catalogue codes, e.g. "nob"
}

// fetchCatalog reads the catalogue record. The record is optional: when it
// is missing the book still converts, with the manifest title only.
func (d *downloader) fetchCatalog(ctx context.Context, identifier string) (*catalogInfo, error) {
	body, err := d.fetch(ctx, d.catalogURL(identifier), "application/")
	if err != nil {
		return nil, fmt.Errorf("fetch catalogue record: %w", err)
	}
	var item catalogItem
	if err := json.Unmarshal(body, &item); err != nil {
		return nil, fmt.Errorf("parse catalogue record: %w", err)
	}
	return catalogFromItem(&item, identifier), nil
}

func (d *downloader) catalogURL(identifier string) string {
	base := d.catalog
	if base == "" {
		base = catalogBase
	}
	return base + url.PathEscape(identifier)
}

func catalogFromItem(item *catalogItem, identifier string) *catalogInfo {
	m := item.Metadata
	info := &catalogInfo{}
	meta := &info.Meta

	meta.Title = m.Title
	for _, t := range m.TitleInfos {
		if t.Type == "" && t.Title != "" {
			meta.Title, meta.Subtitle = t.Title, t.SubTitle
			break
		}
	}

	for _, p := range m.People {
		if p.Name == "" {
			continue
		}
		// "cre" (creator) is used for authors in many records; a primary
		// person without a role is the author too.
		author := p.Usage == "primary" && len(p.Roles) == 0
		for _, r := range p.Roles {
			if r.Name == "aut" || r.Name == "cre" {
				author = true
			}
		}
		if author {
			meta.Authors = append(meta.Authors, displayName(p.Name))
			meta.AuthorsSort = append(meta.AuthorsSort, p.Name)
		} else {
			meta.Contributors = append(meta.Contributors, displayName(p.Name))
		}
	}

	meta.Identifier = identifier
	if m.Identifiers.URN != "" {
		meta.Identifier = m.Identifiers.URN
	}
	meta.ISBNs = append(meta.ISBNs, m.Identifiers.ISBN13...)
	if len(meta.ISBNs) == 0 {
		meta.ISBNs = append(meta.ISBNs, m.Identifiers.ISBN10...)
	}
	for i, isbn := range meta.ISBNs {
		meta.ISBNs[i] = strings.Map(func(r rune) rune {
			if (r >= '0' && r <= '9') || r == 'X' || r == 'x' {
				return r
			}
			return -1
		}, isbn)
	}

	meta.Publisher = m.OriginInfo.Publisher
	meta.Date = yearOf(m.OriginInfo.Issued)
	for _, l := range m.Languages {
		info.Languages = append(info.Languages, l.Code)
	}
	if len(info.Languages) > 0 {
		meta.Language = bcp47(info.Languages[0])
	}
	seen := map[string]bool{}
	for _, t := range m.Subject.Topics {
		key := strings.ToLower(t)
		if t != "" && !seen[key] {
			seen[key] = true
			meta.Subjects = append(meta.Subjects, t)
		}
	}
	meta.Description = m.Summary
	meta.Source = "https://www.nb.no/items/" + meta.Identifier
	return info
}

// displayName turns the catalogue's "Holte, Martin Bech" into "Martin Bech
// Holte".
func displayName(name string) string {
	last, first, ok := strings.Cut(name, ",")
	if !ok {
		return strings.TrimSpace(name)
	}
	return strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
}

func bcp47(code string) string {
	switch code {
	case "nob":
		return "nb"
	case "nno":
		return "nn"
	case "nor":
		return "no"
	case "eng":
		return "en"
	case "dan":
		return "da"
	case "swe":
		return "sv"
	case "deu", "ger":
		return "de"
	case "fra", "fre":
		return "fr"
	case "sme":
		return "se"
	}
	return code
}

// yearOf extracts the first four-digit year from catalogue dates such as
// "[1994]", "c1994" or "1994-1995".
func yearOf(s string) string {
	digits := 0
	for i, r := range s {
		if r >= '0' && r <= '9' {
			digits++
			if digits == 4 {
				return s[i-3 : i+1]
			}
		} else {
			digits = 0
		}
	}
	return ""
}
