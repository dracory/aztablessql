//go:build integration

package aztablessql

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// LIMIT / TOP (Tier 2, item 4) + SELECT projection pushdown
// ---------------------------------------------------------------------------

// TestIntegration_LimitCapsResultSet verifies that SELECT ... LIMIT <n>
// returns at most <n> rows when more rows exist in the table.
func TestIntegration_LimitCapsResultSet(t *testing.T) {
	table := uniqueTable("Limit")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Insert 100 rows in one partition.
	for i := 0; i < 100; i++ {
		rk := fmt.Sprintf("rk%03d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, fmt.Sprintf("name%03d", i),
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk' LIMIT 10`,
	)
	if err != nil {
		t.Fatalf("SELECT with LIMIT: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 10 {
		t.Errorf("expected 10 rows with LIMIT 10, got %d", count)
	}
}

// TestIntegration_LimitLargerThanResultSet verifies that a LIMIT larger than
// the available rows returns all rows without error.
func TestIntegration_LimitLargerThanResultSet(t *testing.T) {
	table := uniqueTable("LimitBig")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	for i := 0; i < 5; i++ {
		rk := fmt.Sprintf("rk%03d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, fmt.Sprintf("name%03d", i),
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk' LIMIT 1000`,
	)
	if err != nil {
		t.Fatalf("SELECT with LIMIT: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 5 rows (LIMIT larger than result set), got %d", count)
	}
}

// TestIntegration_LimitWithExplicitColumns verifies LIMIT works with an
// explicit column list (not SELECT *).
func TestIntegration_LimitWithExplicitColumns(t *testing.T) {
	table := uniqueTable("LimitCols")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	for i := 0; i < 20; i++ {
		rk := fmt.Sprintf("rk%03d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, fmt.Sprintf("name%03d", i),
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	rows, err := db.Query(
		`SELECT Name FROM ` + table + ` WHERE PartitionKey = 'pk' LIMIT 3`,
	)
	if err != nil {
		t.Fatalf("SELECT with LIMIT: %v", err)
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
	if count != 3 {
		t.Errorf("expected 3 rows with LIMIT 3, got %d", count)
	}
}

// TestIntegration_LimitOne verifies LIMIT 1 returns exactly one row.
func TestIntegration_LimitOne(t *testing.T) {
	table := uniqueTable("LimitOne")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	for i := 0; i < 10; i++ {
		rk := fmt.Sprintf("rk%03d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, fmt.Sprintf("name%03d", i),
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk' LIMIT 1`,
	)
	if err != nil {
		t.Fatalf("SELECT with LIMIT 1: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row with LIMIT 1, got %d", count)
	}
}

// TestIntegration_LimitOnPointRead verifies that LIMIT on a point read is
// harmless (a point read returns 0 or 1 row; LIMIT is redundant but allowed).
func TestIntegration_LimitOnPointRead(t *testing.T) {
	table := uniqueTable("LimitPoint")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk' AND RowKey = 'rk' LIMIT 10`,
	)
	if err != nil {
		t.Fatalf("SELECT point read with LIMIT: %v", err)
	}
	m := scanOne(t, rows)
	if fmt.Sprintf("%v", m["Name"]) != "Ada" {
		t.Errorf("Name = %v, want 'Ada'", m["Name"])
	}
}

// TestIntegration_LimitOnPointReadMissingRow verifies that LIMIT on a point
// read that returns no rows is harmless — the result is empty, no error.
func TestIntegration_LimitOnPointReadMissingRow(t *testing.T) {
	table := uniqueTable("LimitPointMiss")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Point read for a non-existent row with LIMIT — should return 0 rows.
	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk' AND RowKey = 'missing' LIMIT 10`,
	)
	if err != nil {
		t.Fatalf("SELECT missing point read with LIMIT: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows for missing point read with LIMIT, got %d", count)
	}
}

// TestIntegration_SelectProjectionPushdown verifies that SELECT with an
// explicit real-property column list returns the requested values correctly
// when the server-side $select pushdown is active. The server always
// returns PartitionKey and RowKey in addition to the selected properties.
func TestIntegration_SelectProjectionPushdown(t *testing.T) {
	table := uniqueTable("Proj")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age, Score, Active) VALUES (?, ?, ?, ?, ?, ?)`,
		"pk", "rk", "Ada", 36, 3.14, true,
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query(
		`SELECT Name FROM ` + table + ` WHERE PartitionKey = 'pk'`,
	)
	if err != nil {
		t.Fatalf("SELECT Name: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) != 1 || cols[0] != "Name" {
		t.Fatalf("Columns() = %v, want [Name]", cols)
	}

	if !rows.Next() {
		t.Fatal("expected 1 row, got 0")
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if name != "Ada" {
		t.Errorf("Name = %q, want 'Ada'", name)
	}
	if rows.Next() {
		t.Error("expected 1 row, got >1")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
}

// TestIntegration_SelectProjectionPushdownMultipleCols verifies that
// selecting multiple real properties returns all of them correctly with the
// server-side $select pushdown active.
func TestIntegration_SelectProjectionPushdownMultipleCols(t *testing.T) {
	table := uniqueTable("ProjMulti")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age, Score, Active, Note) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"pk", "rk", "Ada", 36, 3.14, true, "hello",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query(
		`SELECT Name, Age, Score FROM ` + table + ` WHERE PartitionKey = 'pk'`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) != 3 {
		t.Fatalf("Columns() = %v, want 3 columns", cols)
	}

	if !rows.Next() {
		t.Fatal("expected 1 row, got 0")
	}
	var name string
	var age int
	var score float64
	if err := rows.Scan(&name, &age, &score); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if name != "Ada" {
		t.Errorf("Name = %q, want 'Ada'", name)
	}
	if age != 36 {
		t.Errorf("Age = %d, want 36", age)
	}
	if score != 3.14 {
		t.Errorf("Score = %g, want 3.14", score)
	}
}

// TestIntegration_SelectProjectionWithETagFallsBackToClientSide verifies
// that a SELECT list containing ETag (a pseudo-column) does not break: the
// driver falls back to a full fetch and projects client-side, surfacing the
// ETag from the entity's odata.etag meta field.
func TestIntegration_SelectProjectionWithETagFallsBackToClientSide(t *testing.T) {
	table := uniqueTable("ProjEtag")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
		"pk", "rk", "Ada", 36,
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query(
		`SELECT ETag, Name FROM ` + table + ` WHERE PartitionKey = 'pk'`,
	)
	if err != nil {
		t.Fatalf("SELECT ETag, Name: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) != 2 {
		t.Fatalf("Columns() = %v, want 2 columns [ETag Name]", cols)
	}
	if cols[0] != "ETag" || cols[1] != "Name" {
		t.Errorf("columns = %v, want [ETag Name]", cols)
	}

	if !rows.Next() {
		t.Fatal("expected 1 row, got 0")
	}
	var etag, name string
	if err := rows.Scan(&etag, &name); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if etag == "" || etag == "<nil>" {
		t.Errorf("ETag = %q, want a non-empty value", etag)
	}
	if name != "Ada" {
		t.Errorf("Name = %q, want 'Ada'", name)
	}
}

// TestIntegration_SelectProjectionWithTimestampFallsBackToClientSide
// verifies that a SELECT list containing Timestamp (a pseudo-column) falls
// back to a full fetch and surfaces the server-managed Timestamp field.
func TestIntegration_SelectProjectionWithTimestampFallsBackToClientSide(t *testing.T) {
	table := uniqueTable("ProjTs")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query(
		`SELECT Timestamp, Name FROM ` + table + ` WHERE PartitionKey = 'pk'`,
	)
	if err != nil {
		t.Fatalf("SELECT Timestamp, Name: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) != 2 {
		t.Fatalf("Columns() = %v, want 2 columns [Timestamp Name]", cols)
	}
	if cols[0] != "Timestamp" || cols[1] != "Name" {
		t.Errorf("columns = %v, want [Timestamp Name]", cols)
	}

	if !rows.Next() {
		t.Fatal("expected 1 row, got 0")
	}
	var ts, name string
	if err := rows.Scan(&ts, &name); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ts == "" || ts == "<nil>" {
		t.Errorf("Timestamp = %q, want a non-empty value", ts)
	}
	if name != "Ada" {
		t.Errorf("Name = %q, want 'Ada'", name)
	}
}

// TestIntegration_SelectProjectionWithLimit verifies that $select pushdown
// and LIMIT ($top) compose correctly on the list path.
func TestIntegration_SelectProjectionWithLimit(t *testing.T) {
	table := uniqueTable("ProjLim")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	for i := 0; i < 20; i++ {
		rk := fmt.Sprintf("rk%03d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
			"pk", rk, fmt.Sprintf("name%03d", i), i,
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	rows, err := db.Query(
		`SELECT Name FROM ` + table + ` WHERE PartitionKey = 'pk' LIMIT 5`,
	)
	if err != nil {
		t.Fatalf("SELECT Name with LIMIT: %v", err)
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
	if count != 5 {
		t.Errorf("expected 5 rows with LIMIT 5, got %d", count)
	}
}
