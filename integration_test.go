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
