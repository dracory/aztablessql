package aztablessql

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

func init() {
	sql.Register("aztables", &Driver{})
}

// Driver implements database/sql/driver.Driver.
// DSN is an Azure Storage connection string, e.g.
//
//	"DefaultEndpointsProtocol=https;AccountName=...;AccountKey=...;EndpointSuffix=core.windows.net"
type Driver struct{}

func (d *Driver) Open(dsn string) (driver.Conn, error) {
	svc, err := aztables.NewServiceClientFromConnectionString(dsn, nil)
	if err != nil {
		return nil, err
	}
	return &Conn{svc: svc}, nil
}

type Conn struct {
	svc *aztables.ServiceClient
}

func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

// PrepareContext implements driver.ConnPrepareContext so the caller's context
// is threaded through to every subsequent Exec/Query call.
func (c *Conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	pq, err := parseQuery(query)
	if err != nil {
		return nil, err
	}
	return &Stmt{conn: c, pq: pq, ctx: ctx}, nil
}

func (c *Conn) Close() error { return nil }

func (c *Conn) Begin() (driver.Tx, error) {
	return nil, errTransactionsNotSupported
}
