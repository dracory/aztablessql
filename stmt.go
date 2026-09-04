package aztablessql

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

var errTransactionsNotSupported = errors.New("aztablessql: transactions are not supported")

type Stmt struct {
	conn *Conn
	pq   *parsedQuery
}

func (s *Stmt) Close() error  { return nil }
func (s *Stmt) NumInput() int { return s.pq.numPlaceholders }

// ---------------------------------------------------------------------------
// Exec / ExecContext
// ---------------------------------------------------------------------------

func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.exec(context.Background(), args)
}

// ExecContext implements driver.StmtExecContext.
func (s *Stmt) ExecContext(ctx context.Context, args []driver.Value) (driver.Result, error) {
	return s.exec(ctx, args)
}

func (s *Stmt) exec(ctx context.Context, args []driver.Value) (driver.Result, error) {
	switch s.pq.kind {
	case qInsert:
		return s.execInsert(ctx, args)
	case qUpdate:
		return s.execUpdate(ctx, args)
	case qDelete:
		return s.execDelete(ctx, args)
	default:
		return nil, errors.New("aztablessql: Exec not supported for SELECT, use Query")
	}
}

// ---------------------------------------------------------------------------
// Query / QueryContext
// ---------------------------------------------------------------------------

func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.query(context.Background(), args)
}

// QueryContext implements driver.StmtQueryContext.
func (s *Stmt) QueryContext(ctx context.Context, args []driver.Value) (driver.Rows, error) {
	return s.query(ctx, args)
}

func (s *Stmt) query(ctx context.Context, args []driver.Value) (driver.Rows, error) {
	if s.pq.kind != qSelect {
		return nil, errors.New("aztablessql: Query only supported for SELECT")
	}
	return s.execSelect(ctx, args)
}

// ---------------------------------------------------------------------------
// INSERT
// ---------------------------------------------------------------------------

func (s *Stmt) execInsert(ctx context.Context, args []driver.Value) (driver.Result, error) {
	if len(args) != len(s.pq.columns) {
		return nil, fmt.Errorf("aztablessql: expected %d args, got %d", len(s.pq.columns), len(args))
	}
	client := s.conn.svc.NewClient(s.pq.table)

	entity := aztables.EDMEntity{Properties: map[string]interface{}{}}
	hasPK, hasRK := false, false
	for i, col := range s.pq.columns {
		switch strings.ToLower(col) {
		case "partitionkey":
			entity.PartitionKey = fmt.Sprintf("%v", args[i])
			hasPK = true
		case "rowkey":
			entity.RowKey = fmt.Sprintf("%v", args[i])
			hasRK = true
		default:
			entity.Properties[col] = wrapEDMType(args[i])
		}
	}
	if !hasPK || !hasRK {
		return nil, errors.New("aztablessql: INSERT requires PartitionKey and RowKey columns")
	}

	b, err := json.Marshal(entity)
	if err != nil {
		return nil, err
	}
	if _, err := client.AddEntity(ctx, b, nil); err != nil {
		return nil, err
	}
	return driverResult{rowsAffected: 1}, nil
}

// ---------------------------------------------------------------------------
// UPDATE (merge semantics — only SET columns are touched)
// ---------------------------------------------------------------------------

func (s *Stmt) execUpdate(ctx context.Context, args []driver.Value) (driver.Result, error) {
	if len(args) != s.pq.numPlaceholders {
		return nil, fmt.Errorf("aztablessql: expected %d args, got %d", s.pq.numPlaceholders, len(args))
	}
	setArgs := args[:s.pq.setPlaceholders]
	whereArgs := args[s.pq.setPlaceholders:]

	conds, err := resolveWhere(s.pq.where, whereArgs)
	if err != nil {
		return nil, err
	}
	pk, ok1 := findKeyValue(conds, "PartitionKey")
	rk, ok2 := findKeyValue(conds, "RowKey")
	if !ok1 || !ok2 {
		return nil, errors.New("aztablessql: UPDATE requires WHERE PartitionKey = ? AND RowKey = ?")
	}

	entity := aztables.EDMEntity{Properties: map[string]interface{}{}}
	entity.PartitionKey = pk
	entity.RowKey = rk

	argIdx := 0
	for _, a := range s.pq.set {
		if a.isPlaceholder {
			entity.Properties[a.column] = wrapEDMType(setArgs[argIdx])
			argIdx++
		} else {
			entity.Properties[a.column] = a.value
		}
	}

	b, err := json.Marshal(entity)
	if err != nil {
		return nil, err
	}

	client := s.conn.svc.NewClient(s.pq.table)
	if _, err := client.UpdateEntity(ctx, b, &aztables.UpdateEntityOptions{
		UpdateMode: aztables.UpdateModeMerge,
	}); err != nil {
		return nil, err
	}
	return driverResult{rowsAffected: 1}, nil
}

