package aztablessql

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

type Rows struct {
	columns []string // explicit selected columns (nil if allCols)
	allCols bool

	// Lazy paging state. Only the list path uses pager/page/pagePos.
	// Point reads populate entities directly and leave pager nil.
	pager   *runtime.Pager[aztables.ListEntitiesResponse]
	page    [][]byte // current page buffer
	pagePos int      // index into page

	// Point-read path (single entity). Kept for backward compat.
	entities     [][]byte
	pos          int
	resolvedCols []string

	ctx    context.Context // captured for NextPage calls
	err    error           // sticky error surfaced via Next/Err
	closed bool
}

// Columns returns the column names for the result set.
//
// For explicit column lists (SELECT a, b) the original list is returned.
// For SELECT * the column set is merged across the available entities:
//   - Point-read path: the single entity.
//   - List path: the first page only (see the lazy-paging trade-off below).
//
// Azure Table Storage allows heterogeneous entities within the same table —
// different rows may have different properties. With lazy paging, Columns()
// resolves from the first page only so it can be answered before Next() is
// called (the database/sql contract requires this). If a later page
// introduces a new property, it will not appear in Columns() but will still
// be present in the decoded row map returned by Next(). Callers scanning
// into []interface{} by column count would miss late-appearing properties;
// callers scanning into a map or by name are unaffected.
func (r *Rows) Columns() []string {
	if !r.allCols {
		return r.columns
	}
	if r.resolvedCols != nil {
		return r.resolvedCols
	}
	seen := map[string]bool{}
	var cols []string
	// Scan both the point-read entities and the current page buffer.
	for _, entity := range r.entities {
		r.collectCols(entity, seen, &cols)
	}
	for _, entity := range r.page {
		r.collectCols(entity, seen, &cols)
	}
	sort.Strings(cols)
	// Cache the result so Columns() is stable across calls. But don't
	// cache an empty column set while the pager may still fetch more
	// pages — an empty first page (rare) would otherwise permanently lock
	// in zero columns and hide all values from callers. Once the pager is
	// done (nil or no more pages), an empty result is final and safe to
	// cache.
	if len(cols) > 0 || r.pager == nil || !r.pager.More() {
		r.resolvedCols = cols
	}
	return cols
}

func (r *Rows) collectCols(entity []byte, seen map[string]bool, cols *[]string) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(entity, &m); err != nil {
		return
	}
	for k := range m {
		if k == "odata.etag" || seen[k] {
			continue
		}
		seen[k] = true
		*cols = append(*cols, k)
	}
}

// Close releases resources. It is safe to call multiple times.
func (r *Rows) Close() error {
	r.closed = true
	return nil
}

// Next populates dest with the next row. Returns io.EOF when there are no
// more rows. For the list path, pages are fetched lazily as needed.
func (r *Rows) Next(dest []driver.Value) error {
	if r.closed {
		return io.EOF
	}

	// Lazy paging path.
	if r.pager != nil {
		// Short-circuit on a prior sticky error so we don't retry the
		// failed NextPage call.
		if r.err != nil {
			return r.err
		}
		for r.pagePos >= len(r.page) {
			// Current page exhausted; try to fetch the next one.
			if !r.pager.More() {
				if r.err != nil {
					return r.err
				}
				return io.EOF
			}
			resp, err := r.pager.NextPage(r.ctx)
			if err != nil {
				r.err = err
				return err
			}
			r.page = resp.Entities
			r.pagePos = 0
			// If the page is empty (rare), loop again to fetch the next.
		}
		entity := r.page[r.pagePos]
		r.pagePos++
		return r.decodeEntity(entity, dest)
	}

	// Point-read path.
	if r.pos >= len(r.entities) {
		if r.err != nil {
			return r.err
		}
		return io.EOF
	}
	entity := r.entities[r.pos]
	r.pos++
	return r.decodeEntity(entity, dest)
}

func (r *Rows) decodeEntity(entity []byte, dest []driver.Value) error {
	var m map[string]interface{}
	if err := json.Unmarshal(entity, &m); err != nil {
		return err
	}
	cols := r.Columns()
	if len(dest) < len(cols) {
		return fmt.Errorf("aztablessql: destination slice too short: got %d, want %d", len(dest), len(cols))
	}
	for i, c := range cols {
		dest[i] = m[c]
	}
	return nil
}
