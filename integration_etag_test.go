//go:build integration

package aztablessql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// ---------------------------------------------------------------------------
// ETag-based optimistic concurrency + Timestamp/ETag pseudo-columns
// (Tier 1, item 3 + Tier 3, item 8)
// ---------------------------------------------------------------------------

// fetchETag reads a single entity via SELECT * and returns its ETag value
// (string). Fails the test if the ETag column is missing or empty.
func fetchETag(t *testing.T, db *sql.DB, table, pk, rk string) string {
	t.Helper()
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		pk, rk,
	)
	if err != nil {
		t.Fatalf("SELECT for ETag: %v", err)
	}
	m := scanOne(t, rows)
	v, ok := m["ETag"]
	if !ok {
		t.Fatalf("SELECT * did not surface an ETag column; got columns: %v", mapKeys(m))
	}
	s := fmt.Sprintf("%v", v)
	if s == "" || s == "<nil>" {
		t.Fatalf("ETag is empty; got row: %v", m)
	}
	return s
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestIntegration_SelectSurfacesETag verifies that SELECT * returns an ETag
// column populated from the entity's odata.etag field.
func TestIntegration_SelectSurfacesETag(t *testing.T) {
	table := uniqueTable("EtagSurf")
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
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	m := scanOne(t, rows)

	v, ok := m["ETag"]
	if !ok {
		t.Fatalf("ETag column not surfaced in SELECT *; columns: %v", mapKeys(m))
	}
	s := fmt.Sprintf("%v", v)
	if s == "" || s == "<nil>" {
		t.Errorf("ETag value is empty; row: %v", m)
	}
	t.Logf("ETag from SELECT *: %s", s)
}

// TestIntegration_SelectSurfacesTimestamp verifies that SELECT * returns a
// Timestamp column populated from the server-managed Timestamp field.
func TestIntegration_SelectSurfacesTimestamp(t *testing.T) {
	table := uniqueTable("TsSurf")
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
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	m := scanOne(t, rows)

	v, ok := m["Timestamp"]
	if !ok {
		t.Fatalf("Timestamp column not surfaced in SELECT *; columns: %v", mapKeys(m))
	}
	s := fmt.Sprintf("%v", v)
	if s == "" || s == "<nil>" {
		t.Errorf("Timestamp value is empty; row: %v", m)
	}
	t.Logf("Timestamp from SELECT *: %s", s)
}

