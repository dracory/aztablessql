package aztablessql

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ETag-based optimistic concurrency (Tier 1, item 3)
// ---------------------------------------------------------------------------

func TestParseUpdateWithETag(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantSetN   int
		wantWhereN int
		wantTotalN int
		wantErr    bool
		wantErrSub string
	}{
		{
			name:       "update with etag placeholder",
			query:      "UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND ETag = ?",
			wantSetN:   1,
			wantWhereN: 3,
			wantTotalN: 4,
		},
		{
			name:       "update with etag literal",
			query:      `UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND ETag = 'W/"0xABC"'`,
			wantSetN:   1,
			wantWhereN: 2,
			wantTotalN: 3,
		},
		{
			name:       "update with etag star literal",
			query:      "UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND ETag = '*'",
			wantSetN:   1,
			wantWhereN: 2,
			wantTotalN: 3,
		},
		{
			name:       "update with lowercase etag",
			query:      "UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND etag = ?",
			wantSetN:   1,
			wantWhereN: 3,
			wantTotalN: 4,
		},
		{
			name:       "update without etag still works",
			query:      "UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ?",
			wantSetN:   1,
			wantWhereN: 2,
			wantTotalN: 3,
		},
		{
			name:       "update with etag greater than rejected",
			query:      "UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND ETag > ?",
			wantErr:    true,
			wantErrSub: "ETag condition only supports =",
		},
		{
			name:       "update with non-etag third cond rejected",
			query:      "UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND Age = ?",
			wantErr:    true,
			wantErrSub: "only supports PartitionKey, RowKey and ETag",
		},
		{
			name:       "update with two etag conds rejected",
			query:      "UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND ETag = ? AND ETag = ?",
			wantErr:    true,
			wantErrSub: "only one ETag condition",
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
			if pq.kind != qUpdate {
				t.Fatalf("kind = %d, want qUpdate", pq.kind)
			}
			if pq.setPlaceholders != tc.wantSetN {
				t.Errorf("setPlaceholders = %d, want %d", pq.setPlaceholders, tc.wantSetN)
			}
			if pq.wherePlaceholders != tc.wantWhereN {
				t.Errorf("wherePlaceholders = %d, want %d", pq.wherePlaceholders, tc.wantWhereN)
			}
			if pq.numPlaceholders != tc.wantTotalN {
				t.Errorf("numPlaceholders = %d, want %d", pq.numPlaceholders, tc.wantTotalN)
			}
		})
	}
}

func TestParseDeleteWithETag(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantN      int
		wantErr    bool
		wantErrSub string
	}{
		{
			name:  "delete with etag placeholder",
			query: "DELETE FROM People WHERE PartitionKey = ? AND RowKey = ? AND ETag = ?",
			wantN: 3,
		},
		{
			name:  "delete with etag literal",
			query: `DELETE FROM People WHERE PartitionKey = 'pk' AND RowKey = 'rk' AND ETag = 'W/"0xABC"'`,
			wantN: 0,
		},
		{
			name:  "delete with etag star",
			query: "DELETE FROM People WHERE PartitionKey = 'pk' AND RowKey = 'rk' AND ETag = '*'",
			wantN: 0,
		},
		{
			name:  "delete without etag still works",
			query: "DELETE FROM People WHERE PartitionKey = ? AND RowKey = ?",
			wantN: 2,
		},
		{
			name:       "delete with etag greater than rejected",
			query:      "DELETE FROM People WHERE PartitionKey = ? AND RowKey = ? AND ETag > ?",
			wantErr:    true,
			wantErrSub: "ETag condition only supports =",
		},
		{
			name:       "delete with non-etag third cond rejected",
			query:      "DELETE FROM People WHERE PartitionKey = ? AND RowKey = ? AND Age = ?",
			wantErr:    true,
			wantErrSub: "only supports PartitionKey, RowKey and ETag",
		},
		{
			name:       "delete with two etag conds rejected",
			query:      "DELETE FROM People WHERE PartitionKey = ? AND RowKey = ? AND ETag = ? AND ETag = ?",
			wantErr:    true,
			wantErrSub: "only one ETag condition",
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
			if pq.kind != qDelete {
				t.Fatalf("kind = %d, want qDelete", pq.kind)
			}
			if pq.numPlaceholders != tc.wantN {
				t.Errorf("numPlaceholders = %d, want %d", pq.numPlaceholders, tc.wantN)
			}
		})
	}
}

func TestParseSetRejectsTimestampAndETag(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"set timestamp", "UPDATE People SET Timestamp = ? WHERE PartitionKey = ? AND RowKey = ?"},
		{"set etag", "UPDATE People SET ETag = ? WHERE PartitionKey = ? AND RowKey = ?"},
		{"set lowercase timestamp", "UPDATE People SET timestamp = ? WHERE PartitionKey = ? AND RowKey = ?"},
		{"set lowercase etag", "UPDATE People SET etag = ? WHERE PartitionKey = ? AND RowKey = ?"},
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

// TestParseSelectRejectsETagAndTimestampInWhere verifies that ETag and
// Timestamp are rejected in SELECT WHERE clauses — they are read-only
// pseudo-columns, not stored OData-queryable properties.
func TestParseSelectRejectsETagAndTimestampInWhere(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"etag in where", "SELECT * FROM T WHERE ETag = ?"},
		{"timestamp in where", "SELECT * FROM T WHERE Timestamp = ?"},
		{"etag lowercase in where", "SELECT * FROM T WHERE etag = ?"},
		{"timestamp lowercase in where", "SELECT * FROM T WHERE timestamp = ?"},
		{"etag with literal", `SELECT * FROM T WHERE ETag = 'W/"0x"'`},
		{"etag among other conds", "SELECT * FROM T WHERE PartitionKey = ? AND ETag = ?"},
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
			if !strings.Contains(err.Error(), "cannot be used in a WHERE filter") {
				t.Errorf("error = %q, want it to contain 'cannot be used in a WHERE filter'", err.Error())
			}
		})
	}
}
