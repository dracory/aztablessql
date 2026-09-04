package aztablessql

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"
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
		op   string
		val  interface{}
		want string
	}{
		{"string eq", "Name", "=", "Ada", "Name eq 'Ada'"},
		{"string with quote eq", "Name", "=", "O'Brien", "Name eq 'O''Brien'"},
		{"bool true eq", "Active", "=", true, "Active eq true"},
		{"bool false eq", "Active", "=", false, "Active eq false"},
		{"int eq", "Age", "=", int(36), "Age eq 36"},
		{"int64 eq", "Age", "=", int64(36), "Age eq 36"},
		{"float whole eq", "Age", "=", float64(36), "Age eq 36"},
		{"float fractional eq", "Score", "=", float64(3.14), "Score eq 3.14"},
		{"float overflow eq", "Big", "=", float64(9.2233720368547758e+19), "Big eq 9.223372036854776e+19"},
		{"time eq", "CreatedAt", "=", time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), "CreatedAt eq datetime'2026-09-04T12:00:00.0000000Z'"},
		{"bytes eq", "Data", "=", []byte{0xAB, 0xCD}, "Data eq X'abcd'"},
		{"nil eq", "Optional", "=", nil, "Optional eq null"},
		{"string ne", "Name", "!=", "Bob", "Name ne 'Bob'"},
		{"int gt", "Age", ">", int(30), "Age gt 30"},
		{"int ge", "Age", ">=", int(30), "Age ge 30"},
		{"int lt", "Age", "<", int(30), "Age lt 30"},
		{"int le", "Age", "<=", int(30), "Age le 30"},
		{"string ge", "PartitionKey", ">=", "a", "PartitionKey ge 'a'"},
		{"string lt", "PartitionKey", "<", "b", "PartitionKey lt 'b'"},
		{"bool ne", "Active", "!=", true, "Active ne true"},
		{"time gt", "CreatedAt", ">", time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), "CreatedAt gt datetime'2026-09-04T12:00:00.0000000Z'"},
		{"bytes ne", "Data", "!=", []byte{0xAB, 0xCD}, "Data ne X'abcd'"},
		{"nil ne", "Optional", "!=", nil, "Optional ne null"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatODataPredicate(c.col, c.op, c.val); got != c.want {
				t.Errorf("formatODataPredicate(%q, %q, %v) = %q, want %q", c.col, c.op, c.val, got, c.want)
			}
		})
	}
}

func TestSqlOpToOData(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"=", "eq"},
		{"!=", "ne"},
		{">", "gt"},
		{">=", "ge"},
		{"<", "lt"},
		{"<=", "le"},
		{"unknown", "eq"}, // default fallback
	}
	for _, c := range cases {
		if got := sqlOpToOData(c.in); got != c.want {
			t.Errorf("sqlOpToOData(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildODataFilterPreservesCase(t *testing.T) {
	conds := []resolvedCond{
		{column: "MyCol", op: "=", value: "x"},
		{column: "partitionkey", op: "=", value: "pk"},
		{column: "rowkey", op: "=", value: "rk"},
	}
	got := buildODataFilter(conds)
	for _, want := range []string{"MyCol eq 'x'", "PartitionKey eq 'pk'", "RowKey eq 'rk'"} {
		if !strings.Contains(got, want) {
			t.Errorf("buildODataFilter = %q, missing part %q", got, want)
		}
	}
}

func TestBuildODataFilterWithOperators(t *testing.T) {
	cases := []struct {
		name  string
		conds []resolvedCond
		want  string
	}{
		{
			name: "partition range scan",
			conds: []resolvedCond{
				{column: "PartitionKey", op: ">=", value: "a"},
				{column: "PartitionKey", op: "<", value: "b"},
			},
			want: "PartitionKey ge 'a' and PartitionKey lt 'b'",
		},
		{
			name: "numeric greater than",
			conds: []resolvedCond{
				{column: "Age", op: ">", value: int(30)},
			},
			want: "Age gt 30",
		},
		{
			name: "not equal string",
			conds: []resolvedCond{
				{column: "Name", op: "!=", value: "Bob"},
			},
			want: "Name ne 'Bob'",
		},
		{
			name: "mixed operators",
			conds: []resolvedCond{
				{column: "PartitionKey", op: "=", value: "p1"},
				{column: "Age", op: ">=", value: int64(18)},
				{column: "Age", op: "<=", value: int64(65)},
			},
			want: "PartitionKey eq 'p1' and Age ge 18 and Age le 65",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildODataFilter(c.conds); got != c.want {
				t.Errorf("buildODataFilter = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFindKeyValue(t *testing.T) {
	conds := []resolvedCond{
		{column: "MyCol", op: "=", value: "x"},
		{column: "PartitionKey", op: "=", value: "pk"},
		{column: "RowKey", op: "=", value: 42},
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

func TestFindKeyOp(t *testing.T) {
	conds := []resolvedCond{
		{column: "MyCol", op: "=", value: "x"},
		{column: "PartitionKey", op: "=", value: "pk"},
		{column: "RowKey", op: ">", value: "rk"},
	}
	if op := findKeyOp(conds, "PartitionKey"); op != "=" {
		t.Errorf("findKeyOp(PartitionKey) = %q, want %q", op, "=")
	}
	if op := findKeyOp(conds, "partitionkey"); op != "=" {
		t.Errorf("findKeyOp(partitionkey) = %q, want %q", op, "=")
	}
	if op := findKeyOp(conds, "RowKey"); op != ">" {
		t.Errorf("findKeyOp(RowKey) = %q, want %q", op, ">")
	}
	if op := findKeyOp(conds, "Missing"); op != "" {
		t.Errorf("findKeyOp(Missing) = %q, want %q", op, "")
	}
}

func TestIsNotFound(t *testing.T) {
	if isNotFound(nil) {
		t.Error("isNotFound(nil) should be false")
	}
}

func TestResolveWherePreservesCase(t *testing.T) {
	where := []whereCond{
		{column: "MyCol", op: "=", isPlaceholder: true},
		{column: "PartitionKey", op: "=", isPlaceholder: true},
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

func TestResolveWherePreservesOp(t *testing.T) {
	where := []whereCond{
		{column: "Age", op: ">", isPlaceholder: true},
		{column: "Name", op: "!=", value: "Bob"},
	}
	conds, err := resolveWhere(where, []driver.Value{30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conds) != 2 {
		t.Fatalf("expected 2 conds, got %d", len(conds))
	}
	if conds[0].op != ">" {
		t.Errorf("conds[0].op = %q, want %q", conds[0].op, ">")
	}
	if conds[1].op != "!=" {
		t.Errorf("conds[1].op = %q, want %q", conds[1].op, "!=")
	}
}

func TestResolveWhereTooFewArgs(t *testing.T) {
	where := []whereCond{
		{column: "A", op: "=", isPlaceholder: true},
		{column: "B", op: "=", isPlaceholder: true},
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

func TestWrapEDMType(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		input    driver.Value
		wantType string
	}{
		{"time.Time", now, "aztables.EDMDateTime"},
		{"[]byte", []byte{0xAB, 0xCD}, "aztables.EDMBinary"},
		{"int64", int64(42), "aztables.EDMInt64"},
		{"string passthrough", "hello", "string"},
		{"int passthrough", int(42), "int"},
		{"bool passthrough", true, "bool"},
		{"nil passthrough", nil, "<nil>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := wrapEDMType(c.input)
			gotType := fmt.Sprintf("%T", got)
			if gotType != c.wantType {
				t.Errorf("wrapEDMType(%T) type = %s, want %s", c.input, gotType, c.wantType)
			}
		})
	}
}
