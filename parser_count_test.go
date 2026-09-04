package aztablessql

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// COUNT(*) (Tier 3, item 10)
// ---------------------------------------------------------------------------

func TestParseCount(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantTable string
		wantN     int
		wantErr   bool
	}{
		{
			name:      "basic no where",
			query:     "SELECT COUNT(*) FROM People",
			wantTable: "People",
			wantN:     0,
		},
		{
			name:      "with where placeholders",
			query:     "SELECT COUNT(*) FROM People WHERE PartitionKey = ?",
			wantTable: "People",
			wantN:     1,
		},
		{
			name:      "with where literals",
			query:     "SELECT COUNT(*) FROM People WHERE PartitionKey = 'pk' AND Age > '30'",
			wantTable: "People",
			wantN:     0,
		},
		{
			name:      "trailing semicolon",
			query:     "SELECT COUNT(*) FROM People;",
			wantTable: "People",
			wantN:     0,
		},
		{
			name:      "lowercase keywords",
			query:     "select count(*) from people where partitionkey = ?",
			wantTable: "people",
			wantN:     1,
		},
		{
			name:      "mixed case COUNT",
			query:     "Select Count(*) From People",
			wantTable: "People",
			wantN:     0,
		},
		{
			name:      "spaces inside parens",
			query:     "SELECT COUNT( * ) FROM People",
			wantTable: "People",
			wantN:     0,
		},
		{
			name:      "tabs inside parens",
			query:     "SELECT COUNT(\t*\t) FROM People",
			wantTable: "People",
			wantN:     0,
		},
		{
			name:      "range scan where",
			query:     "SELECT COUNT(*) FROM People WHERE PartitionKey >= 'a' AND PartitionKey < 'b'",
			wantTable: "People",
			wantN:     0,
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
			if pq.kind != qCount {
				t.Fatalf("kind = %d, want qCount", pq.kind)
			}
			if pq.table != tc.wantTable {
				t.Fatalf("table = %q, want %q", pq.table, tc.wantTable)
			}
			if pq.numPlaceholders != tc.wantN {
				t.Fatalf("numPlaceholders = %d, want %d", pq.numPlaceholders, tc.wantN)
			}
			if pq.allColumns {
				t.Fatalf("allColumns = true, want false (COUNT does not select columns)")
			}
			if len(pq.columns) != 0 {
				t.Fatalf("columns = %v, want empty", pq.columns)
			}
		})
	}
}

// TestParseCountRejectsETagAndTimestampInWhere verifies that the SELECT
// WHERE pseudo-column restriction also applies to COUNT(*).
func TestParseCountRejectsETagAndTimestampInWhere(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"etag in where", "SELECT COUNT(*) FROM T WHERE ETag = ?"},
		{"timestamp in where", "SELECT COUNT(*) FROM T WHERE Timestamp = ?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseQuery(tc.query)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "read-only pseudo-column") {
				t.Errorf("error = %q, want it to contain 'read-only pseudo-column'", err.Error())
			}
		})
	}
}

// TestParseCountRejectsCountCol verifies that COUNT(col) is rejected with a
// clear message — only COUNT(*) is supported.
func TestParseCountRejectsCountCol(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"count column", "SELECT COUNT(Name) FROM People"},
		{"count column with where", "SELECT COUNT(Age) FROM People WHERE PartitionKey = ?"},
		{"count key column", "SELECT COUNT(PartitionKey) FROM People"},
		{"lowercase count col", "select count(name) from people"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseQuery(tc.query)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "only COUNT(*) is supported as the sole SELECT expression") {
				t.Errorf("error = %q, want it to contain 'only COUNT(*) is supported as the sole SELECT expression'", err.Error())
			}
		})
	}
}

// TestParseCountRejectsLimit verifies that LIMIT is not accepted on COUNT(*),
// including when a WHERE clause is present (the LIMIT must not be swallowed
// into the WHERE capture and reported as a generic WHERE error).
func TestParseCountRejectsLimit(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"limit no where", "SELECT COUNT(*) FROM People LIMIT 10"},
		{"limit with where", "SELECT COUNT(*) FROM People WHERE PartitionKey = 'p1' LIMIT 10"},
		{"limit with where placeholder", "SELECT COUNT(*) FROM People WHERE PartitionKey = ? LIMIT 5"},
		{"lowercase limit with where", "select count(*) from people where partitionkey = 'p' limit 10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseQuery(tc.query)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "COUNT(*) does not support LIMIT") {
				t.Errorf("error = %q, want it to contain 'COUNT(*) does not support LIMIT'", err.Error())
			}
		})
	}
}

// TestParseCountAcrossNewlines verifies that COUNT(*) statements split
// across multiple lines parse correctly (the (?is) dotall flag).
func TestParseCountAcrossNewlines(t *testing.T) {
	pq, err := parseQuery("SELECT COUNT(*)\nFROM People\nWHERE PartitionKey = ?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pq.kind != qCount {
		t.Fatalf("kind = %d, want qCount", pq.kind)
	}
	if pq.table != "People" {
		t.Errorf("table = %q, want %q", pq.table, "People")
	}
	if pq.numPlaceholders != 1 {
		t.Errorf("numPlaceholders = %d, want 1", pq.numPlaceholders)
	}
}

// TestParseCountLimitInLiteralNotSwallowed verifies that the word LIMIT
// inside a quoted WHERE literal is not mistaken for the real LIMIT clause
// — the $ anchor forces backtracking so the literal value is preserved and
// no LIMIT is detected. Mirrors TestParseSelectLimitDoesNotLeakIntoWhere.
func TestParseCountLimitInLiteralNotSwallowed(t *testing.T) {
	pq, err := parseQuery("SELECT COUNT(*) FROM T WHERE Note = 'LIMIT 10'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pq.kind != qCount {
		t.Fatalf("kind = %d, want qCount", pq.kind)
	}
	if len(pq.where) != 1 {
		t.Fatalf("expected 1 WHERE condition, got %d", len(pq.where))
	}
	if pq.where[0].value != "LIMIT 10" {
		t.Errorf("where[0].value = %q, want %q", pq.where[0].value, "LIMIT 10")
	}
}
