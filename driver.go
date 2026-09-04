package aztablessql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

func init() {
	sql.Register("aztables", &Driver{})
}

// Driver implements database/sql/driver.Driver.
// DSN is an Azure Storage connection string, e.g.
//   "DefaultEndpointsProtocol=https;AccountName=...;AccountKey=...;EndpointSuffix=core.windows.net"
type Driver struct{}

func (d *Driver) Open(dsn string) (driver.Conn, error) {
	svc, err := aztables.NewServiceClientFromConnectionString(dsn, nil)
	if err != nil {
		return nil, err
	}
	return &Conn{svc: svc, ctx: context.Background()}, nil
}

type Conn struct {
	svc *aztables.ServiceClient
	ctx context.Context
}

func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	pq, err := parseQuery(query)
	if err != nil {
		return nil, err
	}
	return &Stmt{conn: c, pq: pq}, nil
}

func (c *Conn) Close() error { return nil }

func (c *Conn) Begin() (driver.Tx, error) {
	return nil, errors.New("aztablessql: transactions are not supported")
}
