package aztablessql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// maxBatchOps is the Azure Table Storage limit on the number of operations
// in a single entity-group transaction (batch). See:
// https://learn.microsoft.com/rest/api/storageservices/performing-entity-group-transactions
const maxBatchOps = 100

// BatchKind selects the mutation performed by a BatchOp within a batch.
type BatchKind string

const (
	// BatchInsert adds a new entity. Fails the whole batch if the RowKey
	// already exists within the partition.
	BatchInsert BatchKind = "insert"
	// BatchInsertMerge inserts a new entity, or merges properties into an
	// existing one (only SET properties are touched on an existing entity).
	BatchInsertMerge BatchKind = "insert-merge"
	// BatchInsertReplace inserts a new entity, or replaces an existing one
	// (drops properties not present in the batch op).
	BatchInsertReplace BatchKind = "insert-replace"
	// BatchUpdateMerge merges the supplied properties into an existing
	// entity. Fails the whole batch if the entity does not exist.
	BatchUpdateMerge BatchKind = "update-merge"
	// BatchUpdateReplace replaces an existing entity with the supplied
	// properties. Fails the whole batch if the entity does not exist.
	BatchUpdateReplace BatchKind = "update-replace"
	// BatchDelete removes an entity. Fails the whole batch if the entity
	// does not exist (unless ETag="*").
	BatchDelete BatchKind = "delete"
)

// BatchOp is a single operation within an entity-group transaction.
//
// Partition and Row are required for every kind. Properties carries the
// entity body for insert/update kinds and is ignored for BatchDelete.
// ETag is optional: when non-empty it is sent as the If-Match precondition
// for the op; the special value "*" matches any existing ETag.
//
// All ops in a single SubmitBatch call MUST share the same Partition value
// — this is the Table Storage entity-group transaction constraint and is
// enforced client-side before any network call.
type BatchOp struct {
	Kind       BatchKind
	Partition  string
	Row        string
	Properties map[string]interface{}
	ETag       string
}

// BatchClient submits entity-group transactions against a single table.
// Obtain one via db.Conn(ctx).Raw(...) — see the package README.
type BatchClient interface {
	SubmitBatch(ctx context.Context, ops []BatchOp) error
}

// batchClient is the concrete BatchClient backed by an aztables table client.
type batchClient struct {
	client *aztables.Client
}

// BatchClient returns a BatchClient bound to the given table on this
// connection's service client. It is intended to be reached through
// database/sql's Conn.Raw accessor, doing the batch work inside the
// callback (Raw's contract says the driver conn must not be used after
// the callback returns):
//
//	conn, _ := db.Conn(ctx)
//	defer conn.Close()
//	err := conn.Raw(func(driverConn any) error {
//	    bc := driverConn.(*Conn).BatchClient(table)
//	    return bc.SubmitBatch(ctx, ops)
//	})
//
// This is the supported escape hatch from the single-statement database/sql
// contract: Table Storage entity-group transactions are not SQL-text driven.
func (c *Conn) BatchClient(table string) BatchClient {
	return &batchClient{client: c.svc.NewClient(table)}
}

// SubmitBatch validates the ops, builds the SDK transaction actions, and
// submits them atomically. All ops must share one PartitionKey and there
// may be at most 100 ops. A single failing op rolls back the entire batch.
func (b *batchClient) SubmitBatch(ctx context.Context, ops []BatchOp) error {
	actions, err := buildBatchActions(ops)
	if err != nil {
		return err
	}
	_, err = b.client.SubmitTransaction(ctx, actions, nil)
	return err
}

