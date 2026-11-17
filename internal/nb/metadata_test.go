package nb

import "testing"

func TestCatalogFromItem(t *testing.T) {
	var item catalogItem
	item.Metadata.Title = "Kristin Lavransdatter : Kransen"
	item.Metadata.People = append(item.Metadata.People,
		struct {
			Name  string `json:"name"`
			Usage string `json:"usage"`
			Roles []struct {
				Name string `json:"name"`
			} `json:"roles"`
		}{Name: "Undset, Sigrid", Usage: "primary", Roles: []struct {
			Name string `json:"name"`
		}{{Name: "cre"}}},
		struct {
			Name  string `json:"name"`
			Usage string `json:"usage"`
			Roles []struct {
				Name string `json:"name"`
			} `json:"roles"`
		}{Name: "Grimnes, Finn", Roles: []struct {
			Name string `json:"name"`
		}{{Name: "edt"}}})
	item.Metadata.Identifiers.ISBN13 = []string{"978-82-489-3825-5"}
	item.Metadata.OriginInfo.Issued = "[1960]"
	item.Metadata.Languages = append(item.Metadata.Languages, struct {
		Code string `json:"code"`
	}{"nob"})
	info := catalogFromItem(&item, "URN:NBN:no-nb_digibok_1")
	m := info.Meta
	if len(m.Authors) != 1 || m.Authors[0] != "Sigrid Undset" || m.AuthorsSort[0] != "Undset, Sigrid" {
		t.Errorf("authors = %v / %v", m.Authors, m.AuthorsSort)
	}
	if len(m.Contributors) != 1 || m.Contributors[0] != "Finn Grimnes" {
		t.Errorf("contributors = %v", m.Contributors)
	}
	if m.ISBNs[0] != "9788248938255" || m.Date != "1960" || m.Language != "nb" {
		t.Errorf("isbn %v date %q language %q", m.ISBNs, m.Date, m.Language)
	}
}
