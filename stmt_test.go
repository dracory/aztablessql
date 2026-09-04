package aztablessql

import (
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

func TestNormalizeKeyName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"PartitionKey", "PartitionKey"},
		{"partitionkey", "PartitionKey"},
		{"PARTITIONKEY", "PartitionKey"},
		{"RowKey", "RowKey"},
		{"rowkey", "RowKey"},
		{"MyCol", "MyCol"},
		{"mycol", "mycol"},
	}
	for _, c := range cases {
		if got := normalizeKeyName(c.in); got != c.want {
			t.Errorf("normalizeKeyName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatODataPredicate(t *testing.T) {
	cases := []struct {
		name string
		col  string
		val  interface{}
		want string
	}{
		{"string", "Name", "Ada", "Name eq 'Ada'"},
		{"string with quote", "Name", "O'Brien", "Name eq 'O''Brien'"},
		{"bool true", "Active", true, "Active eq true"},
		{"bool false", "Active", false, "Active eq false"},
		{"int", "Age", int(36), "Age eq 36"},
		{"int64", "Age", int64(36), "Age eq 36"},
		{"float whole", "Age", float64(36), "Age eq 36"},
		{"float fractional", "Score", float64(3.14), "Score eq 3.14"},
		{"nil", "Optional", nil, "Optional eq null"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatODataPredicate(c.col, c.val); got != c.want {
				t.Errorf("formatODataPredicate(%q, %v) = %q, want %q", c.col, c.val, got, c.want)
			}
		})
	}
}

func TestBuildODataFilterPreservesCase(t *testing.T) {
	conds := []resolvedCond{
		{column: "MyCol", value: "x"},
		{column: "partitionkey", value: "pk"},
		{column: "rowkey", value: "rk"},
	}
	got := buildODataFilter(conds)
	for _, want := range []string{"MyCol eq 'x'", "PartitionKey eq 'pk'", "RowKey eq 'rk'"} {
		if !strings.Contains(got, want) {
			t.Errorf("buildODataFilter = %q, missing part %q", got, want)
		}
	}
}

func TestFindKeyValue(t *testing.T) {
	conds := []resolvedCond{
		{column: "MyCol", value: "x"},
		{column: "PartitionKey", value: "pk"},
		{column: "RowKey", value: 42},
	}
	if v, ok := findKeyValue(conds, "PartitionKey"); !ok || v != "pk" {
		t.Errorf("findKeyValue(PartitionKey) = (%q, %v), want (\"pk\", true)", v, ok)
	}
	if v, ok := findKeyValue(conds, "partitionkey"); !ok || v != "pk" {
		t.Errorf("findKeyValue(partitionkey) = (%q, %v), want (\"pk\", true)", v, ok)
	}
	if v, ok := findKeyValue(conds, "RowKey"); !ok || v != "42" {
		t.Errorf("findKeyValue(RowKey) = (%q, %v), want (\"42\", true)", v, ok)
	}
	if _, ok := findKeyValue(conds, "Missing"); ok {
		t.Errorf("findKeyValue(Missing) should return false")
	}
}

func TestIsNotFound(t *testing.T) {
	if isNotFound(nil) {
		t.Error("isNotFound(nil) should be false")
	}
}

func TestResolveWherePreservesCase(t *testing.T) {
	where := []whereCond{
		{column: "MyCol", isPlaceholder: true},
		{column: "PartitionKey", isPlaceholder: true},
	}
	args := []driver.Value{"x", "pk"}
	conds, err := resolveWhere(where, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conds) != 2 {
		t.Fatalf("expected 2 conds, got %d", len(conds))
	}
	if conds[0].column != "MyCol" {
		t.Errorf("conds[0].column = %q, want %q", conds[0].column, "MyCol")
	}
	if conds[1].column != "PartitionKey" {
		t.Errorf("conds[1].column = %q, want %q", conds[1].column, "PartitionKey")
	}
}

func TestResolveWhereTooFewArgs(t *testing.T) {
	where := []whereCond{
		{column: "A", isPlaceholder: true},
		{column: "B", isPlaceholder: true},
	}
	if _, err := resolveWhere(where, []driver.Value{"only-one"}); err == nil {
		t.Error("expected error for too few args, got nil")
	}
}

func TestDriverRegistered(t *testing.T) {
	for _, n := range sql.Drivers() {
		if n == "aztables" {
			return
		}
	}
	t.Error("driver \"aztables\" is not registered with database/sql")
}