// ---------------------------------------------------------------------------
// DELETE
// ---------------------------------------------------------------------------

func (s *Stmt) execDelete(ctx context.Context, args []driver.Value) (driver.Result, error) {
	conds, err := resolveWhere(s.pq.where, args)
	if err != nil {
		return nil, err
	}
	// DELETE only supports a point delete on PartitionKey + RowKey.
	// Any additional condition is rejected so the caller cannot accidentally
	// delete a row that does not match their full predicate.
	if len(conds) != 2 {
		return nil, errors.New("aztablessql: DELETE requires exactly WHERE PartitionKey = ? AND RowKey = ?")
	}
	pk, ok1 := findKeyValue(conds, "PartitionKey")
	rk, ok2 := findKeyValue(conds, "RowKey")
	if !ok1 || !ok2 {
		return nil, errors.New("aztablessql: DELETE requires WHERE PartitionKey = ? AND RowKey = ?")
	}
	client := s.conn.svc.NewClient(s.pq.table)
	if _, err := client.DeleteEntity(ctx, pk, rk, nil); err != nil {
		return nil, err
	}
	return driverResult{rowsAffected: 1}, nil
}

// ---------------------------------------------------------------------------
// SELECT
// ---------------------------------------------------------------------------

func (s *Stmt) execSelect(ctx context.Context, args []driver.Value) (driver.Rows, error) {
	conds, err := resolveWhere(s.pq.where, args)
	if err != nil {
		return nil, err
	}
	client := s.conn.svc.NewClient(s.pq.table)

	pk, hasPK := findKeyValue(conds, "PartitionKey")
	rk, hasRK := findKeyValue(conds, "RowKey")

	var entities [][]byte

	// Point read: exactly PartitionKey + RowKey, nothing else.
	if hasPK && hasRK && len(conds) == 2 {
		resp, err := client.GetEntity(ctx, pk, rk, nil)
		if err != nil {
			if isNotFound(err) {
				return &Rows{columns: s.pq.columns, allCols: s.pq.allColumns}, nil
			}
			return nil, err
		}
		entities = append(entities, resp.Value)
	} else {
		opts := &aztables.ListEntitiesOptions{}
		if filter := buildODataFilter(conds); filter != "" {
			opts.Filter = &filter
		}
		pager := client.NewListEntitiesPager(opts)
		for pager.More() {
			resp, err := pager.NextPage(ctx)
			if err != nil {
				return nil, err
			}
			entities = append(entities, resp.Entities...)
		}
	}

	return &Rows{columns: s.pq.columns, allCols: s.pq.allColumns, entities: entities}, nil
}

// ---------------------------------------------------------------------------
// WHERE resolution — preserves original column casing
// ---------------------------------------------------------------------------

// resolvedCond keeps the original column name as written by the caller so that
// Azure Table Storage property-name matching (which is case-sensitive) works
// correctly. Only PartitionKey / RowKey are matched case-insensitively.
type resolvedCond struct {
	column string
	value  driver.Value
}

func resolveWhere(where []whereCond, args []driver.Value) ([]resolvedCond, error) {
	out := make([]resolvedCond, 0, len(where))
	argIdx := 0
	for _, c := range where {
		var val driver.Value
		if c.isPlaceholder {
			if argIdx >= len(args) {
				return nil, errors.New("aztablessql: not enough arguments for placeholders")
			}
			val = args[argIdx]
			argIdx++
		} else {
			val = c.value
		}
		out = append(out, resolvedCond{column: c.column, value: val})
	}
	return out, nil
}

