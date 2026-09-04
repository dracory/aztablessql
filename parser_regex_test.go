package aztablessql

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Regex flag and boundary-condition coverage
// ---------------------------------------------------------------------------

// TestParseNewlinesAcrossStatements verifies that the (?is) dotall flag on
// every top-level regex makes "." match newlines, so statements split across
// multiple lines parse correctly. Only UPDATE had newline coverage before.
func TestParseNewlinesAcrossStatements(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantKind  queryType
		wantTable string
	}{
		{
			name:      "select across newlines",
			query:     "SELECT *\nFROM People\nWHERE PartitionKey = ?\nAND RowKey = ?",
			wantKind:  qSelect,
			wantTable: "People",
		},
		{
			name:      "insert across newlines",
			query:     "INSERT INTO People\n(PartitionKey, RowKey, Name)\nVALUES (?, ?, ?)",
			wantKind:  qInsert,
			wantTable: "People",
		},
		{
			name:      "upsert across newlines",
			query:     "INSERT OR REPLACE INTO People\n(PartitionKey, RowKey, Name)\nVALUES (?, ?, ?)",
			wantKind:  qInsert,
			wantTable: "People",
		},
		{
			name:      "delete across newlines",
			query:     "DELETE FROM People\nWHERE PartitionKey = ?\nAND RowKey = ?",
			wantKind:  qDelete,
			wantTable: "People",
		},
		{
			name:      "select with carriage returns",
			query:     "SELECT *\r\nFROM People\r\nWHERE PartitionKey = ?",
			wantKind:  qSelect,
			wantTable: "People",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pq, err := parseQuery(tc.query)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pq.kind != tc.wantKind {
				t.Errorf("kind = %d, want %d", pq.kind, tc.wantKind)
			}
			if pq.table != tc.wantTable {
				t.Errorf("table = %q, want %q", pq.table, tc.wantTable)
			}
		})
	}
}

// TestParseTrailingSemicolons verifies that the optional ;? at the end of
// each regex is exercised for SELECT and DELETE (INSERT/UPSERT/UPDATE
// already have semicolon tests).
func TestParseTrailingSemicolons(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantKind  queryType
		wantTable string
	}{
		{"select with semicolon", "SELECT * FROM People;", qSelect, "People"},
		{"select with semicolon and where", "SELECT * FROM People WHERE PartitionKey = ?;", qSelect, "People"},
		{"select with semicolon and where literal", "SELECT * FROM People WHERE PartitionKey = 'pk';", qSelect, "People"},
		{"delete with semicolon", "DELETE FROM People WHERE PartitionKey = ? AND RowKey = ?;", qDelete, "People"},
		{"select no semicolon", "SELECT * FROM People", qSelect, "People"},
		{"delete no semicolon", "DELETE FROM People WHERE PartitionKey = ? AND RowKey = ?", qDelete, "People"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pq, err := parseQuery(tc.query)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pq.kind != tc.wantKind {
				t.Errorf("kind = %d, want %d", pq.kind, tc.wantKind)
			}
			if pq.table != tc.wantTable {
				t.Errorf("table = %q, want %q", pq.table, tc.wantTable)
			}
		})
	}
}

// TestParseExtraWhitespaceBetweenKeywords verifies that \s+ in the regexes
// tolerates multiple spaces / tabs between keywords.
func TestParseExtraWhitespaceBetweenKeywords(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantKind  queryType
		wantTable string
	}{
		{"select double spaces", "SELECT  *  FROM  People  WHERE  PartitionKey = ?", qSelect, "People"},
		{"select tabs", "SELECT\t*\tFROM\tPeople\tWHERE\tPartitionKey = ?", qSelect, "People"},
		{"insert double spaces", "INSERT  INTO  People  (A, B)  VALUES  (?, ?)", qInsert, "People"},
		{"upsert double spaces", "INSERT  OR  REPLACE  INTO  People  (A, B)  VALUES  (?, ?)", qInsert, "People"},
		{"delete double spaces", "DELETE  FROM  People  WHERE  PartitionKey = ?  AND  RowKey = ?", qDelete, "People"},
		{"update double spaces", "UPDATE  People  SET  Name = ?  WHERE  PartitionKey = ?  AND  RowKey = ?", qUpdate, "People"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pq, err := parseQuery(tc.query)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pq.kind != tc.wantKind {
				t.Errorf("kind = %d, want %d", pq.kind, tc.wantKind)
			}
			if pq.table != tc.wantTable {
				t.Errorf("table = %q, want %q", pq.table, tc.wantTable)
			}
		})
	}
}