// buildBatchActions validates the op slice and converts it into the
// []aztables.TransactionAction expected by SubmitTransaction. It is
// exported via the unexported receiver so it can be unit-tested without
// a live Table Storage endpoint.
func buildBatchActions(ops []BatchOp) ([]aztables.TransactionAction, error) {
	if len(ops) == 0 {
		return nil, errors.New("aztablessql: batch is empty")
	}
	if len(ops) > maxBatchOps {
		return nil, fmt.Errorf("aztablessql: batch has %d ops, exceeds limit of %d", len(ops), maxBatchOps)
	}

	partition := ops[0].Partition
	if partition == "" {
		return nil, errors.New("aztablessql: batch op 0 is missing PartitionKey")
	}
	seenRows := make(map[string]struct{}, len(ops))
	actions := make([]aztables.TransactionAction, 0, len(ops))

	for i, op := range ops {
		if err := validateBatchOp(op, i); err != nil {
			return nil, err
		}
		if op.Partition != partition {
			return nil, fmt.Errorf("aztablessql: batch op %d has PartitionKey %q, want %q (entity-group transactions require a single partition)", i, op.Partition, partition)
		}
		if _, dup := seenRows[op.Row]; dup {
			// The SDK rejects duplicate RowKeys within a batch; surface a
			// clear client-side error instead of a generic server 400.
			return nil, fmt.Errorf("aztablessql: batch op %d duplicates RowKey %q (a batch may target each RowKey at most once)", i, op.Row)
		}
		seenRows[op.Row] = struct{}{}

		action, err := batchOpToAction(op, i)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, nil
}

// validateBatchOp checks the per-op invariants that do not depend on the
// surrounding batch (kind, keys, properties-for-delete).
func validateBatchOp(op BatchOp, i int) error {
	switch op.Kind {
	case BatchInsert, BatchInsertMerge, BatchInsertReplace,
		BatchUpdateMerge, BatchUpdateReplace, BatchDelete:
	default:
		return fmt.Errorf("aztablessql: batch op %d has unknown kind %q", i, op.Kind)
	}
	if op.Partition == "" {
		return fmt.Errorf("aztablessql: batch op %d is missing PartitionKey", i)
	}
	if op.Row == "" {
		return fmt.Errorf("aztablessql: batch op %d is missing RowKey", i)
	}
	if op.Kind == BatchDelete && len(op.Properties) != 0 {
		// Not a hard error for the SDK, but it indicates caller confusion:
		// delete carries no body. Reject to keep the contract explicit.
		return fmt.Errorf("aztablessql: batch op %d is a delete but carries Properties", i)
	}
	return nil
}

// batchOpToAction converts a single BatchOp into a TransactionAction. The
// entity body is built via buildBatchEntity so the key/property handling
// matches the non-batch INSERT/UPDATE paths (EDM wrapping, read-only
// pseudo-column rejection).
func batchOpToAction(op BatchOp, i int) (aztables.TransactionAction, error) {
	actionType, err := batchKindToActionType(op.Kind)
	if err != nil {
		return aztables.TransactionAction{}, fmt.Errorf("aztablessql: batch op %d: %w", i, err)
	}

	action := aztables.TransactionAction{ActionType: actionType}

	if op.Kind != BatchDelete {
		entity, err := buildBatchEntity(op)
		if err != nil {
			return aztables.TransactionAction{}, fmt.Errorf("aztablessql: batch op %d: %w", i, err)
		}
		b, err := json.Marshal(entity)
		if err != nil {
			return aztables.TransactionAction{}, fmt.Errorf("aztablessql: batch op %d: marshal entity: %w", i, err)
		}
		action.Entity = b
	} else {
		// For delete, the SDK needs an entity body carrying only the keys
		// so it can address the row.
		entity := aztables.EDMEntity{
			PartitionKey: op.Partition,
			RowKey:       op.Row,
		}
		b, err := json.Marshal(entity)
		if err != nil {
			return aztables.TransactionAction{}, fmt.Errorf("aztablessql: batch op %d: marshal delete entity: %w", i, err)
		}
		action.Entity = b
	}

	if op.ETag != "" {
		e := azcore.ETag(op.ETag)
		if op.ETag == "*" {
			e = azcore.ETagAny
		}
		action.IfMatch = &e
	}
	return action, nil
}

// batchKindToActionType maps the public BatchKind enum onto the SDK's
// TransactionType constants.
func batchKindToActionType(k BatchKind) (aztables.TransactionType, error) {
	switch k {
	case BatchInsert:
		return aztables.TransactionTypeAdd, nil
	case BatchInsertMerge:
		return aztables.TransactionTypeInsertMerge, nil
	case BatchInsertReplace:
		return aztables.TransactionTypeInsertReplace, nil
	case BatchUpdateMerge:
		return aztables.TransactionTypeUpdateMerge, nil
	case BatchUpdateReplace:
		return aztables.TransactionTypeUpdateReplace, nil
	case BatchDelete:
		return aztables.TransactionTypeDelete, nil
	default:
		return "", fmt.Errorf("unknown batch kind %q", k)
	}
}

// buildBatchEntity assembles an aztables.EDMEntity for a non-delete batch
// op. It mirrors buildInsertEntity: PartitionKey/RowKey are pulled out of
// Properties into the entity key fields, ETag/Timestamp are rejected as
// read-only pseudo-columns, and remaining values are EDM-wrapped.
func buildBatchEntity(op BatchOp) (aztables.EDMEntity, error) {
	entity := aztables.EDMEntity{
		PartitionKey: op.Partition,
		RowKey:       op.Row,
		Properties:   make(map[string]interface{}, len(op.Properties)),
	}
	for col, val := range op.Properties {
		switch strings.ToLower(col) {
		case "partitionkey", "rowkey":
			// Already set from op.Partition/op.Row; ignore any duplicate
			// entry in the properties map rather than silently overwriting
			// the canonical key with a possibly-mismatched value.
			continue
		case "etag", "timestamp":
			return entity, fmt.Errorf("cannot set read-only pseudo-column %q in a batch", col)
		default:
			entity.Properties[col] = wrapEDMType(val)
		}
	}
	return entity, nil
}