// TestIntegration_SelectExplicitETagAndTimestamp verifies that ETag and
// Timestamp can be selected explicitly by name.
func TestIntegration_SelectExplicitETagAndTimestamp(t *testing.T) {
	table := uniqueTable("EtagTsExp")
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
		`SELECT ETag, Timestamp FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT ETag, Timestamp: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) != 2 {
		t.Fatalf("expected 2 columns, got %d: %v", len(cols), cols)
	}
	// Column names should preserve the caller's casing.
	if cols[0] != "ETag" || cols[1] != "Timestamp" {
		t.Errorf("columns = %v, want [ETag Timestamp]", cols)
	}

	if !rows.Next() {
		t.Fatal("expected 1 row, got 0")
	}
	vals := make([]interface{}, 2)
	ptrs := make([]interface{}, 2)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	etag := fmt.Sprintf("%v", vals[0])
	ts := fmt.Sprintf("%v", vals[1])
	if etag == "" || etag == "<nil>" {
		t.Errorf("ETag value is empty: %v", vals[0])
	}
	if ts == "" || ts == "<nil>" {
		t.Errorf("Timestamp value is empty: %v", vals[1])
	}
	t.Logf("ETag=%s Timestamp=%s", etag, ts)
}

// TestIntegration_UpdateWithETagSuccess verifies that an UPDATE with the
// correct ETag succeeds.
func TestIntegration_UpdateWithETagSuccess(t *testing.T) {
	table := uniqueTable("EtagUpd")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
		"pk", "rk", "Ada", 36,
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	etag := fetchETag(t, db, table, "pk", "rk")

	_, err = db.Exec(
		`UPDATE `+table+` SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND ETag = ?`,
		37, "pk", "rk", etag,
	)
	if err != nil {
		t.Fatalf("UPDATE with correct ETag: %v", err)
	}

	// Verify the update landed.
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	m := scanOne(t, rows)
	if fmt.Sprintf("%v", m["Age"]) != "37" {
		t.Errorf("Age = %v, want 37", m["Age"])
	}
}

// TestIntegration_UpdateWithStaleETagFails verifies that an UPDATE with a
// stale ETag (after a concurrent modification) fails with a 412 error.
func TestIntegration_UpdateWithStaleETagFails(t *testing.T) {
	table := uniqueTable("EtagStale")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
		"pk", "rk", "Ada", 36,
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	etag := fetchETag(t, db, table, "pk", "rk")

	// Simulate a concurrent writer by updating the entity via a second SDK
	// client (bypassing the SQL driver).
	svc, err := aztables.NewServiceClientFromConnectionString(testConnStr(t), nil)
	if err != nil {
		t.Fatalf("create second service client: %v", err)
	}
	client := svc.NewClient(table)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Force a change that alters the ETag by updating via the SDK directly.
	entity := map[string]interface{}{
		"PartitionKey": "pk",
		"RowKey":       "rk",
		"Name":         "Ada",
		"Age":          99,
	}
	b, err := json.Marshal(entity)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := client.UpdateEntity(ctx, b, &aztables.UpdateEntityOptions{
		UpdateMode: aztables.UpdateModeReplace,
	}); err != nil {
		t.Fatalf("concurrent UpdateEntity: %v", err)
	}

	// Now UPDATE via the SQL driver using the stale ETag — must fail.
	_, err = db.Exec(
		`UPDATE `+table+` SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND ETag = ?`,
		42, "pk", "rk", etag,
	)
	if err == nil {
		t.Fatal("expected UPDATE with stale ETag to fail, got nil")
	}
	if !strings.Contains(err.Error(), "aztablessql: ETag precondition failed") {
		t.Errorf("error = %q, want it to contain 'aztablessql: ETag precondition failed'", err.Error())
	}
	t.Logf("got expected stale-ETag error: %v", err)
}

// TestIntegration_DeleteWithETagSuccess verifies that a DELETE with the
// correct ETag succeeds.
func TestIntegration_DeleteWithETagSuccess(t *testing.T) {
	table := uniqueTable("EtagDel")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	etag := fetchETag(t, db, table, "pk", "rk")

	_, err = db.Exec(
		`DELETE FROM `+table+` WHERE PartitionKey = ? AND RowKey = ? AND ETag = ?`,
		"pk", "rk", etag,
	)
	if err != nil {
		t.Fatalf("DELETE with correct ETag: %v", err)
	}

	// Verify the entity is gone.
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("expected 0 rows after delete, got at least 1")
	}
}

// TestIntegration_DeleteWithStaleETagFails verifies that a DELETE with a
// stale ETag (after a concurrent modification) fails.
func TestIntegration_DeleteWithStaleETagFails(t *testing.T) {
	table := uniqueTable("EtagDelStale")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	etag := fetchETag(t, db, table, "pk", "rk")

	// Concurrent modification via a second SDK client.
	svc, err := aztables.NewServiceClientFromConnectionString(testConnStr(t), nil)
	if err != nil {
		t.Fatalf("create second service client: %v", err)
	}
	client := svc.NewClient(table)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entity := map[string]interface{}{
		"PartitionKey": "pk",
		"RowKey":       "rk",
		"Name":         "Grace",
	}
	b, err := json.Marshal(entity)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := client.UpdateEntity(ctx, b, &aztables.UpdateEntityOptions{
		UpdateMode: aztables.UpdateModeReplace,
	}); err != nil {
		t.Fatalf("concurrent UpdateEntity: %v", err)
	}

	// DELETE via the SQL driver with the stale ETag — must fail.
	_, err = db.Exec(
		`DELETE FROM `+table+` WHERE PartitionKey = ? AND RowKey = ? AND ETag = ?`,
		"pk", "rk", etag,
	)
	if err == nil {
		t.Fatal("expected DELETE with stale ETag to fail, got nil")
	}
	if !strings.Contains(err.Error(), "aztablessql: ETag precondition failed") {
		t.Errorf("error = %q, want it to contain 'aztablessql: ETag precondition failed'", err.Error())
	}
	t.Logf("got expected stale-ETag delete error: %v", err)

	// Entity should still exist.
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	m := scanOne(t, rows)
	if fmt.Sprintf("%v", m["Name"]) != "Grace" {
		t.Errorf("Name = %v, want 'Grace' (concurrent update should have landed)", m["Name"])
	}
}

// TestIntegration_DeleteWithETagStar verifies that ETag = '*' on an existing
// entity succeeds (matches any existing entity).
func TestIntegration_DeleteWithETagStar(t *testing.T) {
	table := uniqueTable("EtagStar")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// ETag = '*' on an existing entity should succeed.
	_, err = db.Exec(
		`DELETE FROM `+table+` WHERE PartitionKey = ? AND RowKey = ? AND ETag = '*'`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("DELETE with ETag='*' on existing entity: %v", err)
	}

	// Entity should be gone.
	rows, err := db.Query(
		`SELECT * FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "rk",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("expected 0 rows after ETag='*' delete, got at least 1")
	}
}

