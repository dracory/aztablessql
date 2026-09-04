package aztablessql

import (
	"strings"
	"testing"
)

func TestParseInsert(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantTable  string
		wantCols   []string
		wantN      int
		wantUpsert upsertMode
		wantErr    bool
	}{
		{
			name:       "basic",
			query:      "INSERT INTO People (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)",
			wantTable:  "People",
			wantCols:   []string{"PartitionKey", "RowKey", "Name", "Age"},
			wantN:      4,
			wantUpsert: upsertNone,
		},
		{
			name:       "trailing semicolon",
			query:      "INSERT INTO T (A, B) VALUES (?, ?);",
			wantTable:  "T",
			wantCols:   []string{"A", "B"},
			wantN:      2,
			wantUpsert: upsertNone,
		},
		{
			name:       "lowercase keywords",
			query:      "insert into t (a, b) values (?, ?)",
			wantTable:  "t",
			wantCols:   []string{"a", "b"},
			wantN:      2,
			wantUpsert: upsertNone,
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
			if pq.upsert != tc.wantUpsert {
				t.Fatalf("upsert = %d, want %d", pq.upsert, tc.wantUpsert)
			}
		})
	}
}

func TestParseUpsert(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantTable  string
		wantCols   []string
		wantN      int
		wantUpsert upsertMode
		wantErr    bool
	}{
		{
			name:       "insert or replace",
			query:      "INSERT OR REPLACE INTO People (PartitionKey, RowKey, Name) VALUES (?, ?, ?)",
			wantTable:  "People",
			wantCols:   []string{"PartitionKey", "RowKey", "Name"},
			wantN:      3,
			wantUpsert: upsertReplace,
		},
		{
			name:       "insert or merge",
			query:      "INSERT OR MERGE INTO People (PartitionKey, RowKey, Name) VALUES (?, ?, ?)",
			wantTable:  "People",
			wantCols:   []string{"PartitionKey", "RowKey", "Name"},
			wantN:      3,
			wantUpsert: upsertMerge,
		},
		{
			name:       "upsert into defaults to replace",
			query:      "UPSERT INTO People (PartitionKey, RowKey, Name) VALUES (?, ?, ?)",
			wantTable:  "People",
			wantCols:   []string{"PartitionKey", "RowKey", "Name"},
			wantN:      3,
			wantUpsert: upsertReplace,
		},
		{
			name:       "lowercase insert or replace",
			query:      "insert or replace into t (a, b) values (?, ?)",
			wantTable:  "t",
			wantCols:   []string{"a", "b"},
			wantN:      2,
			wantUpsert: upsertReplace,
		},
		{
			name:       "lowercase insert or merge",
			query:      "insert or merge into t (a, b) values (?, ?)",
			wantTable:  "t",
			wantCols:   []string{"a", "b"},
			wantN:      2,
			wantUpsert: upsertMerge,
		},
		{
			name:       "lowercase upsert into",
			query:      "upsert into t (a, b) values (?, ?)",
			wantTable:  "t",
			wantCols:   []string{"a", "b"},
			wantN:      2,
			wantUpsert: upsertReplace,
		},
		{
			name:       "trailing semicolon",
			query:      "UPSERT INTO T (A, B) VALUES (?, ?);",
			wantTable:  "T",
			wantCols:   []string{"A", "B"},
			wantN:      2,
			wantUpsert: upsertReplace,
		},
		{
			name:    "upsert literal value rejected",
			query:   "UPSERT INTO T (A, B) VALUES (?, 'x')",
			wantErr: true,
		},
		{
			name:    "upsert col/val count mismatch",
			query:   "INSERT OR REPLACE INTO T (A, B, C) VALUES (?, ?)",
			wantErr: true,
		},
		{
			name:    "quoted placeholder rejected",
			query:   "UPSERT INTO T (A, B) VALUES (?, '?')",
			wantErr: true,
		},
		{
			name:    "double-quoted placeholder rejected",
			query:   `INSERT INTO T (A, B) VALUES ("?", ?)`,
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
			if pq.upsert != tc.wantUpsert {
				t.Fatalf("upsert = %d, want %d", pq.upsert, tc.wantUpsert)
			}
		})
	}
}

