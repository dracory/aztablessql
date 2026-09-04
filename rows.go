package aztablessql

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type Rows struct {
	columns      []string // explicit selected columns (nil if allCols)
	allCols      bool
	entities     [][]byte
	pos          int
	resolvedCols []string
}

// Columns returns the column names for the result set.
//
// For explicit column lists (SELECT a, b) the original list is returned.
// For SELECT * the column set is merged across ALL entities, because Azure
// Table Storage allows heterogeneous entities within the same table —
// different rows may have different properties.
func (r *Rows) Columns() []string {
	if !r.allCols {
		return r.columns
	}
	if r.resolvedCols != nil {
		return r.resolvedCols
	}
	seen := map[string]bool{}
	var cols []string
	for _, entity := range r.entities {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(entity, &m); err != nil {
			continue
		}
		for k := range m {
			if k == "odata.etag" || seen[k] {
				continue
			}
			seen[k] = true
			cols = append(cols, k)
		}
	}
	sort.Strings(cols)
	r.resolvedCols = cols
	return cols
}

func (r *Rows) Close() error { return nil }

func (r *Rows) Next(dest []driver.Value) error {
	if r.pos >= len(r.entities) {
		return io.EOF
	}
	var m map[string]interface{}
	if err := json.Unmarshal(r.entities[r.pos], &m); err != nil {
		return err
	}
	cols := r.Columns()
	if len(dest) < len(cols) {
		return fmt.Errorf("aztablessql: destination slice too short: got %d, want %d", len(dest), len(cols))
	}
	for i, c := range cols {
		dest[i] = m[c]
	}
	r.pos++
	return nil
}
