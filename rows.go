package aztablessql

import (
	"database/sql/driver"
	"encoding/json"
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

func (r *Rows) Columns() []string {
	if !r.allCols {
		return r.columns
	}
	if r.resolvedCols != nil {
		return r.resolvedCols
	}
	if len(r.entities) == 0 {
		return []string{}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(r.entities[0], &m); err != nil {
		return []string{}
	}
	cols := make([]string, 0, len(m))
	for k := range m {
		if k == "odata.etag" {
			continue
		}
		cols = append(cols, k)
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
	for i, c := range cols {
		dest[i] = m[c]
	}
	r.pos++
	return nil
}