// TestParseInsertErrorMessageLabel verifies that UPSERT statements report
// "UPSERT" (not "INSERT") in validation error messages.
func TestParseInsertErrorMessageLabel(t *testing.T) {
	_, err := parseQuery("UPSERT INTO T (A, B) VALUES (?, 'x')")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "UPSERT") {
		t.Errorf("error = %q, want it to contain 'UPSERT'", err.Error())
	}
	if strings.Contains(err.Error(), "INSERT only") {
		t.Errorf("error = %q, should not say 'INSERT only' for a UPSERT statement", err.Error())
	}

	_, err = parseQuery("INSERT INTO T (A, B) VALUES (?, 'x')")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "INSERT") {
		t.Errorf("error = %q, want it to contain 'INSERT'", err.Error())
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

func TestParseUpdate(t *testing.T) {
	cases := []struct {
		name        string
		query       string
		wantTable   string
		wantSetCols []string
		wantSetN    int
		wantWhereN  int
		wantTotalN  int
		wantErr     bool
	}{
		{
			name:        "basic with placeholders",
			query:       "UPDATE People SET Name = ?, Age = ? WHERE PartitionKey = ? AND RowKey = ?",
			wantTable:   "People",
			wantSetCols: []string{"Name", "Age"},
			wantSetN:    2,
			wantWhereN:  2,
			wantTotalN:  4,
		},
		{
			name:        "set literal values",
			query:       "UPDATE People SET Name = 'Bob', Age = '42' WHERE PartitionKey = ? AND RowKey = ?",
			wantTable:   "People",
			wantSetCols: []string{"Name", "Age"},
			wantSetN:    0,
			wantWhereN:  2,
			wantTotalN:  2,
		},
		{
			name:        "trailing semicolon",
			query:       "UPDATE T SET A = ? WHERE PartitionKey = ? AND RowKey = ?;",
			wantTable:   "T",
			wantSetCols: []string{"A"},
			wantSetN:    1,
			wantWhereN:  2,
			wantTotalN:  3,
		},
		{
			name:        "lowercase keywords",
			query:       "update t set a = ? where partitionkey = ? and rowkey = ?",
			wantTable:   "t",
			wantSetCols: []string{"a"},
			wantSetN:    1,
			wantWhereN:  2,
			wantTotalN:  3,
		},
		{
			name:    "set partitionkey rejected",
			query:   "UPDATE People SET PartitionKey = ? WHERE PartitionKey = ? AND RowKey = ?",
			wantErr: true,
		},
		{
			name:    "set rowkey rejected",
			query:   "UPDATE People SET RowKey = ? WHERE PartitionKey = ? AND RowKey = ?",
			wantErr: true,
		},
		{
			name:    "missing where rejected",
			query:   "UPDATE People SET Name = ?",
			wantErr: true,
		},
		{
			name:    "extra where condition rejected",
			query:   "UPDATE People SET Name = ? WHERE PartitionKey = ? AND RowKey = ? AND Age = ?",
			wantErr: true,
		},
		{
			name:    "where missing rowkey rejected",
			query:   "UPDATE People SET Name = ? WHERE PartitionKey = ?",
			wantErr: true,
		},
		{
			name:    "empty set rejected",
			query:   "UPDATE People SET WHERE PartitionKey = ? AND RowKey = ?",
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
			if pq.kind != qUpdate {
				t.Fatalf("kind = %d, want qUpdate", pq.kind)
			}
			if pq.table != tc.wantTable {
				t.Fatalf("table = %q, want %q", pq.table, tc.wantTable)
			}
			if len(pq.set) != len(tc.wantSetCols) {
				t.Fatalf("set columns = %v, want %v", pq.set, tc.wantSetCols)
			}
			for i, a := range pq.set {
				if a.column != tc.wantSetCols[i] {
					t.Fatalf("set[%d].column = %q, want %q", i, a.column, tc.wantSetCols[i])
				}
			}
			if pq.setPlaceholders != tc.wantSetN {
				t.Fatalf("setPlaceholders = %d, want %d", pq.setPlaceholders, tc.wantSetN)
			}
			if pq.wherePlaceholders != tc.wantWhereN {
				t.Fatalf("wherePlaceholders = %d, want %d", pq.wherePlaceholders, tc.wantWhereN)
			}
			if pq.numPlaceholders != tc.wantTotalN {
				t.Fatalf("numPlaceholders = %d, want %d", pq.numPlaceholders, tc.wantTotalN)
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

func TestParseUpdateWithoutWhereGivesClearError(t *testing.T) {
	_, err := parseQuery("UPDATE People SET Name = ?")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "UPDATE requires WHERE") {
		t.Errorf("error = %q, want it to contain 'UPDATE requires WHERE'", err.Error())
	}
}

func TestParseSetWithCommaInLiteral(t *testing.T) {
	pq, err := parseQuery("UPDATE People SET Name = 'Doe, Jr', Age = ? WHERE PartitionKey = ? AND RowKey = ?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.set) != 2 {
		t.Fatalf("expected 2 SET assignments, got %d", len(pq.set))
	}
	if pq.set[0].column != "Name" || pq.set[0].value != "Doe, Jr" {
		t.Errorf("set[0] = {col: %q, val: %q}, want {col: \"Name\", val: \"Doe, Jr\"}", pq.set[0].column, pq.set[0].value)
	}
	if pq.set[1].column != "Age" || !pq.set[1].isPlaceholder {
		t.Errorf("set[1] = {col: %q, placeholder: %v}, want {col: \"Age\", placeholder: true}", pq.set[1].column, pq.set[1].isPlaceholder)
	}
}

func TestParseWhereWithANDInLiteral(t *testing.T) {
	pq, err := parseQuery("SELECT * FROM T WHERE Note = 'A and B' AND PartitionKey = ?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.where) != 2 {
		t.Fatalf("expected 2 WHERE conditions, got %d", len(pq.where))
	}
	if pq.where[0].column != "Note" || pq.where[0].value != "A and B" {
		t.Errorf("where[0] = {col: %q, val: %q}, want {col: \"Note\", val: \"A and B\"}", pq.where[0].column, pq.where[0].value)
	}
	if pq.where[1].column != "PartitionKey" || !pq.where[1].isPlaceholder {
		t.Errorf("where[1] = {col: %q, placeholder: %v}, want {col: \"PartitionKey\", placeholder: true}", pq.where[1].column, pq.where[1].isPlaceholder)
	}
}

func TestParseSetWithCommaInDoubleQuotedLiteral(t *testing.T) {
	pq, err := parseQuery(`UPDATE People SET Name = "Doe, Jr", Age = ? WHERE PartitionKey = ? AND RowKey = ?`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.set) != 2 {
		t.Fatalf("expected 2 SET assignments, got %d", len(pq.set))
	}
	if pq.set[0].column != "Name" || pq.set[0].value != "Doe, Jr" {
		t.Errorf("set[0] = {col: %q, val: %q}, want {col: \"Name\", val: \"Doe, Jr\"}", pq.set[0].column, pq.set[0].value)
	}
}

func TestParseWhereWithANDInDoubleQuotedLiteral(t *testing.T) {
	pq, err := parseQuery(`SELECT * FROM T WHERE Note = "A and B" AND PartitionKey = ?`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.where) != 2 {
		t.Fatalf("expected 2 WHERE conditions, got %d", len(pq.where))
	}
	if pq.where[0].column != "Note" || pq.where[0].value != "A and B" {
		t.Errorf("where[0] = {col: %q, val: %q}, want {col: \"Note\", val: \"A and B\"}", pq.where[0].column, pq.where[0].value)
	}
}

func TestFindUnquotedKeyword(t *testing.T) {
	cases := []struct {
		s       string
		keyword string
		want    bool // true = found (pos >= 0)
	}{
		{"Name = 'x' WHERE", "WHERE", true},
		{"Name = 'WHERE clause'", "WHERE", false},
		{`Name = "WHERE clause"`, "WHERE", false},
		{"Name = 'x'", "WHERE", false},
		{"WHERE x = 1", "WHERE", true},
		{"Name = 'x' where y = 1", "WHERE", true},
		{`Name = "x and where y"`, "WHERE", false},
		{"Name = 'x'\nWHERE y = 1", "WHERE", true},
		{"Name = 'x'\r\nWHERE y = 1", "WHERE", true},
		{"Name = 'x'\tWHERE y = 1", "WHERE", true},
	}
	for _, c := range cases {
		pos := findUnquotedKeyword(c.s, c.keyword)
		got := pos >= 0
		if got != c.want {
			t.Errorf("findUnquotedKeyword(%q, %q) pos=%d, want found=%v", c.s, c.keyword, pos, c.want)
		}
	}
}

func TestParseUpdateWithWhereInQuotedSet(t *testing.T) {
	// The word WHERE inside a double-quoted literal in the SET clause
	// must not be mistaken for the real WHERE keyword.
	pq, err := parseQuery(`UPDATE People SET Name = "x WHERE y" WHERE PartitionKey = ? AND RowKey = ?`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.set) != 1 || pq.set[0].column != "Name" || pq.set[0].value != "x WHERE y" {
		t.Errorf("set = %v, want [{Name x WHERE y}]", pq.set)
	}
	if len(pq.where) != 2 {
		t.Errorf("expected 2 WHERE conditions, got %d", len(pq.where))
	}
}

func TestParseUpdateWithWhereInSingleQuotedSet(t *testing.T) {
	pq, err := parseQuery(`UPDATE People SET Name = 'x WHERE y' WHERE PartitionKey = ? AND RowKey = ?`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.set) != 1 || pq.set[0].column != "Name" || pq.set[0].value != "x WHERE y" {
		t.Errorf("set = %v, want [{Name x WHERE y}]", pq.set)
	}
}

func TestUnquoteLiteralPreservesInnerQuotes(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`'He said "hi"'`, `He said "hi"`},
		{`"He said 'hi'"`, `He said 'hi'`},
		{`'It''s'`, `It's`},
		{`"She said ""hi"""`, `She said "hi"`},
		{`'simple'`, `simple`},
		{`"simple"`, `simple`},
		{`?`, `?`},
	}
	for _, c := range cases {
		if got := unquoteLiteral(c.input); got != c.want {
			t.Errorf("unquoteLiteral(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestParseSetWithDoubledQuoteEscape(t *testing.T) {
	pq, err := parseQuery(`UPDATE People SET Name = 'It''s', Age = ? WHERE PartitionKey = ? AND RowKey = ?`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.set) != 2 {
		t.Fatalf("expected 2 SET assignments, got %d", len(pq.set))
	}
	if pq.set[0].value != "It's" {
		t.Errorf("set[0].value = %q, want %q", pq.set[0].value, "It's")
	}
}

func TestParseWhereWithDoubledQuoteEscape(t *testing.T) {
	pq, err := parseQuery(`SELECT * FROM T WHERE Note = 'It''s a test' AND PartitionKey = ?`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.where) != 2 {
		t.Fatalf("expected 2 WHERE conditions, got %d", len(pq.where))
	}
	if pq.where[0].value != "It's a test" {
		t.Errorf("where[0].value = %q, want %q", pq.where[0].value, "It's a test")
	}
}

func TestParseWhereWithTabSeparatedAND(t *testing.T) {
	pq, err := parseQuery("SELECT * FROM T WHERE Note = 'A and B'\tAND PartitionKey = ?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.where) != 2 {
		t.Fatalf("expected 2 WHERE conditions, got %d", len(pq.where))
	}
	if pq.where[0].value != "A and B" {
		t.Errorf("where[0].value = %q, want %q", pq.where[0].value, "A and B")
	}
}

func TestParseUpdateWithNewlineBeforeWhere(t *testing.T) {
	pq, err := parseQuery("UPDATE People SET Name = ?\nWHERE PartitionKey = ? AND RowKey = ?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pq.set) != 1 || pq.set[0].column != "Name" {
		t.Errorf("set = %v, want [{Name}]", pq.set)
	}
	if len(pq.where) != 2 {
		t.Errorf("expected 2 WHERE conditions, got %d", len(pq.where))
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
