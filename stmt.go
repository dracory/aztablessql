package aztablessql

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

type Stmt struct {
	conn *Conn
	pq   *parsedQuery
}

func (s *Stmt) Close() error  { return nil }
func (s *Stmt) NumInput() int { return s.pq.numPlaceholders }

func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
	switch s.pq.kind {
	case qInsert:
		return s.execInsert(args)
	case qDelete:
		return s.execDelete(args)
	default:
		return nil, errors.New("aztablessql: Exec not supported for SELECT, use Query")
	}
}

func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.pq.kind != qSelect {
		return nil, errors.New("aztablessql: Query only supported for SELECT")
	}
	return s.execSelect(args)
}

func (s *Stmt) execInsert(args []driver.Value) (driver.Result, error) {
	if len(args) != len(s.pq.columns) {
		return nil, fmt.Errorf("aztablessql: expected %d args, got %d", len(s.pq.columns), len(args))
	}
	client := s.conn.svc.NewClient(s.pq.table)

	entity := aztables.EDMEntity{Properties: map[string]interface{}{}}
	for i, col := range s.pq.columns {
		switch strings.ToLower(col) {
		case "partitionkey":
			entity.PartitionKey = fmt.Sprintf("%v", args[i])
		case "rowkey":
			entity.RowKey = fmt.Sprintf("%v", args[i])
		default:
			entity.Properties[col] = args[i]
		}
	}
	if entity.PartitionKey == "" || entity.RowKey == "" {
		return nil, errors.New("aztablessql: INSERT requires PartitionKey and RowKey columns")
	}

	b, err := json.Marshal(entity)
	if err != nil {
		return nil, err
	}
	if _, err := client.AddEntity(s.conn.ctx, b, nil); err != nil {
		return nil, err
	}
	return driverResult{rowsAffected: 1}, nil
}

func (s *Stmt) execDelete(args []driver.Value) (driver.Result, error) {
	where, err := resolveWhere(s.pq.where, args)
	if err != nil {
		return nil, err
	}
	pk, ok1 := where["partitionkey"]
	rk, ok2 := where["rowkey"]
	if !ok1 || !ok2 {
		return nil, errors.New("aztablessql: DELETE requires WHERE PartitionKey = ? AND RowKey = ?")
	}
	client := s.conn.svc.NewClient(s.pq.table)
	if _, err := client.DeleteEntity(s.conn.ctx, pk, rk, nil); err != nil {
		return nil, err
	}
	return driverResult{rowsAffected: 1}, nil
}

func (s *Stmt) execSelect(args []driver.Value) (driver.Rows, error) {
	where, err := resolveWhere(s.pq.where, args)
	if err != nil {
		return nil, err
	}
	client := s.conn.svc.NewClient(s.pq.table)

	pk, hasPK := where["partitionkey"]
	rk, hasRK := where["rowkey"]

	var entities [][]byte

	if hasPK && hasRK && len(where) == 2 {
		resp, err := client.GetEntity(s.conn.ctx, pk, rk, nil)
		if err != nil {
			if isNotFound(err) {
				return &Rows{columns: s.pq.columns, allCols: s.pq.allColumns}, nil
			}
			return nil, err
		}
		entities = append(entities, resp.Value)
	} else {
		opts := &aztables.ListEntitiesOptions{}
		if filter := buildODataFilter(where); filter != "" {
			opts.Filter = &filter
		}
		pager := client.NewListEntitiesPager(opts)
		for pager.More() {
			resp, err := pager.NextPage(s.conn.ctx)
			if err != nil {
				return nil, err
			}
			entities = append(entities, resp.Entities...)
		}
	}

	return &Rows{columns: s.pq.columns, allCols: s.pq.allColumns, entities: entities}, nil
}

func resolveWhere(where []whereCond, args []driver.Value) (map[string]string, error) {
	result := map[string]string{}
	argIdx := 0
	for _, c := range where {
		var val string
		if c.isPlaceholder {
			if argIdx >= len(args) {
				return nil, errors.New("aztablessql: not enough arguments for placeholders")
			}
			val = fmt.Sprintf("%v", args[argIdx])
			argIdx++
		} else {
			val = c.value
		}
		result[strings.ToLower(c.column)] = val
	}
	return result, nil
}

func buildODataFilter(where map[string]string) string {
	var parts []string
	for col, val := range where {
		name := col
		switch col {
		case "partitionkey":
			name = "PartitionKey"
		case "rowkey":
			name = "RowKey"
		}
		parts = append(parts, fmt.Sprintf("%s eq '%s'", name, strings.ReplaceAll(val, "'", "''")))
	}
	return strings.Join(parts, " and ")
}

func isNotFound(err error) bool {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		return respErr.StatusCode == 404
	}
	return false
}

type driverResult struct{ rowsAffected int64 }

func (r driverResult) LastInsertId() (int64, error) { return 0, errors.New("aztablessql: not supported") }
func (r driverResult) RowsAffected() (int64, error) { return r.rowsAffected, nil }
