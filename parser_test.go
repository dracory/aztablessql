package aztablessql

import (
	"testing"
)

func TestParseInsert(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantTable string
		wantCols  []string
		wantN     int
		wantErr   bool
	}{
		{
			name:      "basic",
			query:     "INSERT INTO People (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)",
			wantTable: "People",
			wantCols:  []string{"PartitionKey", "RowKey", "Name", "Age"},
			wantN:     4,
		},
		{
			name:      "trailing semicolon",
			query:     "INSERT INTO T (A, B) VALUES (?, ?);",
			wantTable: "T",
			wantCols:  []string{"A", "B"},
			wantN:     2,
		},
		{
			name:      "lowercase keywords",
			query:     "insert into t (a, b) values (?, ?)",
			wantTable: "t",
			wantCols:  []string{"a", "b"},
			wantN:     2,
		},
		{
			name:    "literal value rejected",
			query:   "INSERT INTO T (A, B) VALUES (?, 'x')",
			wantErr: true,
		},
		{
			name:    "col/val count mismatch",
			query:   "INSERT INTO T (A, B, C) VALUES (?, ?)",
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
			if pq.kind != qInsert {
				t.Fatalf("kind = %d, want qInsert", pq.kind)
			}
			if pq.table != tc.wantTable {
				t.Fatalf("table = %q, want %q", pq.table, tc.wantTable)
			}
			if len(pq.columns) != len(tc.wantCols) {
				t.Fatalf("columns = %v, want %v", pq.columns, tc.wantCols)
			}
			for i, c := range pq.columns {
				if c != tc.wantCols[i] {
					t.Fatalf("columns[%d] = %q, want %q", i, c, tc.wantCols[i])
				}
			}
			if pq.numPlaceholders != tc.wantN {
				t.Fatalf("numPlaceholders = %d, want %d", pq.numPlaceholders, tc.wantN)
			}
		})
	}
}

func TestParseDelete(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantTable string
		wantN     int
		wantErr   bool
	}{
		{
			name:      "with where placeholders",
			query:     "DELETE FROM People WHERE PartitionKey = ? AND RowKey = ?",
			wantTable: "People",
			wantN:     2,
		},
		{
			name:      "with where literals",
			query:     "DELETE FROM People WHERE PartitionKey = 'pk' AND RowKey = 'rk'",
			wantTable: "People",
			wantN:     0,
		},
		{
			name:      "no where",
			query:     "DELETE FROM People",
			wantTable: "People",
			wantN:     0,
		},
		{
			name:    "unsupported operator",
			query:   "DELETE FROM People WHERE Age > 5",
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
			if pq.kind != qDelete {
				t.Fatalf("kind = %d, want qDelete", pq.kind)
			}
			if pq.table != tc.wantTable {
				t.Fatalf("table = %q, want %q", pq.table, tc.wantTable)
			}
			if pq.numPlaceholders != tc.wantN {
				t.Fatalf("numPlaceholders = %d, want %d", pq.numPlaceholders, tc.wantN)
			}
		})
	}
}

func TestParseSelect(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantTable string
		wantAll   bool
		wantCols  []string
		wantN     int
		wantErr   bool
	}{
		{
			name:      "star point read",
			query:     "SELECT * FROM People WHERE PartitionKey = ? AND RowKey = ?",
			wantTable: "People",
			wantAll:   true,
			wantN:     2,
		},
		{
			name:      "explicit cols",
			query:     "SELECT Name, Age FROM People WHERE PartitionKey = 'pk'",
			wantTable: "People",
			wantCols:  []string{"Name", "Age"},
			wantN:     0,
		},
		{
			name:      "no where",
			query:     "SELECT * FROM People",
			wantTable: "People",
			wantAll:   true,
			wantN:     0,
		},
		{
			name:    "unsupported where",
			query:   "SELECT * FROM People WHERE Age <> 5",
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
			if pq.kind != qSelect {
				t.Fatalf("kind = %d, want qSelect", pq.kind)
			}
			if pq.table != tc.wantTable {
				t.Fatalf("table = %q, want %q", pq.table, tc.wantTable)
			}
			if pq.allColumns != tc.wantAll {
				t.Fatalf("allColumns = %v, want %v", pq.allColumns, tc.wantAll)
			}
			if !tc.wantAll {
				if len(pq.columns) != len(tc.wantCols) {
					t.Fatalf("columns = %v, want %v", pq.columns, tc.wantCols)
				}
				for i, c := range pq.columns {
					if c != tc.wantCols[i] {
						t.Fatalf("columns[%d] = %q, want %q", i, c, tc.wantCols[i])
					}
				}
			}
			if pq.numPlaceholders != tc.wantN {
				t.Fatalf("numPlaceholders = %d, want %d", pq.numPlaceholders, tc.wantN)
			}
		})
	}
}

func TestParseUnsupported(t *testing.T) {
	for _, q := range []string{
		"UPDATE People SET Name = 'x'",
		"DROP TABLE People",
		"SELECT * FROM People JOIN Other ON ...",
		"",
		"random text",
	} {
		if _, err := parseQuery(q); err == nil {
			t.Errorf("parseQuery(%q) expected error, got nil", q)
		}
	}
}

func TestParseWherePreservesColumnCase(t *testing.T) {
	pq, err := parseQuery("SELECT * FROM T WHERE MyCol = ? AND PartitionKey = ?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.where) != 2 {
		t.Fatalf("expected 2 conditions, got %d", len(pq.where))
	}
	if pq.where[0].column != "MyCol" {
		t.Fatalf("where[0].column = %q, want %q", pq.where[0].column, "MyCol")
	}
	if pq.where[1].column != "PartitionKey" {
		t.Fatalf("where[1].column = %q, want %q", pq.where[1].column, "PartitionKey")
	}
}
