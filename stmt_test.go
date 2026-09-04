package aztablessql

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
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

// ---------------------------------------------------------------------------
// ETag helpers (Tier 1, item 3)
// ---------------------------------------------------------------------------

func TestFindETagValue(t *testing.T) {
	cases := []struct {
		name   string
		conds  []resolvedCond
		want   string
		wantOK bool
	}{
		{
			name:   "no etag",
			conds:  []resolvedCond{{column: "PartitionKey", op: "=", value: "pk"}},
			wantOK: false,
		},
		{
			name:   "etag placeholder value",
			conds:  []resolvedCond{{column: "ETag", op: "=", value: `W/"0xABC"`}},
			want:   `W/"0xABC"`,
			wantOK: true,
		},
		{
			name:   "etag lowercase",
			conds:  []resolvedCond{{column: "etag", op: "=", value: `W/"0xDEF"`}},
			want:   `W/"0xDEF"`,
			wantOK: true,
		},
		{
			name:   "etag star",
			conds:  []resolvedCond{{column: "ETag", op: "=", value: "*"}},
			want:   "*",
			wantOK: true,
		},
		{
			name:   "etag non-eq ignored",
			conds:  []resolvedCond{{column: "ETag", op: ">", value: "x"}},
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := findETagValue(c.conds)
			if ok != c.wantOK {
				t.Fatalf("findETagValue ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Errorf("findETagValue = %q, want %q", got, c.want)
			}
		})
	}
}

func TestETagPtr(t *testing.T) {
	// Regular ETag value.
	p := etagPtr(`W/"0xABC"`)
	if p == nil {
		t.Fatal("etagPtr returned nil")
	}
	if string(*p) != `W/"0xABC"` {
		t.Errorf("etagPtr value = %q, want %q", string(*p), `W/"0xABC"`)
	}

	// "*" maps to azcore.ETagAny.
	pStar := etagPtr("*")
	if string(*pStar) != string(azcore.ETagAny) {
		t.Errorf("etagPtr('*') = %q, want %q", string(*pStar), string(azcore.ETagAny))
	}
}

func TestWrapPreconditionFailed(t *testing.T) {
	if err := wrapPreconditionFailed(nil); err != nil {
		t.Errorf("wrapPreconditionFailed(nil) = %v, want nil", err)
	}

	// Non-412 error passes through unchanged.
	passErr := errors.New("some other error")
	if got := wrapPreconditionFailed(passErr); got != passErr {
		t.Errorf("wrapPreconditionFailed(non-412) returned a different error: %v", got)
	}
}

// TestWrapPreconditionFailed412 verifies that a 412 *azcore.ResponseError is
// wrapped with the "aztablessql: ETag precondition failed" prefix and that
// the original error is preserved via %w so errors.As still works.
func TestWrapPreconditionFailed412(t *testing.T) {
	orig := &azcore.ResponseError{StatusCode: 412, ErrorCode: "UpdateConditionNotSatisfied"}
	got := wrapPreconditionFailed(orig)

	if got == nil {
		t.Fatal("wrapPreconditionFailed(412) returned nil, want wrapped error")
	}
	if !strings.Contains(got.Error(), "aztablessql: ETag precondition failed") {
		t.Errorf("error = %q, want it to contain 'aztablessql: ETag precondition failed'", got.Error())
	}
	if !strings.Contains(got.Error(), "UpdateConditionNotSatisfied") {
		t.Errorf("error = %q, want it to preserve the original ErrorCode", got.Error())
	}

	// errors.As must still find the original *azcore.ResponseError.
	var respErr *azcore.ResponseError
	if !errors.As(got, &respErr) {
		t.Errorf("errors.As failed to find *azcore.ResponseError in wrapped error: %v", got)
	}
	if respErr.StatusCode != 412 {
		t.Errorf("unwrapped StatusCode = %d, want 412", respErr.StatusCode)
	}
}

// TestWrapPreconditionFailedNon412ResponseError verifies that a non-412
// *azcore.ResponseError (e.g. 404) passes through unwrapped.
func TestWrapPreconditionFailedNon412ResponseError(t *testing.T) {
	orig := &azcore.ResponseError{StatusCode: 404, ErrorCode: "ResourceNotFound"}
	got := wrapPreconditionFailed(orig)
	if got != orig {
		t.Errorf("wrapPreconditionFailed(404) wrapped the error, want pass-through: got %v", got)
	}
}

