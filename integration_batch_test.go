//go:build integration

package aztablessql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Batch / Entity Group Transactions
// ---------------------------------------------------------------------------

// countPartition returns the number of entities in the given partition by
// scanning the list path. Used by the batch tests to verify visibility and
// rollback.
func countPartition(t *testing.T, db *sql.DB, table, pk string) int {
	t.Helper()
	rows, err := db.Query(
		`SELECT RowKey FROM `+table+` WHERE PartitionKey = ?`,
		pk,
	)
	if err != nil {
		t.Fatalf("countPartition SELECT: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("countPartition rows.Err: %v", err)
	}
	return n
}

// submitBatchViaRaw submits a batch through the documented database/sql
// Conn.Raw escape hatch. It returns the error from SubmitBatch (or from the
// Raw callback plumbing).
func submitBatchViaRaw(t *testing.T, db *sql.DB, table string, ops []BatchOp) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	defer conn.Close()
	var batchErr error
	if err := conn.Raw(func(driverConn interface{}) error {
		dc, ok := driverConn.(*Conn)
		if !ok {
			return fmt.Errorf("driver conn is %T, want *aztablessql.Conn", driverConn)
		}
		batchErr = dc.BatchClient(table).SubmitBatch(ctx, ops)
		return nil
	}); err != nil {
		t.Fatalf("conn.Raw: %v", err)
	}
	return batchErr
}

// TestIntegration_BatchInsertSuccess submits 3 inserts in one batch and
// verifies all three become visible.
func TestIntegration_BatchInsertSuccess(t *testing.T) {
	table := uniqueTable("BatchOk")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	ops := []BatchOp{
		{Kind: BatchInsert, Partition: "pk", Row: "r1", Properties: map[string]interface{}{"Name": "Ada", "Age": int64(36)}},
		{Kind: BatchInsert, Partition: "pk", Row: "r2", Properties: map[string]interface{}{"Name": "Bob", "Age": int64(40)}},
		{Kind: BatchInsert, Partition: "pk", Row: "r3", Properties: map[string]interface{}{"Name": "Cy", "Age": int64(50)}},
	}
	if err := submitBatchViaRaw(t, db, table, ops); err != nil {
		t.Fatalf("SubmitBatch: %v", err)
	}

	if got := countPartition(t, db, table, "pk"); got != 3 {
		t.Errorf("expected 3 entities after batch, got %d", got)
	}

	// Verify one entity's content via point read.
	rows, err := db.Query(
		`SELECT Name, Age FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "r2",
	)
	if err != nil {
		t.Fatalf("SELECT r2: %v", err)
	}
	m := scanOne(t, rows)
	if name, _ := m["Name"].(string); name != "Bob" {
		t.Errorf("r2 Name = %v, want 'Bob'", m["Name"])
	}
}

// TestIntegration_BatchRollbackOnConflict submits a batch where one op
// conflicts with a pre-existing entity (Insert on a RowKey that already
// exists). The whole batch must roll back: none of the 3 ops should be
// visible afterwards (the pre-existing entity remains, but the 3 new ones
// must not).
func TestIntegration_BatchRollbackOnConflict(t *testing.T) {
	table := uniqueTable("BatchRoll")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Pre-create an entity at r0 so the batch's Insert on r0 conflicts.
	if _, err := db.Exec(
		`INSERT INTO `+table+` (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
		"pk", "r0", "PreExisting",
	); err != nil {
		t.Fatalf("seed INSERT: %v", err)
	}

	ops := []BatchOp{
		{Kind: BatchInsert, Partition: "pk", Row: "r1", Properties: map[string]interface{}{"Name": "Ada"}},
		{Kind: BatchInsert, Partition: "pk", Row: "r2", Properties: map[string]interface{}{"Name": "Bob"}},
		// Conflict: r0 already exists; Insert (Add) must fail and roll back the batch.
		{Kind: BatchInsert, Partition: "pk", Row: "r0", Properties: map[string]interface{}{"Name": "Conflict"}},
	}
	err := submitBatchViaRaw(t, db, table, ops)
	if err == nil {
		t.Fatal("expected SubmitBatch to fail on conflict, got nil")
	}
	t.Logf("batch conflict error (expected): %v", err)

	// r1 and r2 must NOT be visible (rolled back). r0 remains as seeded.
	if got := countPartition(t, db, table, "pk"); got != 1 {
		t.Errorf("expected 1 entity (only the pre-existing r0) after rolled-back batch, got %d", got)
	}

	// Confirm r1 is absent.
	rows, err := db.Query(
		`SELECT Name FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "r1",
	)
	if err != nil {
		t.Fatalf("SELECT r1: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("r1 should not exist after batch rollback")
	}
}

// TestIntegration_BatchMixedKinds submits a batch combining an insert, an
// update-merge, and a delete, and verifies the end state.
func TestIntegration_BatchMixedKinds(t *testing.T) {
	table := uniqueTable("BatchMix")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	// Seed two entities: r1 (will be updated) and r2 (will be deleted).
	for _, rk := range []string{"r1", "r2"} {
		if _, err := db.Exec(
			`INSERT INTO `+table+` (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
			"pk", rk, "seed", int64(1),
		); err != nil {
			t.Fatalf("seed INSERT %s: %v", rk, err)
		}
	}

	ops := []BatchOp{
		{Kind: BatchInsert, Partition: "pk", Row: "r3", Properties: map[string]interface{}{"Name": "new", "Age": int64(9)}},
		{Kind: BatchUpdateMerge, Partition: "pk", Row: "r1", Properties: map[string]interface{}{"Name": "updated"}},
		{Kind: BatchDelete, Partition: "pk", Row: "r2"},
	}
	if err := submitBatchViaRaw(t, db, table, ops); err != nil {
		t.Fatalf("SubmitBatch: %v", err)
	}

	// End state: r1 (updated), r3 (new). r2 deleted. Total 2.
	if got := countPartition(t, db, table, "pk"); got != 2 {
		t.Errorf("expected 2 entities after mixed batch, got %d", got)
	}

	// r1 Name should be "updated"; Age untouched by merge.
	rows, err := db.Query(
		`SELECT Name, Age FROM `+table+` WHERE PartitionKey = ? AND RowKey = ?`,
		"pk", "r1",
	)
	if err != nil {
		t.Fatalf("SELECT r1: %v", err)
	}
	m := scanOne(t, rows)
	if name, _ := m["Name"].(string); name != "updated" {
		t.Errorf("r1 Name = %v, want 'updated'", m["Name"])
	}
	if age, ok := m["Age"]; !ok || fmt.Sprintf("%v", age) != "1" {
		t.Errorf("r1 Age = %v, want 1 (merge must preserve untouched properties)", age)
	}
}

// TestIntegration_BatchRejectsMixedPartitions verifies the client-side
// guard fires before any network round-trip.
func TestIntegration_BatchRejectsMixedPartitions(t *testing.T) {
	table := uniqueTable("BatchMixP")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDB(t, table)

	ops := []BatchOp{
		{Kind: BatchInsert, Partition: "p1", Row: "r1", Properties: map[string]interface{}{"Name": "a"}},
		{Kind: BatchInsert, Partition: "p2", Row: "r2", Properties: map[string]interface{}{"Name": "b"}},
	}
	err := submitBatchViaRaw(t, db, table, ops)
	if err == nil {
		t.Fatal("expected error for mixed partitions, got nil")
	}
	if !strings.Contains(err.Error(), "single partition") {
		t.Errorf("expected 'single partition' in error, got %v", err)
	}
}
