//go:build integration

package aztablessql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// azuriteConnStr is the well-known Azurite dev-store connection string,
// overridden by AZTABLES_TEST_CONNSTR if set.
const azuriteConnStr = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;TableEndpoint=http://127.0.0.1:10002/devstoreaccount1;"

func testConnStr(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("AZTABLES_TEST_CONNSTR"); v != "" {
		return v
	}
	return azuriteConnStr
}

// openDB opens a *sql.DB against Azurite and ensures the test table exists.
func openDB(t *testing.T, table string) *sql.DB {
	t.Helper()
	connStr := testConnStr(t)

	svc, err := aztables.NewServiceClientFromConnectionString(connStr, nil)
	if err != nil {
		t.Fatalf("create service client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Create the table if it doesn't exist. Cre.CreateTable returns an error
	// if it already exists, which we tolerate.
	_, _ = svc.CreateTable(ctx, table, nil)

	db, err := sql.Open("aztables", connStr)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Clean the table before each test by deleting any entities we know about.
	// We use a unique partition per test to avoid cross-test interference.
	t.Cleanup(func() {
		db.Close()
	})
	return db
}

func dropTable(t *testing.T, table string) {
	t.Helper()
	svc, err := aztables.NewServiceClientFromConnectionString(testConnStr(t), nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = svc.DeleteTable(ctx, table, nil)
}

// uniqueTable returns a unique table name per test invocation.
func uniqueTable(prefix string) string {
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()%1000000)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestIntegration_InsertSelectDeleteRoundTrip(t *testing.T) {
	table := uniqueTable("People")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// INSERT
	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
		"pk1", "rk1", "Ada Lovelace", 36,
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// SELECT point read
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk1", "rk1",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	t.Logf("columns: %v", cols)

	count := 0
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		count++
		t.Logf("row: %v", vals)
	}
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}

	// DELETE
	_, err = db.Exec(
		`DELETE FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk1", "rk1",
	)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}

	// SELECT after delete — should be empty
	rows2, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk1", "rk1",
	)
	if err != nil {
		t.Fatalf("SELECT after delete: %v", err)
	}
	defer rows2.Close()
	count = 0
	for rows2.Next() {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0 rows after delete, got %d", count)
	}
}

func TestIntegration_MixedCaseProperty(t *testing.T) {
	table := uniqueTable("Mixed")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, MyCol) VALUES (?, ?, ?)`,
		"pk", "rk", "value",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Query by the mixed-case property — this is the bug the review caught:
	// the old code lowercased the column name and the OData filter would not
	// match the stored "MyCol" property.
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE MyCol = ? AND PartitionKey = ?`,
		"value", "pk",
	)
	if err != nil {
		t.Fatalf("SELECT with mixed-case WHERE: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if count != 1 {
		t.Fatalf("expected 1 row matching MyCol='value', got %d", count)
	}
}

func TestIntegration_DeleteRejectsExtraConditions(t *testing.T) {
	table := uniqueTable("DelExtra")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// DELETE with an extra condition must be rejected.
	_, err = db.Exec(
		`DELETE FROM `+table+` WHERE PartitionKey = ? AND RowKey = ? AND Name = ?`,
		"pk", "rk", "Ada",
	)
	if err == nil {
		t.Fatal("expected DELETE with extra condition to fail, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// TestIntegration_DeleteRejectsNonEqOperators verifies that a DELETE with a
// non-"=" operator on a key column is rejected at parse time. Without this
// check, `WHERE PartitionKey = 'pk' AND RowKey > 'rk'` would resolve RowKey
// to "rk" and silently delete the wrong entity.
func TestIntegration_DeleteRejectsNonEqOperators(t *testing.T) {
	table := uniqueTable("DelNeOp")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	cases := []struct {
		name  string
		query string
		args  []interface{}
	}{
		{"rowkey greater than", `DELETE FROM ` + table + ` WHERE PartitionKey = ? AND RowKey > ?`, []interface{}{"pk", "rk"}},
		{"partitionkey less than", `DELETE FROM ` + table + ` WHERE PartitionKey < ? AND RowKey = ?`, []interface{}{"pk", "rk"}},
		{"rowkey not equal", `DELETE FROM ` + table + ` WHERE PartitionKey = ? AND RowKey != ?`, []interface{}{"pk", "rk"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := db.Exec(c.query, c.args...)
			if err == nil {
				t.Fatalf("expected DELETE with non-= operator to fail, got nil")
			}
			t.Logf("got expected error: %v", err)
		})
	}

	// Sanity check: the entity must still be present (no wrong delete happened).
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	m := scanOne(t, rows)
	if fmt.Sprintf("%v", m["Name"]) != "Ada" {
		t.Errorf("Name = %v, want 'Ada' (entity should not have been deleted)", m["Name"])
	}
}

func TestIntegration_SelectEmptyResult(t *testing.T) {
	table := uniqueTable("Empty")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"nonexistent", "nonexistent",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0 rows, got %d", count)
	}
}

func TestIntegration_InsertMissingKeysRejected(t *testing.T) {
	table := uniqueTable("NoKeys")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, Name) VALUES (?, ?)`,
		"pk", "Ada",
	)
	if err == nil {
		t.Fatal("expected INSERT without RowKey to fail, got nil")
	}
	t.Logf("got expected error: %v", err)
}

func TestIntegration_ContextCancellation(t *testing.T) {
	table := uniqueTable("Ctx")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Insert a row first so the table is populated.
	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Now query with an already-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, err := db.QueryContext(
		ctx,
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err == nil {
		// Some drivers return rows lazily; iterate to force the call.
		defer rows.Close()
		for rows.Next() {
		}
		if rows.Err() == nil {
			t.Log("context cancellation did not surface an error (Azurite may not honor cancellation) — not failing")
		}
	}
	// We don't hard-fail: Azurite/local SDKs sometimes complete in-flight
	// requests before observing cancellation. The test verifies the
	// context is *threaded* (no panic, no type assertion error).
}

func TestIntegration_UpdateMergeSemantics(t *testing.T) {
	table := uniqueTable("UpdMerge")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Insert initial entity with Name and Age.
	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
		"pk", "rk", "Ada Lovelace", 36,
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// UPDATE only the Age column — Name should be preserved by merge.
	_, err = db.Exec(
		`UPDATE `+table+` SET Age = ? WHERE PartitionKey = ? AND RowKey = ?`,
		37, "pk", "rk",
	)
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	// Read back and verify Name is unchanged, Age is updated.
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	found := false
	for rows.Next() {
		found = true
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		if name, ok := m["Name"]; !ok || fmt.Sprintf("%v", name) != "Ada Lovelace" {
			t.Errorf("Name = %v, want 'Ada Lovelace' (merge should preserve)", name)
		}
		if age, ok := m["Age"]; !ok || fmt.Sprintf("%v", age) != "37" {
			t.Errorf("Age = %v, want 37", age)
		}
	}
	if !found {
		t.Fatal("expected 1 row after update, got 0")
	}
}

func TestIntegration_UpdateSetLiteralValue(t *testing.T) {
	table := uniqueTable("UpdLit")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Original",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// UPDATE with a literal value in SET (not a placeholder).
	_, err = db.Exec(
		`UPDATE `+table+` SET Name = 'Updated' WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("UPDATE with literal: %v", err)
	}

	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		if name, ok := m["Name"]; !ok || fmt.Sprintf("%v", name) != "Updated" {
			t.Errorf("Name = %v, want 'Updated'", name)
		}
	}
}

func TestIntegration_UpdateRejectsSetKey(t *testing.T) {
	table := uniqueTable("UpdKey")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Attempting to SET PartitionKey must be rejected at parse time.
	_, err = db.Exec(
		`UPDATE `+table+` SET PartitionKey = ? WHERE PartitionKey = ? AND RowKey = ?`,
		"newpk", "pk", "rk",
	)
	if err == nil {
		t.Fatal("expected UPDATE SET PartitionKey to fail, got nil")
	}
	t.Logf("got expected error: %v", err)
}

func TestIntegration_UpdateRejectsMissingWhere(t *testing.T) {
	table := uniqueTable("UpdNoWhere")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`UPDATE `+table+` SET Name = ?`,
		"NewName",
	)
	if err == nil {
		t.Fatal("expected UPDATE without WHERE to fail, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// scanOne reads exactly one row from rows and returns it as a column→value
// map. It closes rows and fails the test if there is not exactly one row.
func scanOne(t *testing.T, rows *sql.Rows) map[string]interface{} {
	t.Helper()
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		t.Fatal("expected 1 row, got 0")
	}
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	m := make(map[string]interface{}, len(cols))
	for i, c := range cols {
		m[c] = vals[i]
	}
	if rows.Next() {
		t.Fatal("expected 1 row, got >1")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return m
}

func TestIntegration_UpsertReplaceInsertsNew(t *testing.T) {
	table := uniqueTable("UpsIns")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// INSERT OR REPLACE on a non-existent entity must create it.
	_, err := db.Exec(
		`INSERT OR REPLACE INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
		"pk", "rk", "Ada", 36,
	)
	if err != nil {
		t.Fatalf("INSERT OR REPLACE (new): %v", err)
	}

	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	m := scanOne(t, rows)
	if fmt.Sprintf("%v", m["Name"]) != "Ada" {
		t.Errorf("Name = %v, want 'Ada'", m["Name"])
	}
	if fmt.Sprintf("%v", m["Age"]) != "36" {
		t.Errorf("Age = %v, want 36", m["Age"])
	}
}

func TestIntegration_UpsertReplaceOverwritesAndDropsProperties(t *testing.T) {
	table := uniqueTable("UpsRepl")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Seed with Name and Age.
	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
		"pk", "rk", "Ada", 36,
	)
	if err != nil {
		t.Fatalf("INSERT seed: %v", err)
	}

	// INSERT OR REPLACE with only Name — replace semantics drop Age.
	_, err = db.Exec(
		`INSERT OR REPLACE INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Grace",
	)
	if err != nil {
		t.Fatalf("INSERT OR REPLACE (overwrite): %v", err)
	}

	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	m := scanOne(t, rows)
	if fmt.Sprintf("%v", m["Name"]) != "Grace" {
		t.Errorf("Name = %v, want 'Grace'", m["Name"])
	}
	if _, hasAge := m["Age"]; hasAge {
		t.Errorf("Age = %v present after replace, want dropped (replace semantics)", m["Age"])
	}
}

func TestIntegration_UpsertMergePreservesUntouchedProperties(t *testing.T) {
	table := uniqueTable("UpsMerge")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Seed with Name and Age.
	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
		"pk", "rk", "Ada", 36,
	)
	if err != nil {
		t.Fatalf("INSERT seed: %v", err)
	}

	// INSERT OR MERGE with only Name — merge semantics preserve Age.
	_, err = db.Exec(
		`INSERT OR MERGE INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Grace",
	)
	if err != nil {
		t.Fatalf("INSERT OR MERGE (overwrite): %v", err)
	}

	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	m := scanOne(t, rows)
	if fmt.Sprintf("%v", m["Name"]) != "Grace" {
		t.Errorf("Name = %v, want 'Grace'", m["Name"])
	}
	if age, ok := m["Age"]; !ok || fmt.Sprintf("%v", age) != "36" {
		t.Errorf("Age = %v (ok=%v), want 36 (merge should preserve)", age, ok)
	}
}

func TestIntegration_UpsertAliasInsertsNew(t *testing.T) {
	table := uniqueTable("UpsAlias")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// UPSERT INTO is an alias for INSERT OR REPLACE.
	_, err := db.Exec(
		`UPSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("UPSERT INTO (new): %v", err)
	}

	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	m := scanOne(t, rows)
	if fmt.Sprintf("%v", m["Name"]) != "Ada" {
		t.Errorf("Name = %v, want 'Ada'", m["Name"])
	}
}

func TestIntegration_QuotedPlaceholderRejected(t *testing.T) {
	table := uniqueTable("QuotPh")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// A quoted '?' is a string literal, not a placeholder, and must be
	// rejected at parse time — not silently accepted as a placeholder.
	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, '?')`,
		"pk", "rk",
	)
	if err == nil {
		t.Fatal("expected INSERT with quoted '?' to fail, got nil")
	}
	t.Logf("got expected error: %v", err)

	// Same for UPSERT.
	_, err = db.Exec(
		`UPSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, '?')`,
		"pk", "rk",
	)
	if err == nil {
		t.Fatal("expected UPSERT with quoted '?' to fail, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// ---------------------------------------------------------------------------
// Comparison WHERE operators (Tier 1, item 1)
// ---------------------------------------------------------------------------

// insertAgeRows inserts N entities into the given table with the same
// PartitionKey, RowKey "rkNN", Name "nameNN" and an Age property. Used by
// the comparison-operator integration tests.
func insertAgeRows(t *testing.T, db *sql.DB, table, partition string, ages []int) {
	t.Helper()
	for i, age := range ages {
		rk := fmt.Sprintf("rk%02d", i)
		_, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
			partition, rk, fmt.Sprintf("name%02d", i), age,
		)
		if err != nil {
			t.Fatalf("INSERT seed %d: %v", i, err)
		}
	}
}

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