// TestIntegration_DeleteWithETagStarOnMissingEntity verifies that ETag = '*'
// on a non-existent entity fails (the '*' If-Match fails when the entity
// does not exist).
func TestIntegration_DeleteWithETagStarOnMissingEntity(t *testing.T) {
	table := uniqueTable("EtagStarMiss")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// No insert — the entity does not exist. ETag='*' should fail.
	_, err := db.Exec(
		`DELETE FROM `+table+` WHERE PartitionKey = ? AND RowKey = ? AND ETag = '*'`,
		"pk", "missing",
	)
	if err == nil {
		t.Fatal("expected DELETE with ETag='*' on missing entity to fail, got nil")
	}
	t.Logf("got expected error for ETag='*' on missing entity: %v", err)
}

// TestIntegration_UpdateRejectsSetTimestamp verifies that SET Timestamp is
// rejected at parse time (Timestamp is a server-managed read-only field).
func TestIntegration_UpdateRejectsSetTimestamp(t *testing.T) {
	table := uniqueTable("TsSetRej")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	_, err = db.Exec(
		`UPDATE `+table+` SET Timestamp = ? WHERE PartitionKey = ? AND RowKey = ?`,
		"2026-01-01T00:00:00Z", "pk", "rk",
	)
	if err == nil {
		t.Fatal("expected UPDATE SET Timestamp to fail, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// TestIntegration_InsertRejectsETagColumn verifies that INSERT with an ETag
// column is rejected (ETag is server-managed).
func TestIntegration_InsertRejectsETagColumn(t *testing.T) {
	table := uniqueTable("EtagInsRej")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, ETag) VALUES (?, ?, ?)`,
		"pk", "rk", `W/"0xABC"`,
	)
	if err == nil {
		t.Fatal("expected INSERT with ETag column to fail, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// TestIntegration_UpdateETagChangesAfterUpdate verifies that the ETag changes
// after an update, which is the foundation of optimistic concurrency.
func TestIntegration_UpdateETagChangesAfterUpdate(t *testing.T) {
	table := uniqueTable("EtagChg")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	_, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "rk", "Ada",
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	etag1 := fetchETag(t, db, table, "pk", "rk")

	_, err = db.Exec(
		`UPDATE `+table+` SET Name = ? WHERE PartitionKey = ? AND RowKey = ?`,
		"Grace", "pk", "rk",
	)
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	etag2 := fetchETag(t, db, table, "pk", "rk")

	if etag1 == etag2 {
		t.Errorf("ETag did not change after update; before=%q after=%q", etag1, etag2)
	}
	t.Logf("ETag changed: %q -> %q", etag1, etag2)
}
