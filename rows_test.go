package aztablessql

import (
	"encoding/json"
	"testing"
)

// TestResolveColumnValueETag verifies that the ETag pseudo-column is resolved
// from the "odata.etag" JSON field, not from the regular properties map.
func TestResolveColumnValueETag(t *testing.T) {
	raw := `{"PartitionKey":"pk","RowKey":"rk","Name":"Ada","odata.etag":"W/\"0xABC\""}`
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := resolveColumnValue("ETag", m)
	if got != `W/"0xABC"` {
		t.Errorf("resolveColumnValue(ETag) = %v, want %q", got, `W/"0xABC"`)
	}

	// Case-insensitive column name.
	got = resolveColumnValue("etag", m)
	if got != `W/"0xABC"` {
		t.Errorf("resolveColumnValue(etag) = %v, want %q", got, `W/"0xABC"`)
	}
}

// TestResolveColumnValueTimestamp verifies that the Timestamp pseudo-column
// is resolved from the entity's Timestamp JSON field (case-insensitive).
func TestResolveColumnValueTimestamp(t *testing.T) {
	raw := `{"PartitionKey":"pk","RowKey":"rk","Timestamp":"2026-09-04T12:00:00Z","odata.etag":"W/\"0x\""}`
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := resolveColumnValue("Timestamp", m)
	if got != "2026-09-04T12:00:00Z" {
		t.Errorf("resolveColumnValue(Timestamp) = %v, want %q", got, "2026-09-04T12:00:00Z")
	}

	// Case-insensitive column name lookup.
	got = resolveColumnValue("timestamp", m)
	if got != "2026-09-04T12:00:00Z" {
		t.Errorf("resolveColumnValue(timestamp) = %v, want %q", got, "2026-09-04T12:00:00Z")
	}
}

// TestResolveColumnValueRegularProperty verifies that regular property names
// pass through unchanged (case-sensitive, matching Azure Table Storage
// property-name semantics).
func TestResolveColumnValueRegularProperty(t *testing.T) {
	raw := `{"PartitionKey":"pk","RowKey":"rk","MyCol":"value"}`
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := resolveColumnValue("MyCol", m)
	if got != "value" {
		t.Errorf("resolveColumnValue(MyCol) = %v, want %q", got, "value")
	}

	// Case-sensitive: "mycol" should not match "MyCol".
	got = resolveColumnValue("mycol", m)
	if got != nil {
		t.Errorf("resolveColumnValue(mycol) = %v, want nil (case-sensitive)", got)
	}
}

// TestResolveColumnValueMissingETag verifies that a missing odata.etag field
// returns nil rather than panicking.
func TestResolveColumnValueMissingETag(t *testing.T) {
	raw := `{"PartitionKey":"pk","RowKey":"rk","Name":"Ada"}`
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := resolveColumnValue("ETag", m)
	if got != nil {
		t.Errorf("resolveColumnValue(ETag) with no odata.etag = %v, want nil", got)
	}
}

// TestCollectColsSurfacesETag verifies that collectCols surfaces "ETag" as a
// synthetic column when the entity JSON contains an "odata.etag" field.
func TestCollectColsSurfacesETag(t *testing.T) {
	raw := `{"PartitionKey":"pk","RowKey":"rk","Name":"Ada","odata.etag":"W/\"0x\""}`
	r := &Rows{}
	seen := map[string]bool{}
	var cols []string
	r.collectCols([]byte(raw), seen, &cols)

	foundETag := false
	for _, c := range cols {
		if c == "ETag" {
			foundETag = true
			break
		}
	}
	if !foundETag {
		t.Errorf("collectCols did not surface ETag column; got %v", cols)
	}

	// The raw "odata.etag" key must NOT appear as a column.
	for _, c := range cols {
		if c == "odata.etag" {
			t.Errorf("collectCols leaked raw 'odata.etag' key as a column; got %v", cols)
		}
	}
}