func TestBuildInsertEntityRejectsETagAndTimestamp(t *testing.T) {
	cases := []struct {
		name    string
		columns []string
		args    []driver.Value
		wantErr string
	}{
		{
			name:    "insert etag",
			columns: []string{"PartitionKey", "RowKey", "ETag"},
			args:    []driver.Value{"pk", "rk", `W/"0x"`},
			wantErr: "read-only pseudo-column",
		},
		{
			name:    "insert timestamp",
			columns: []string{"PartitionKey", "RowKey", "Timestamp"},
			args:    []driver.Value{"pk", "rk", "2026-01-01T00:00:00Z"},
			wantErr: "read-only pseudo-column",
		},
		{
			name:    "insert lowercase etag",
			columns: []string{"PartitionKey", "RowKey", "etag"},
			args:    []driver.Value{"pk", "rk", `W/"0x"`},
			wantErr: "read-only pseudo-column",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := buildInsertEntity(c.columns, c.args)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

// TestValidatePointConds verifies the execution-time defensive backstop for
// UPDATE/DELETE: exactly PartitionKey = ? AND RowKey = ?, optionally
// AND ETag = ?. Extra conditions, non-"=" operators, and unknown columns
// are rejected.
func TestValidatePointConds(t *testing.T) {
	cases := []struct {
		name       string
		conds      []resolvedCond
		wantErr    bool
		wantErrSub string
		wantPK     string
		wantRK     string
		wantETag   string
	}{
		{
			name:   "pk rk only",
			conds:  []resolvedCond{{column: "PartitionKey", op: "=", value: "pk"}, {column: "RowKey", op: "=", value: "rk"}},
			wantPK: "pk",
			wantRK: "rk",
		},
		{
			name:     "pk rk etag",
			conds:    []resolvedCond{{column: "PartitionKey", op: "=", value: "pk"}, {column: "RowKey", op: "=", value: "rk"}, {column: "ETag", op: "=", value: `W/"0x"`}},
			wantPK:   "pk",
			wantRK:   "rk",
			wantETag: `W/"0x"`,
		},
		{
			name:       "too few conds",
			conds:      []resolvedCond{{column: "PartitionKey", op: "=", value: "pk"}},
			wantErr:    true,
			wantErrSub: "requires WHERE PartitionKey",
		},
		{
			name:       "too many conds",
			conds:      []resolvedCond{{column: "PartitionKey", op: "=", value: "pk"}, {column: "RowKey", op: "=", value: "rk"}, {column: "ETag", op: "=", value: "x"}, {column: "Extra", op: "=", value: "y"}},
			wantErr:    true,
			wantErrSub: "optionally AND ETag",
		},
		{
			name:       "unknown column",
			conds:      []resolvedCond{{column: "PartitionKey", op: "=", value: "pk"}, {column: "RowKey", op: "=", value: "rk"}, {column: "Age", op: "=", value: "30"}},
			wantErr:    true,
			wantErrSub: "only supports PartitionKey, RowKey and ETag",
		},
		{
			name:       "non-eq on pk",
			conds:      []resolvedCond{{column: "PartitionKey", op: ">", value: "pk"}, {column: "RowKey", op: "=", value: "rk"}},
			wantErr:    true,
			wantErrSub: "only supports = on PartitionKey",
		},
		{
			name:       "non-eq on etag",
			conds:      []resolvedCond{{column: "PartitionKey", op: "=", value: "pk"}, {column: "RowKey", op: "=", value: "rk"}, {column: "ETag", op: ">", value: "x"}},
			wantErr:    true,
			wantErrSub: "ETag condition only supports =",
		},
		{
			name:       "duplicate etag (4 conds rejected by length check first)",
			conds:      []resolvedCond{{column: "PartitionKey", op: "=", value: "pk"}, {column: "RowKey", op: "=", value: "rk"}, {column: "ETag", op: "=", value: "x"}, {column: "ETag", op: "=", value: "y"}},
			wantErr:    true,
			wantErrSub: "optionally AND ETag",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pk, rk, etag, err := validatePointConds(c.conds, "UPDATE")
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
			if pk != c.wantPK {
				t.Errorf("pk = %q, want %q", pk, c.wantPK)
			}
			if rk != c.wantRK {
				t.Errorf("rk = %q, want %q", rk, c.wantRK)
			}
			if etag != c.wantETag {
				t.Errorf("etag = %q, want %q", etag, c.wantETag)
			}
		})
	}
}
