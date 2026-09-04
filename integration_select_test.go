//go:build integration

package aztablessql

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Comparison WHERE operators (Tier 1, item 1)
// ---------------------------------------------------------------------------

func TestIntegration_PartitionRangeScan(t *testing.T) {
	table := uniqueTable("Range")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Insert entities across three partitions: "pa", "pb", "pc".
	for _, pk := range []string{"pa", "pb", "pc"} {
		insertAgeRows(t, db, table, pk, []int{10, 20, 30})
	}

	// Range scan: PartitionKey >= 'pa' AND PartitionKey < 'pb' should
	// return only the "pa" partition (3 rows).
	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey >= 'pa' AND PartitionKey < 'pb'`,
	)
	if err != nil {
		t.Fatalf("SELECT range scan: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 rows in [pa, pb), got %d", count)
	}
}

func TestIntegration_NumericGreaterThan(t *testing.T) {
	table := uniqueTable("Gt")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	insertAgeRows(t, db, table, "pk", []int{10, 25, 30, 45, 60})

	// Age > 30 should return 2 rows (45, 60).
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = 'pk' AND Age > ?`,
		30,
	)
	if err != nil {
		t.Fatalf("SELECT Age > 30: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows with Age > 30, got %d", count)
	}
}

func TestIntegration_NumericGreaterOrEqual(t *testing.T) {
	table := uniqueTable("Ge")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	insertAgeRows(t, db, table, "pk", []int{10, 25, 30, 45, 60})

	// Age >= 30 should return 3 rows (30, 45, 60).
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = 'pk' AND Age >= ?`,
		30,
	)
	if err != nil {
		t.Fatalf("SELECT Age >= 30: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 rows with Age >= 30, got %d", count)
	}
}

func TestIntegration_NumericLessThan(t *testing.T) {
	table := uniqueTable("Lt")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	insertAgeRows(t, db, table, "pk", []int{10, 25, 30, 45, 60})

	// Age < 30 should return 2 rows (10, 25).
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = 'pk' AND Age < ?`,
		30,
	)
	if err != nil {
		t.Fatalf("SELECT Age < 30: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows with Age < 30, got %d", count)
	}
}

func TestIntegration_NumericLessOrEqual(t *testing.T) {
	table := uniqueTable("Le")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	insertAgeRows(t, db, table, "pk", []int{10, 25, 30, 45, 60})

	// Age <= 30 should return 3 rows (10, 25, 30).
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = 'pk' AND Age <= ?`,
		30,
	)
	if err != nil {
		t.Fatalf("SELECT Age <= 30: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 rows with Age <= 30, got %d", count)
	}
}

func TestIntegration_StringNotEqual(t *testing.T) {
	table := uniqueTable("Ne")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Insert entities with distinct Names in one partition.
	for i, name := range []string{"Alice", "Bob", "Carol"} {
		rk := fmt.Sprintf("rk%02d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, name,
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	// Name != 'Bob' should return 2 rows (Alice, Carol).
	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk' AND Name != 'Bob'`,
	)
	if err != nil {
		t.Fatalf("SELECT Name != 'Bob': %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows with Name != 'Bob', got %d", count)
	}
}

func TestIntegration_DiamondOperatorAliasForNotEqual(t *testing.T) {
	table := uniqueTable("Diamond")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	for i, name := range []string{"Alice", "Bob", "Carol"} {
		rk := fmt.Sprintf("rk%02d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, name,
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}

	// "<>" is accepted as an alias for "!=".
	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk' AND Name <> 'Bob'`,
	)
	if err != nil {
		t.Fatalf("SELECT Name <> 'Bob': %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows with Name <> 'Bob', got %d", count)
	}
}

func TestIntegration_PointReadStillUsedWhenBothKeysAreEq(t *testing.T) {
	table := uniqueTable("Point")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// A point read with both keys using "=" must still work and return 1 row.
	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk' AND RowKey = 'rk'`,
	)
	if err != nil {
		t.Fatalf("SELECT point read: %v", err)
	}
	m := scanOne(t, rows)
	if fmt.Sprintf("%v", m["Name"]) != "Ada" {
		t.Errorf("Name = %v, want 'Ada'", m["Name"])
	}
}

func TestIntegration_RowKeyRangeScanUsesListPath(t *testing.T) {
	table := uniqueTable("RkRange")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Insert entities with RowKeys "a", "b", "c" in one partition.
	for _, rk := range []string{"a", "b", "c"} {
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
			"pk", rk, "name-"+rk,
		)
		if err != nil {
			t.Fatalf("INSERT seed %s: %v", rk, err)
		}
	}

	// PartitionKey = 'pk' AND RowKey >= 'b' is a range scan (not a point
	// read) and must go through ListEntities, returning 2 rows (b, c).
	rows, err := db.Query(
		`SELECT * FROM ` + table + ` WHERE PartitionKey = 'pk' AND RowKey >= 'b'`,
	)
	if err != nil {
		t.Fatalf("SELECT RowKey range: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows with RowKey >= 'b', got %d", count)
	}
}
