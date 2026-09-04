//go:build integration

package aztablessql

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// COUNT(*) (Tier 3, item 10)
// ---------------------------------------------------------------------------

// countResult runs SELECT COUNT(*) and returns the count as an int. The
// decoded value follows encoding/json's default typing (float64 for
// numbers), so we go through interface{} to be tolerant.
func countResult(t *testing.T, db *sql.DB, query string, args ...interface{}) int {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("SELECT COUNT(*): %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) != 1 || cols[0] != "count" {
		t.Fatalf("columns = %v, want [count]", cols)
	}
	if !rows.Next() {
		t.Fatal("expected exactly one row from COUNT(*), got none")
	}
	var v interface{}
	if err := rows.Scan(&v); err != nil {
		t.Fatalf("Scan count: %v", err)
	}
	if rows.Next() {
		t.Fatal("expected exactly one row from COUNT(*), got more")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	default:
		t.Fatalf("count value has unexpected type %T (%v)", v, v)
		return 0
	}
}

// TestIntegration_CountAll verifies that SELECT COUNT(*) returns the total
// number of entities in the table (no WHERE filter).
func TestIntegration_CountAll(t *testing.T) {
	table := uniqueTable("Count")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Seed 5 entities across two partitions.
	insertAgeRows(t, db, table, "p1", []int{10, 20, 30})
	insertAgeRows(t, db, table, "p2", []int{40, 50})

	got := countResult(t, db, `SELECT COUNT(*) FROM `+table)
	if got != 5 {
		t.Errorf("COUNT(*) = %d, want 5", got)
	}
}

// TestIntegration_CountWithWherePartition verifies COUNT(*) with a WHERE
// filter on PartitionKey returns the count of matching entities only.
func TestIntegration_CountWithWherePartition(t *testing.T) {
	table := uniqueTable("CountP")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	insertAgeRows(t, db, table, "p1", []int{10, 20, 30})
	insertAgeRows(t, db, table, "p2", []int{40, 50})

	got := countResult(t, db, `SELECT COUNT(*) FROM `+table+` WHERE PartitionKey = ?`, "p1")
	if got != 3 {
		t.Errorf("COUNT(*) p1 = %d, want 3", got)
	}
}

// TestIntegration_CountWithRangeFilter verifies COUNT(*) works with a
// non-point WHERE filter (range scan), which goes through ListEntities.
func TestIntegration_CountWithRangeFilter(t *testing.T) {
	table := uniqueTable("CountR")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	insertAgeRows(t, db, table, "pa", []int{10, 20, 30})
	insertAgeRows(t, db, table, "pb", []int{40, 50})
	insertAgeRows(t, db, table, "pc", []int{60})

	// Range [pa, pb) covers only "pa" → 3 entities.
	got := countResult(t, db,
		`SELECT COUNT(*) FROM `+table+` WHERE PartitionKey >= 'pa' AND PartitionKey < 'pb'`)
	if got != 3 {
		t.Errorf("COUNT(*) [pa,pb) = %d, want 3", got)
	}
}

// TestIntegration_CountEmpty verifies COUNT(*) on a table with no matching
// entities returns 0 (not an error, not an empty result set).
func TestIntegration_CountEmpty(t *testing.T) {
	table := uniqueTable("CountE")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// No entities inserted.
	got := countResult(t, db, `SELECT COUNT(*) FROM `+table)
	if got != 0 {
		t.Errorf("COUNT(*) on empty table = %d, want 0", got)
	}
}

// TestIntegration_CountEmptyWithWhere verifies COUNT(*) with a WHERE filter
// that matches nothing also returns 0.
func TestIntegration_CountEmptyWithWhere(t *testing.T) {
	table := uniqueTable("CountEW")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	insertAgeRows(t, db, table, "p1", []int{10, 20, 30})

	got := countResult(t, db, `SELECT COUNT(*) FROM `+table+` WHERE PartitionKey = ?`, "nonexistent")
	if got != 0 {
		t.Errorf("COUNT(*) on no-match = %d, want 0", got)
	}
}

// TestIntegration_CountCrossesPageBoundary inserts more than 1000 entities
// (the default Table Storage page size) and verifies COUNT(*) counts them
// all — exercising the "iterate pager to completion" path.
func TestIntegration_CountCrossesPageBoundary(t *testing.T) {
	table := uniqueTable("CountPg")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	const n = 1100
	for i := 0; i < n; i++ {
		rk := fmt.Sprintf("rk%04d", i)
		if _, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Idx) VALUES (?, ?, ?)`,
			"pk", rk, int64(i),
		); err != nil {
			t.Fatalf("seed INSERT %d: %v", i, err)
		}
	}

	got := countResult(t, db, `SELECT COUNT(*) FROM `+table+` WHERE PartitionKey = ?`, "pk")
	if got != n {
		t.Errorf("COUNT(*) = %d, want %d (page boundary not crossed?)", got, n)
	}
}

// TestIntegration_CountRejectsCountCol verifies that COUNT(col) is rejected
// at the driver level (parser rejects before any network call). Uses db.Query
// since COUNT(...) is a query-shaped statement; the parse error is returned
// before any rows are produced, so there is nothing to close.
func TestIntegration_CountRejectsCountCol(t *testing.T) {
	table := uniqueTable("CountCol")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	rows, err := db.Query(`SELECT COUNT(Name) FROM ` + table)
	if err == nil {
		if rows != nil {
			rows.Close()
		}
		t.Fatal("expected error for COUNT(col), got nil")
	}
	if !strings.Contains(err.Error(), "only COUNT(*) is supported as the sole SELECT expression") {
		t.Errorf("error = %v, want it to contain 'only COUNT(*) is supported as the sole SELECT expression'", err)
	}
}

// TestIntegration_CountRejectsLimit verifies that COUNT(*) with a trailing
// LIMIT is rejected.
func TestIntegration_CountRejectsLimit(t *testing.T) {
	table := uniqueTable("CountLim")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Query(`SELECT COUNT(*) FROM ` + table + ` LIMIT 10`)
	if err == nil {
		t.Fatal("expected error for COUNT(*) LIMIT, got nil")
	}
	if !strings.Contains(err.Error(), "COUNT(*) does not support LIMIT") {
		t.Errorf("error = %v, want it to contain 'COUNT(*) does not support LIMIT'", err)
	}
}
