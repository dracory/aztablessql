package aztablessql

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Table management (DDL) — CREATE/DROP/SHOW TABLES parser tests
// ---------------------------------------------------------------------------

func TestParseCreateTable(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantTable  string
		wantIfNot  bool
		wantErr    bool
		wantErrSub string
	}{
		{
			name:      "basic",
			query:     "CREATE TABLE People",
			wantTable: "People",
		},
		{
			name:      "if not exists",
			query:     "CREATE TABLE IF NOT EXISTS People",
			wantTable: "People",
			wantIfNot: true,
		},
		{
			name:      "trailing semicolon",
			query:     "CREATE TABLE People;",
			wantTable: "People",
		},
		{
			name:      "lowercase keywords",
			query:     "create table if not exists people",
			wantTable: "people",
			wantIfNot: true,
		},
		{
			name:      "mixed case if not exists",
			query:     "Create Table If Not Exists People",
			wantTable: "People",
			wantIfNot: true,
		},
		{
			name:      "irregular whitespace in if not exists",
			query:     "CREATE  TABLE   IF  NOT   EXISTS   People",
			wantTable: "People",
			wantIfNot: true,
		},
		{
			name:       "column definitions rejected",
			query:      "CREATE TABLE People (PartitionKey, RowKey, Name)",
			wantErr:    true,
			wantErrSub: "schemaless",
		},
		{
			name:       "column definitions rejected with if not exists",
			query:      "CREATE TABLE IF NOT EXISTS People (A, B)",
			wantErr:    true,
			wantErrSub: "schemaless",
		},
		{
			name:    "missing table name",
			query:   "CREATE TABLE",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pq, err := parseQuery(tc.query)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pq.kind != qCreateTable {
				t.Fatalf("kind = %d, want qCreateTable", pq.kind)
			}
			if pq.table != tc.wantTable {
				t.Fatalf("table = %q, want %q", pq.table, tc.wantTable)
			}
			if pq.ifNotExists != tc.wantIfNot {
				t.Fatalf("ifNotExists = %v, want %v", pq.ifNotExists, tc.wantIfNot)
			}
			if pq.numPlaceholders != 0 {
				t.Fatalf("numPlaceholders = %d, want 0", pq.numPlaceholders)
			}
		})
	}
}

func TestParseDropTable(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantTable string
		wantIfEx  bool
		wantErr   bool
	}{
		{
			name:      "basic",
			query:     "DROP TABLE People",
			wantTable: "People",
		},
		{
			name:      "if exists",
			query:     "DROP TABLE IF EXISTS People",
			wantTable: "People",
			wantIfEx:  true,
		},
		{
			name:      "trailing semicolon",
			query:     "DROP TABLE People;",
			wantTable: "People",
		},
		{
			name:      "lowercase keywords",
			query:     "drop table if exists people",
			wantTable: "people",
			wantIfEx:  true,
		},
		{
			name:      "irregular whitespace in if exists",
			query:     "DROP  TABLE   IF   EXISTS   People",
			wantTable: "People",
			wantIfEx:  true,
		},
		{
			name:    "missing table name",
			query:   "DROP TABLE",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pq, err := parseQuery(tc.query)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pq.kind != qDropTable {
				t.Fatalf("kind = %d, want qDropTable", pq.kind)
			}
			if pq.table != tc.wantTable {
				t.Fatalf("table = %q, want %q", pq.table, tc.wantTable)
			}
			if pq.ifExists != tc.wantIfEx {
				t.Fatalf("ifExists = %v, want %v", pq.ifExists, tc.wantIfEx)
			}
			if pq.numPlaceholders != 0 {
				t.Fatalf("numPlaceholders = %d, want 0", pq.numPlaceholders)
			}
		})
	}
}

func TestParseShowTables(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{"basic", "SHOW TABLES", false},
		{"trailing semicolon", "SHOW TABLES;", false},
		{"lowercase", "show tables", false},
		{"mixed case", "Show Tables", false},
		{"with extra token", "SHOW TABLES foo", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pq, err := parseQuery(tc.query)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pq.kind != qShowTables {
				t.Fatalf("kind = %d, want qShowTables", pq.kind)
			}
			if pq.table != "" {
				t.Fatalf("table = %q, want empty", pq.table)
			}
			if pq.numPlaceholders != 0 {
				t.Fatalf("numPlaceholders = %d, want 0", pq.numPlaceholders)
			}
		})
	}
}
