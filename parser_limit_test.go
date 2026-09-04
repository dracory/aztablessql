package aztablessql

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// LIMIT / TOP (Tier 2, item 4)
// ---------------------------------------------------------------------------

func TestParseSelectLimit(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantTable  string
		wantAll    bool
		wantCols   []string
		wantLimit  int
		wantN      int
		wantErr    bool
		wantErrSub string
	}{
		{
			name:      "limit with star",
			query:     "SELECT * FROM People LIMIT 50",
			wantTable: "People",
			wantAll:   true,
			wantLimit: 50,
			wantN:     0,
		},
		{
			name:      "limit with where and star",
			query:     "SELECT * FROM People WHERE PartitionKey = ? LIMIT 50",
			wantTable: "People",
			wantAll:   true,
			wantLimit: 50,
			wantN:     1,
		},
		{
			name:      "limit with explicit cols",
			query:     "SELECT Name, Age FROM People LIMIT 10",
			wantTable: "People",
			wantCols:  []string{"Name", "Age"},
			wantLimit: 10,
			wantN:     0,
		},
		{
			name:      "limit with where literals",
			query:     "SELECT * FROM People WHERE PartitionKey = 'pk' AND RowKey = 'rk' LIMIT 5",
			wantTable: "People",
			wantAll:   true,
			wantLimit: 5,
			wantN:     0,
		},
		{
			name:      "lowercase limit keyword",
			query:     "select * from people limit 7",
			wantTable: "people",
			wantAll:   true,
			wantLimit: 7,
			wantN:     0,
		},
		{
			name:      "limit with trailing semicolon",
			query:     "SELECT * FROM People LIMIT 10;",
			wantTable: "People",
			wantAll:   true,
			wantLimit: 10,
			wantN:     0,
		},
		{
			name:      "limit one",
			query:     "SELECT * FROM People LIMIT 1",
			wantTable: "People",
			wantAll:   true,
			wantLimit: 1,
			wantN:     0,
		},
		{
			name:      "no limit defaults to zero",
			query:     "SELECT * FROM People",
			wantTable: "People",
			wantAll:   true,
			wantLimit: 0,
			wantN:     0,
		},
		{
			name:       "limit zero rejected",
			query:      "SELECT * FROM People LIMIT 0",
			wantErr:    true,
			wantErrSub: "LIMIT must be ≥ 1",
		},
		{
			name:    "limit placeholder rejected",
			query:   "SELECT * FROM People LIMIT ?",
			wantErr: true,
		},
		{
			name:    "limit negative rejected by regex",
			query:   "SELECT * FROM People LIMIT -5",
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
					t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErrSub)
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
			if pq.limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", pq.limit, tc.wantLimit)
			}
			if pq.numPlaceholders != tc.wantN {
				t.Fatalf("numPlaceholders = %d, want %d", pq.numPlaceholders, tc.wantN)
			}
		})
	}
}

// TestParseSelectLimitDoesNotLeakIntoWhere verifies that the LIMIT keyword
// inside a quoted literal is not mistaken for the real LIMIT clause — both
// with and without a real trailing LIMIT. The regex's `$` anchor forces
// backtracking so the engine finds the correct (rightmost) LIMIT that
// satisfies the end-of-string constraint.
func TestParseSelectLimitDoesNotLeakIntoWhere(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantLimit int
		wantValue string // expected where[0].value
	}{
		{
			name:      "limit in literal, no real limit",
			query:     "SELECT * FROM T WHERE Note = 'LIMIT 10'",
			wantLimit: 0,
			wantValue: "LIMIT 10",
		},
		{
			name:      "limit in literal, with real limit",
			query:     "SELECT * FROM T WHERE Note = 'LIMIT 10' LIMIT 5",
			wantLimit: 5,
			wantValue: "LIMIT 10",
		},
		{
			name:      "two limits in literal, no real limit",
			query:     "SELECT * FROM T WHERE Note = 'LIMIT 10 LIMIT 5'",
			wantLimit: 0,
			wantValue: "LIMIT 10 LIMIT 5",
		},
		{
			name:      "two limits in literal, with real limit",
			query:     "SELECT * FROM T WHERE Note = 'LIMIT 10 LIMIT 5' LIMIT 3",
			wantLimit: 3,
			wantValue: "LIMIT 10 LIMIT 5",
		},
		{
			name:      "limit surrounded by text in literal, with real limit",
			query:     "SELECT * FROM T WHERE Note = 'x LIMIT 10 y' LIMIT 7",
			wantLimit: 7,
			wantValue: "x LIMIT 10 y",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pq, err := parseQuery(c.query)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pq.limit != c.wantLimit {
				t.Errorf("limit = %d, want %d", pq.limit, c.wantLimit)
			}
			if len(pq.where) != 1 {
				t.Fatalf("expected 1 WHERE condition, got %d", len(pq.where))
			}
			if pq.where[0].value != c.wantValue {
				t.Errorf("where[0].value = %q, want %q", pq.where[0].value, c.wantValue)
			}
		})
	}
}

// TestParseSelectLimitOverflow verifies that LIMIT values exceeding
// math.MaxInt32 are rejected at parse time, since the server-side $top
// parameter is an int32 and a silent wrap-around would ask the server for
// the wrong count.
func TestParseSelectLimitOverflow(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantErr    bool
		wantErrSub string
	}{
		{
			name:    "max int32 accepted",
			query:   fmt.Sprintf("SELECT * FROM T LIMIT %d", math.MaxInt32),
			wantErr: false,
		},
		{
			name:       "max int32 plus one rejected",
			query:      fmt.Sprintf("SELECT * FROM T LIMIT %d", math.MaxInt32+1),
			wantErr:    true,
			wantErrSub: "LIMIT must be ≤",
		},
		{
			name:       "very large value rejected",
			query:      "SELECT * FROM T LIMIT 9999999999",
			wantErr:    true,
			wantErrSub: "LIMIT must be ≤",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pq, err := parseQuery(c.query)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if c.wantErrSub != "" && !strings.Contains(err.Error(), c.wantErrSub) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), c.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pq.limit != math.MaxInt32 {
				t.Errorf("limit = %d, want %d", pq.limit, math.MaxInt32)
			}
		})
	}
}

// TestParseLimitRejectedOnNonSelect verifies that LIMIT is only accepted on
// SELECT. INSERT/UPDATE/DELETE don't have a LIMIT capture group in their
// regexes, so a trailing LIMIT makes the statement not match and is rejected.
func TestParseLimitRejectedOnNonSelect(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"insert with limit", "INSERT INTO T (A, B) VALUES (?, ?) LIMIT 5"},
		{"update with limit", "UPDATE T SET Name = ? WHERE PartitionKey = ? AND RowKey = ? LIMIT 5"},
		{"delete with limit", "DELETE FROM T WHERE PartitionKey = ? AND RowKey = ? LIMIT 5"},
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
