package models

import (
	_ "embed"
	"encoding/json"
)

// Entry is one curated model.
type Entry struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Spec    string  `json:"spec"`
	SizeGB  float64 `json:"sizeGB"`
	RAMGB   float64 `json:"ramGB"`
	Label   string  `json:"label"`
	Licence string  `json:"licence"`
	Note    string  `json:"note"`
	Default bool    `json:"default,omitempty"`
}

//go:embed catalog.json
var catalogJSON []byte

// Catalog lists the curated models.
func Catalog() []Entry {
	var out []Entry
	if err := json.Unmarshal(catalogJSON, &out); err != nil {
		panic(err) // embedded at build time and covered by a test
	}
	return out
}

// Lookup finds a catalogue entry by id.
func Lookup(id string) (Entry, bool) {
	for _, e := range Catalog() {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}