// findKeyValue performs a case-insensitive search for a key column
// (PartitionKey or RowKey) and returns its string representation.
func findKeyValue(conds []resolvedCond, key string) (string, bool) {
	for _, c := range conds {
		if strings.EqualFold(c.column, key) {
			return fmt.Sprintf("%v", c.value), true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// OData filter builder — type-aware
// ---------------------------------------------------------------------------

func buildODataFilter(conds []resolvedCond) string {
	var parts []string
	for _, c := range conds {
		name := normalizeKeyName(c.column)
		parts = append(parts, formatODataPredicate(name, c.value))
	}
	return strings.Join(parts, " and ")
}

// normalizeKeyName maps the case-insensitive key names to the canonical
// Azure Table Storage casing. All other column names are returned as-is
// to preserve the caller's original casing.
func normalizeKeyName(col string) string {
	if strings.EqualFold(col, "PartitionKey") {
		return "PartitionKey"
	}
	if strings.EqualFold(col, "RowKey") {
		return "RowKey"
	}
	return col
}

// formatODataPredicate renders a single `col eq <value>` OData predicate,
// quoting the value only when it is a string. Numeric and boolean values
// are emitted unquoted so that OData type matching works correctly.
// time.Time is formatted as an OData datetime literal. []byte is formatted
// as an Edm.Binary literal (X'hex').
func formatODataPredicate(col string, val driver.Value) string {
	switch v := val.(type) {
	case string:
		return fmt.Sprintf("%s eq '%s'", col, strings.ReplaceAll(v, "'", "''"))
	case bool:
		return fmt.Sprintf("%s eq %t", col, v)
	case int:
		return fmt.Sprintf("%s eq %d", col, v)
	case int64:
		return fmt.Sprintf("%s eq %d", col, v)
	case float64:
		// JSON numbers arrive as float64. Emit as integer when the value
		// is a whole number to avoid `42.000000` in the filter.
		// Guard against overflow: if the value exceeds int64 range, emit
		// as a float instead of wrapping.
		if v >= -9.2233720368547758e+18 && v <= 9.2233720368547758e+18 && v == float64(int64(v)) {
			return fmt.Sprintf("%s eq %d", col, int64(v))
		}
		return fmt.Sprintf("%s eq %g", col, v)
	case time.Time:
		return fmt.Sprintf("%s eq datetime'%s'", col, v.UTC().Format("2006-01-02T15:04:05.0000000Z"))
	case []byte:
		return fmt.Sprintf("%s eq X'%x'", col, v)
	case nil:
		return fmt.Sprintf("%s eq null", col)
	default:
		return fmt.Sprintf("%s eq '%s'", col, strings.ReplaceAll(fmt.Sprintf("%v", v), "'", "''"))
	}
}

// ---------------------------------------------------------------------------

// wrapEDMType converts Go native types into their aztables EDM equivalents
// so that the SDK serializes them with the correct Edm type annotations.
//
//   - time.Time  → EDMDateTime (Edm.DateTime)
//   - []byte     → EDMBinary   (Edm.Binary)
//   - int64      → EDMInt64    (Edm.Int64)
//
// float64, bool, string, and nil pass through unchanged. The Azure Table
// Storage service infers their Edm types from the JSON value shape
// (Edm.Double for float64, Edm.Boolean for bool, Edm.String for string).
// This is a known limitation: if a column was created as Edm.Int32 but a
// placeholder supplies a float64, the service may store it as Edm.Double.
// Callers who need precise type control should pass int64 for integers.
func wrapEDMType(v driver.Value) interface{} {
	switch v := v.(type) {
	case time.Time:
		return aztables.EDMDateTime(v)
	case []byte:
		return aztables.EDMBinary(v)
	case int64:
		return aztables.EDMInt64(v)
	default:
		return v
	}
}

func isNotFound(err error) bool {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		return respErr.StatusCode == 404
	}
	return false
}

type driverResult struct{ rowsAffected int64 }

func (r driverResult) LastInsertId() (int64, error) {
	return 0, errors.New("aztablessql: not supported")
}
func (r driverResult) RowsAffected() (int64, error) { return r.rowsAffected, nil }