// TestParseEmptyColumnValueLists verifies that empty () in INSERT/UPSERT
// is rejected (the [^)]+ group requires at least one character).
func TestParseEmptyColumnValueLists(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"insert empty cols", "INSERT INTO T () VALUES (?)"},
		{"insert empty vals", "INSERT INTO T (A) VALUES ()"},
		{"insert both empty", "INSERT INTO T () VALUES ()"},
		{"upsert empty cols", "UPSERT INTO T () VALUES (?)"},
		{"upsert empty vals", "UPSERT INTO T (A) VALUES ()"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseQuery(tc.query)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.query)
			}
		})
	}
}

// TestParseSelectSemicolonWithWhereInteraction verifies the non-greedy
// WHERE capture group (.+?) interacts correctly with the trailing ;?\s*$
// — the semicolon must not be captured as part of the WHERE clause.
func TestParseSelectSemicolonWithWhereInteraction(t *testing.T) {
	pq, err := parseQuery("SELECT * FROM T WHERE PartitionKey = 'pk' AND RowKey = 'rk';")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.where) != 2 {
		t.Fatalf("expected 2 WHERE conditions, got %d (semicolon may have leaked into capture)", len(pq.where))
	}
	if pq.where[0].column != "PartitionKey" || pq.where[0].value != "pk" {
		t.Errorf("where[0] = {col: %q, val: %q}, want {PartitionKey, pk}", pq.where[0].column, pq.where[0].value)
	}
	if pq.where[1].column != "RowKey" || pq.where[1].value != "rk" {
		t.Errorf("where[1] = {col: %q, val: %q}, want {RowKey, rk}", pq.where[1].column, pq.where[1].value)
	}
}

// TestParseDegenerateCondReInputs verifies that condRe rejects malformed
// conditions that are not valid column-operator-value triples.
func TestParseDegenerateCondReInputs(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"missing column", "SELECT * FROM T WHERE = ?"},
		{"missing value", "SELECT * FROM T WHERE Name ="},
		{"missing both", "SELECT * FROM T WHERE ="},
		{"operator only", "SELECT * FROM T WHERE >"},
		{"column only", "SELECT * FROM T WHERE Name"},
		{"empty where", "SELECT * FROM T WHERE "},
		{"double operator", "SELECT * FROM T WHERE Name == ?"},
		{"triple operator", "SELECT * FROM T WHERE Name === 'x'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseQuery(tc.query)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.query)
			}
		})
	}
}

// TestParseCondReOperatorWhitespace verifies that whitespace around the
// operator is tolerated (\s* in condRe).
func TestParseCondReOperatorWhitespace(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"no spaces around =", "SELECT * FROM T WHERE Name='Bob'", "Bob"},
		{"space before =", "SELECT * FROM T WHERE Name = 'Bob'", "Bob"},
		{"space after =", "SELECT * FROM T WHERE Name= 'Bob'", "Bob"},
		{"spaces around >", "SELECT * FROM T WHERE Age > '30'", "30"},
		{"no spaces around >=", "SELECT * FROM T WHERE Age>='30'", "30"},
		{"tab around !=", "SELECT * FROM T WHERE Name!=\t'Bob'", "Bob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pq, err := parseQuery(tc.query)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(pq.where) != 1 {
				t.Fatalf("expected 1 condition, got %d", len(pq.where))
			}
			if pq.where[0].value != tc.want {
				t.Errorf("where[0].value = %q, want %q", pq.where[0].value, tc.want)
			}
		})
	}
}
