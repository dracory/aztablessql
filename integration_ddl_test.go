//go:build integration

package aztablessql

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// ---------------------------------------------------------------------------
// Table management (DDL) — CREATE/DROP/SHOW TABLES
// ---------------------------------------------------------------------------

// openDBNoCreate opens a *sql.DB without pre-creating the table. Used by
// DDL tests that need to exercise CREATE TABLE themselves.
func openDBNoCreate(t *testing.T) *sql.DB {
	t.Helper()
	connStr := testConnStr(t)
	db, err := sql.Open("aztables", connStr)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// tableExists checks via the SDK whether a table exists on the account.
// Used as an independent oracle in DDL tests.
func tableExists(t *testing.T, table string) bool {
	t.Helper()
	svc, err := aztables.NewServiceClientFromConnectionString(testConnStr(t), nil)
	if err != nil {
		t.Fatalf("create service client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pager := svc.NewListTablesPager(nil)
	for pager.More() {
		resp, err := pager.NextPage(ctx)
		if err != nil {
			t.Fatalf("list tables: %v", err)
		}
		for _, tbl := range resp.Tables {
			if tbl.Name != nil && *tbl.Name == table {
				return true
			}
		}
	}
	return false
}

func TestIntegration_CreateTable(t *testing.T) {
	table := uniqueTable("DDLCreate")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDBNoCreate(t)

	_, err := db.Exec(`CREATE TABLE ` + table)
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if !tableExists(t, table) {
		t.Fatalf("table %q not found after CREATE TABLE", table)
	}
}

func TestIntegration_CreateTableIfNotExistsIsNoOp(t *testing.T) {
	table := uniqueTable("DDLCreateINE")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDBNoCreate(t)

	// First create succeeds.
	if _, err := db.Exec(`CREATE TABLE ` + table); err != nil {
		t.Fatalf("first CREATE TABLE: %v", err)
	}
	// Second create without IF NOT EXISTS should fail (409 Conflict).
	_, err := db.Exec(`CREATE TABLE ` + table)
	if err == nil {
		t.Fatal("expected 409 on duplicate CREATE TABLE, got nil")
	}
	// IF NOT EXISTS should tolerate the conflict.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ` + table); err != nil {
		t.Fatalf("CREATE TABLE IF NOT EXISTS on existing: %v", err)
	}
}

func TestIntegration_DropTable(t *testing.T) {
	table := uniqueTable("DDLDrop")
	// Pre-create via SDK so DROP TABLE is what we test.
	svc, err := aztables.NewServiceClientFromConnectionString(testConnStr(t), nil)
	if err != nil {
		t.Fatalf("create service client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := svc.CreateTable(ctx, table, nil); err != nil {
		t.Fatalf("seed CreateTable: %v", err)
	}
	t.Cleanup(func() { dropTable(t, table) })

	db := openDBNoCreate(t)
	if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
		t.Fatalf("DROP TABLE: %v", err)
	}
	if tableExists(t, table) {
		t.Fatalf("table %q still present after DROP TABLE", table)
	}
}

func TestIntegration_DropTableIfExistsIsNoOp(t *testing.T) {
	table := uniqueTable("DDLDropIE")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDBNoCreate(t)

	// DROP on a missing table without IF EXISTS should fail (404).
	_, err := db.Exec(`DROP TABLE ` + table)
	if err == nil {
		t.Fatal("expected 404 on DROP of missing table, got nil")
	}
	// IF EXISTS should tolerate the 404.
	if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
		t.Fatalf("DROP TABLE IF EXISTS on missing: %v", err)
	}
}

func TestIntegration_ShowTablesListsCreatedTable(t *testing.T) {
	table := uniqueTable("DDLShow")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDBNoCreate(t)

	if _, err := db.Exec(`CREATE TABLE ` + table); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	rows, err := db.Query(`SHOW TABLES`)
	if err != nil {
		t.Fatalf("SHOW TABLES: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) != 1 || cols[0] != "TableName" {
		t.Fatalf("columns = %v, want [TableName]", cols)
	}

	found := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if name == table {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if !found {
		t.Fatalf("table %q not found in SHOW TABLES output", table)
	}
}

func TestIntegration_ShowTablesColumnShape(t *testing.T) {
	// Verifies the SHOW TABLES column shape and that iteration completes
	// without error regardless of how many tables the account happens to
	// hold. We cannot assert an empty result on a shared Azurite account,
	// so this is a shape/contract test rather than an emptiness test.
	db := openDBNoCreate(t)
	rows, err := db.Query(`SHOW TABLES`)
	if err != nil {
		t.Fatalf("SHOW TABLES: %v", err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) != 1 || cols[0] != "TableName" {
		t.Fatalf("columns = %v, want [TableName]", cols)
	}
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	t.Logf("SHOW TABLES returned %d tables", count)
}

func TestIntegration_CreateTableRejectsColumnDefs(t *testing.T) {
	table := uniqueTable("DDLCreateCols")
	t.Cleanup(func() { dropTable(t, table) })
	db := openDBNoCreate(t)

	_, err := db.Exec(`CREATE TABLE ` + table + ` (PartitionKey, RowKey, Name)`)
	if err == nil {
		t.Fatal("expected error for CREATE TABLE with column defs, got nil")
	}
	if !strings.Contains(err.Error(), "schemaless") {
		t.Errorf("expected 'schemaless' in error, got %v", err)
	}
	// The table must not have been created.
	if tableExists(t, table) {
		t.Fatalf("table %q was created despite the rejected statement", table)
	}
}
