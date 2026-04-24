package t4doc

import (
	"encoding/json"
	"fmt"
)

type catalog struct {
	FormatVersion int         `json:"format_version"`
	Indexes       []IndexSpec `json:"indexes,omitempty"`
}

func emptyCatalog() catalog {
	return catalog{FormatVersion: 1}
}

func decodeCatalog(data []byte) (catalog, error) {
	if len(data) == 0 {
		return emptyCatalog(), nil
	}
	var c catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return catalog{}, err
	}
	if c.FormatVersion == 0 {
		c.FormatVersion = 1
	}
	if c.FormatVersion != 1 {
		return catalog{}, fmt.Errorf("t4doc: unsupported catalog version %d", c.FormatVersion)
	}
	return c, nil
}

func (c catalog) encode() ([]byte, error) {
	if c.FormatVersion == 0 {
		c.FormatVersion = 1
	}
	return json.Marshal(c)
}

func (c catalog) findReadyIndexForFilter(f Filter) (IndexSpec, string, bool) {
	eqs := f.eqFilters(nil)
	for _, idx := range c.Indexes {
		if idx.State != IndexReady {
			continue
		}
		for _, eq := range eqs {
			encoded, ok := filterValueForIndex(idx, eq)
			if ok {
				return idx, encoded, true
			}
		}
	}
	return IndexSpec{}, "", false
}

func (c catalog) indexByName(name string) (IndexSpec, int, bool) {
	for i, idx := range c.Indexes {
		if idx.Name == name {
			return idx, i, true
		}
	}
	return IndexSpec{}, -1, false
}

func normalizeIndexSpec(spec IndexSpec, state IndexState) (IndexSpec, error) {
	if err := validateName(spec.Name); err != nil {
		return IndexSpec{}, err
	}
	if err := validateName(spec.Field); err != nil {
		return IndexSpec{}, err
	}
	switch spec.Type {
	case String, Int64, Bool:
	default:
		return IndexSpec{}, fmt.Errorf("t4doc: unsupported index type %q", spec.Type)
	}
	spec.State = state
	return spec, nil
}
