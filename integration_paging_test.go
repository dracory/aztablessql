//go:build integration

package aztablessql

import (
	"context"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Lazy paging (Tier 1, item 2)
// ---------------------------------------------------------------------------

// TestIntegration_LazyPaging inserts ~1200 entities (more than the default
// page size of 1000) into one partition, selects with SELECT *, and verifies
// that all rows are returned via lazy page fetching. This exercises the
// multi-page code path in Rows.Next.
func TestIntegration_LazyPaging(t *testing.T) {
	table := uniqueTable("Lazy")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	const total = 1200
	for i := 0; i < total; i++ {
		rk := fmt.Sprintf("rk%04d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, fmt.Sprintf("name%04d", i),
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk'`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	// Columns() must be callable before Next() (database/sql contract).
	cols, _ := rows.Columns()
	if len(cols) == 0 {
		t.Fatal("Columns() returned empty before any Next()")
	}
	t.Logf("columns from first page: %v", cols)

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err after iteration: %v", err)
	}
	if count != total {
		t.Errorf("expected %d rows, got %d (lazy paging may have dropped pages)", total, count)
	}
}

// TestIntegration_LazyPagingExplicitColumns verifies lazy paging works with
// an explicit column list (not SELECT *), which uses a different Columns()
// path.
func TestIntegration_LazyPagingExplicitColumns(t *testing.T) {
	table := uniqueTable("LazyCols")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	const total = 1100
	for i := 0; i < total; i++ {
		rk := fmt.Sprintf("rk%04d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, fmt.Sprintf("name%04d", i),
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	rows, err := db.Query(
		`SELECT Name FROM ` + table + ` WHERE PartitionKey = 'pk'`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) != 1 || cols[0] != "Name" {
		t.Fatalf("Columns() = %v, want [Name]", cols)
	}

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != total {
		t.Errorf("expected %d rows, got %d", total, count)
	}
}

// TestIntegration_LazyPagingEmptyResult verifies that a list-path query
// that returns zero rows still works — the first page is empty and
// pager.More() is false.
func TestIntegration_LazyPagingEmptyResult(t *testing.T) {
	table := uniqueTable("LazyEmpty")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Insert one entity in a different partition so the table exists but
	// our query returns nothing.
	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"other", "rk", "x",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'nonexistent'`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	t.Logf("columns on empty result: %v", cols)

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows, got %d", count)
	}
}

// TestIntegration_LazyPagingErrorMidway cancels the context mid-iteration
// and verifies that rows.Err() surfaces the error rather than silently
// truncating results.
func TestIntegration_LazyPagingErrorMidway(t *testing.T) {
	table := uniqueTable("LazyErr")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	const total = 1200
	for i := 0; i < total; i++ {
		rk := fmt.Sprintf("rk%04d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, fmt.Sprintf("name%04d", i),
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	// Use a cancellable context and cancel it after reading at least one
	// row. The first page (1000 entities) is fetched eagerly in execSelect,
	// so we need to iterate past 1000 rows to trigger a NextPage call that
	// will observe the cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rows, err := db.QueryContext(
		ctx,
		`SELECT * FROM `+table+` WHERE PartitionKey = 'pk'`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
		// Cancel after the 1000th row (the last of the eagerly-fetched
		// first page) so the *next* Next() call triggers pager.NextPage
		// on a canceled context.
		if count == 1000 {
			cancel()
		}
	}
	// We expect either:
	//   - rows.Err() returns a context-canceled error, OR
	//   - count == total (Azurite may complete the in-flight request before
	//     observing cancellation — same leniency as TestIntegration_ContextCancellation).
	if err := rows.Err(); err != nil {
		t.Logf("rows.Err after cancel at count=%d: %v", count, err)
	} else if count == total {
		t.Log("context cancellation did not surface an error (Azurite may not honor cancellation) — not failing")
	} else {
		t.Errorf("expected either an error or all %d rows, got count=%d with no error", total, count)
	}
}

// TestIntegration_LazyPagingCloseIdempotent verifies that Close() can be
// called multiple times and after Close, Next returns io.EOF.
func TestIntegration_LazyPagingCloseIdempotent(t *testing.T) {
	table := uniqueTable("LazyClose")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	for i := 0; i < 5; i++ {
		rk := fmt.Sprintf("rk%02d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, fmt.Sprintf("name%02d", i),
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk'`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}

	if err := rows.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	// Next after Close should return false (no more rows), not panic.
	if rows.Next() {
		t.Error("Next after Close returned true, expected false")
	}
}
