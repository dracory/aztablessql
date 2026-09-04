package aztablessql

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

func TestBuildBatchActions_Empty(t *testing.T) {
	_, err := buildBatchActions(nil)
	if err == nil {
		t.Fatal("expected error for empty batch, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected 'empty' in error, got %v", err)
	}
}

func TestBuildBatchActions_OverLimit(t *testing.T) {
	ops := make([]BatchOp, maxBatchOps+1)
	for i := range ops {
		ops[i] = BatchOp{Kind: BatchInsert, Partition: "p", Row: "r", Properties: map[string]interface{}{}}
	}
	// Distinct row keys so the duplicate-row check doesn't trip first.
	for i := range ops {
		ops[i].Row = "r" + itoa(i)
	}
	_, err := buildBatchActions(ops)
	if err == nil {
		t.Fatal("expected error for >100 ops, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected 'exceeds limit' in error, got %v", err)
	}
}

func TestBuildBatchActions_MixedPartitions(t *testing.T) {
	ops := []BatchOp{
		{Kind: BatchInsert, Partition: "p1", Row: "r1", Properties: map[string]interface{}{"Name": "a"}},
		{Kind: BatchInsert, Partition: "p2", Row: "r2", Properties: map[string]interface{}{"Name": "b"}},
	}
	_, err := buildBatchActions(ops)
	if err == nil {
		t.Fatal("expected error for mixed partitions, got nil")
	}
	if !strings.Contains(err.Error(), "single partition") {
		t.Fatalf("expected 'single partition' in error, got %v", err)
	}
}

func TestBuildBatchActions_DuplicateRowKey(t *testing.T) {
	ops := []BatchOp{
		{Kind: BatchInsert, Partition: "p", Row: "r", Properties: map[string]interface{}{"Name": "a"}},
		{Kind: BatchInsert, Partition: "p", Row: "r", Properties: map[string]interface{}{"Name": "b"}},
	}
	_, err := buildBatchActions(ops)
	if err == nil {
		t.Fatal("expected error for duplicate RowKey, got nil")
	}
	if !strings.Contains(err.Error(), "duplicates RowKey") {
		t.Fatalf("expected 'duplicates RowKey' in error, got %v", err)
	}
}

func TestBuildBatchActions_UnknownKind(t *testing.T) {
	ops := []BatchOp{
		{Kind: BatchKind("bogus"), Partition: "p", Row: "r", Properties: map[string]interface{}{}},
	}
	_, err := buildBatchActions(ops)
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
	if !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("expected 'unknown kind' in error, got %v", err)
	}
}

func TestBuildBatchActions_MissingKeys(t *testing.T) {
	cases := []struct {
		name string
		op   BatchOp
		want string
	}{
		{"missing partition", BatchOp{Kind: BatchInsert, Row: "r", Properties: map[string]interface{}{}}, "missing PartitionKey"},
		{"missing row", BatchOp{Kind: BatchInsert, Partition: "p", Properties: map[string]interface{}{}}, "missing RowKey"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := buildBatchActions([]BatchOp{c.op})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("expected %q in error, got %v", c.want, err)
			}
		})
	}
}

func TestBuildBatchActions_DeleteWithProperties(t *testing.T) {
	ops := []BatchOp{
		{Kind: BatchDelete, Partition: "p", Row: "r", Properties: map[string]interface{}{"Name": "a"}},
	}
	_, err := buildBatchActions(ops)
	if err == nil {
		t.Fatal("expected error for delete with properties, got nil")
	}
	if !strings.Contains(err.Error(), "delete but carries Properties") {
		t.Fatalf("expected delete-properties error, got %v", err)
	}
}

func TestBuildBatchActions_ReadOnlyPseudoColumn(t *testing.T) {
	ops := []BatchOp{
		{Kind: BatchInsert, Partition: "p", Row: "r", Properties: map[string]interface{}{"ETag": "x"}},
	}
	_, err := buildBatchActions(ops)
	if err == nil {
		t.Fatal("expected error for ETag in properties, got nil")
	}
	if !strings.Contains(err.Error(), "read-only pseudo-column") {
		t.Fatalf("expected pseudo-column error, got %v", err)
	}
}

func TestBuildBatchActions_KindToActionType(t *testing.T) {
	cases := []struct {
		kind BatchKind
		want aztables.TransactionType
	}{
		{BatchInsert, aztables.TransactionTypeAdd},
		{BatchInsertMerge, aztables.TransactionTypeInsertMerge},
		{BatchInsertReplace, aztables.TransactionTypeInsertReplace},
		{BatchUpdateMerge, aztables.TransactionTypeUpdateMerge},
		{BatchUpdateReplace, aztables.TransactionTypeUpdateReplace},
		{BatchDelete, aztables.TransactionTypeDelete},
	}
	for _, c := range cases {
		got, err := batchKindToActionType(c.kind)
		if err != nil {
			t.Fatalf("batchKindToActionType(%q): %v", c.kind, err)
		}
		if got != c.want {
			t.Errorf("batchKindToActionType(%q) = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestBuildBatchActions_HappyPath(t *testing.T) {
	ops := []BatchOp{
		{Kind: BatchInsert, Partition: "p", Row: "r1", Properties: map[string]interface{}{"Name": "a", "Age": int64(1)}},
		{Kind: BatchInsert, Partition: "p", Row: "r2", Properties: map[string]interface{}{"Name": "b"}},
		{Kind: BatchDelete, Partition: "p", Row: "r3"},
	}
	actions, err := buildBatchActions(ops)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(actions) != 3 {
		t.Fatalf("expected 3 actions, got %d", len(actions))
	}
	if actions[0].ActionType != aztables.TransactionTypeAdd {
		t.Errorf("action 0 type = %q, want %q", actions[0].ActionType, aztables.TransactionTypeAdd)
	}
	if actions[2].ActionType != aztables.TransactionTypeDelete {
		t.Errorf("action 2 type = %q, want %q", actions[2].ActionType, aztables.TransactionTypeDelete)
	}
	if len(actions[0].Entity) == 0 {
		t.Error("expected non-empty entity body for insert action")
	}
	if len(actions[2].Entity) == 0 {
		t.Error("expected non-empty entity body for delete action (keys only)")
	}
}

func TestBuildBatchActions_ETagPropagation(t *testing.T) {
	ops := []BatchOp{
		{Kind: BatchUpdateMerge, Partition: "p", Row: "r", ETag: `W/"0xABC"`, Properties: map[string]interface{}{"Name": "a"}},
	}
	actions, err := buildBatchActions(ops)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if actions[0].IfMatch == nil {
		t.Fatal("expected IfMatch to be set, got nil")
	}
	if string(*actions[0].IfMatch) != `W/"0xABC"` {
		t.Errorf("IfMatch = %q, want %q", string(*actions[0].IfMatch), `W/"0xABC"`)
	}
}

func TestBuildBatchActions_ETagStarMapsToAny(t *testing.T) {
	ops := []BatchOp{
		{Kind: BatchDelete, Partition: "p", Row: "r", ETag: "*"},
	}
	actions, err := buildBatchActions(ops)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if actions[0].IfMatch == nil {
		t.Fatal("expected IfMatch to be set, got nil")
	}
	if *actions[0].IfMatch != azcore.ETagAny {
		t.Errorf("expected ETagAny for '*'")
	}
}

// itoa is a tiny dependency-free int formatter to keep the test file
// import-light; strconv would do equally well.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
