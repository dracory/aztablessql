# aztablessql

A `database/sql` driver for [Azure Table Storage](https://learn.microsoft.com/en-us/azure/storage/tables/), built on the [Azure SDK for Go `aztables`](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/data/aztables) package.

It lets you use the standard `database/sql` API (`db.Exec`, `db.Query`, prepared statements) against Azure Table Storage, translating a small subset of SQL into Table Storage REST operations.

## Why it's needed?

Azure Table Storage is a cheap, schemaless NoSQL store, but its SDK is REST/OData-flavored and has no SQL surface. That creates friction in a few common situations:

- **Tooling that speaks `database/sql`** — ORMs, query builders, migration helpers, and SQL-based libraries expect a `database/sql` driver. Without one, Table Storage is invisible to that ecosystem.
- **Codebases migrating off SQL databases** — when moving from SQLite/Postgres to Table Storage, rewriting every data-access call to the `aztables` SDK is invasive and error-prone. A SQL dialect lets you keep most query code intact.
- **Quick scripts and one-off tooling** — `db.Query("SELECT * FROM People WHERE ...")` is faster to write than constructing OData filter strings by hand.
- **Familiarity** — teams already fluent in SQL can read and review data-access code without learning the OData filter grammar (`PartitionKey eq 'pk' and RowKey eq 'rk'`, URL-encoding, etc.).

This driver is intentionally a **small subset** of SQL — point reads, simple `AND` filters, and single-entity mutations — mapped onto the operations Table Storage actually supports. It is not a full SQL engine, and the [Supported SQL](#supported-sql) and [What's NOT supported](#whats-not-supported) sections make those limits explicit so you can decide whether it fits your use case.

## Install

```bash
go get github.com/dracory/aztablessql
```

## Quick start

```go
package main

import (
    "database/sql"
    "fmt"
    "log"

    _ "github.com/dracory/aztablessql"
)

func main() {
    // DSN is an Azure Storage connection string.
    // For local development with Azurite, see the "Local development" section below.
    connStr := "DefaultEndpointsProtocol=https;AccountName=...;AccountKey=...;EndpointSuffix=core.windows.net"

    db, err := sql.Open("aztables", connStr)
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    // INSERT — all values must be ? placeholders
    _, err = db.Exec(
        `INSERT INTO People (PartitionKey, RowKey, Name, Age) VALUES (?, ?, ?, ?)`,
        "pk1", "rk1", "Ada Lovelace", 36,
    )
    if err != nil {
        log.Fatal(err)
    }

    // UPSERT — insert or replace (no prior existence check, no race)
    _, err = db.Exec(
        `INSERT OR REPLACE INTO People (PartitionKey, RowKey, Name) VALUES (?, ?, ?)`,
        "pk1", "rk1", "Ada Lovelace",
    )
    if err != nil {
        log.Fatal(err)
    }

    // SELECT — point read (PartitionKey + RowKey)
    rows, err := db.Query(
        `SELECT * FROM People WHERE PartitionKey = ? AND RowKey = ?`,
        "pk1", "rk1",
    )
    if err != nil {
        log.Fatal(err)
    }
    defer rows.Close()

    cols, _ := rows.Columns()
    for rows.Next() {
        vals := make([]interface{}, len(cols))
        ptrs := make([]interface{}, len(cols))
        for i := range vals {
            ptrs[i] = &vals[i]
        }
        if err := rows.Scan(ptrs...); err != nil {
            log.Fatal(err)
        }
        fmt.Println(cols, vals)
    }

    // UPDATE — merge semantics (only SET columns are touched)
    _, err = db.Exec(
        `UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ?`,
        37, "pk1", "rk1",
    )
    if err != nil {
        log.Fatal(err)
    }

    // DELETE
    _, err = db.Exec(
        `DELETE FROM People WHERE PartitionKey = ? AND RowKey = ?`,
        "pk1", "rk1",
    )
    if err != nil {
        log.Fatal(err)
    }
}
```

## Supported SQL

| Statement | Syntax | Notes |
|-----------|--------|-------|
| **INSERT** | `INSERT INTO <table> (col1, col2, ...) VALUES (?, ?, ...)` | All values must be `?` placeholders. `PartitionKey` and `RowKey` columns are required. Fails if the entity already exists. |
| **INSERT OR REPLACE** | `INSERT OR REPLACE INTO <table> (col1, col2, ...) VALUES (?, ?, ...)` | Upsert with replace semantics — if the entity exists, it is fully replaced (properties not in the column list are dropped). Maps to `UpsertEntity` with `UpdateModeReplace`. |
| **INSERT OR MERGE** | `INSERT OR MERGE INTO <table> (col1, col2, ...) VALUES (?, ?, ...)` | Upsert with merge semantics — if the entity exists, only the supplied properties are updated; existing properties are preserved. Maps to `UpsertEntity` with `UpdateModeMerge`. |
| **UPSERT INTO** | `UPSERT INTO <table> (col1, col2, ...) VALUES (?, ?, ...)` | Alias for `INSERT OR REPLACE`. |
| **SELECT** | `SELECT * FROM <table> [WHERE ...]` or `SELECT col1, col2 FROM <table> [WHERE ...]` | Point read when `WHERE PartitionKey = ? AND RowKey = ?` (uses `GetEntity`). Otherwise falls back to `ListEntities` with an OData filter. |
| **UPDATE** | `UPDATE <table> SET col1 = ?, col2 = ? WHERE PartitionKey = ? AND RowKey = ? [AND ETag = ?]` | Merge semantics — only SET columns are touched, existing properties are preserved. `WHERE` must be exactly `PartitionKey = ? AND RowKey = ?`, optionally followed by `AND ETag = ?` for optimistic concurrency. Cannot SET `PartitionKey`, `RowKey`, `ETag`, or `Timestamp`. |
| **DELETE** | `DELETE FROM <table> WHERE PartitionKey = ? AND RowKey = ? [AND ETag = ?]` | `WHERE` must be exactly `PartitionKey = ? AND RowKey = ?`, optionally followed by `AND ETag = ?` for optimistic concurrency. Extra conditions are rejected. |
| **CREATE TABLE** | `CREATE TABLE [IF NOT EXISTS] <table>` | Creates a Table Storage table. `IF NOT EXISTS` makes a duplicate create a no-op (tolerates 409 Conflict). Column definitions are rejected — Table Storage is schemaless, properties are per-entity. |
| **DROP TABLE** | `DROP TABLE [IF EXISTS] <table>` | Deletes a table. `IF EXISTS` makes dropping a missing table a no-op (tolerates 404 Not Found). |
| **SHOW TABLES** | `SHOW TABLES` | Lists all tables on the account. Returns a single `TableName` column. |

### Upsert

Table Storage's `UpsertEntity` inserts the entity if it does not exist, and either replaces or merges it if it does — without a prior existence check. This avoids the race condition inherent in a SELECT-then-INSERT/UPDATE pattern.

- **`INSERT OR REPLACE`** / **`UPSERT INTO`** → replace semantics. The entity is overwritten entirely; properties absent from the column list are removed.
- **`INSERT OR MERGE`** → merge semantics. Only the supplied properties are written; existing properties are left untouched.

Both require `PartitionKey` and `RowKey` in the column list, just like `INSERT`.

### WHERE clause

- Comparison operators: `=`, `!=` (or `<>`), `>`, `>=`, `<`, `<=`
- Right-hand side is a `?` placeholder or a quoted string literal (`'literal'` or `"literal"`)
- Multiple conditions joined by `AND` only (no `OR`)
- Column names are case-sensitive for user properties; `PartitionKey` and `RowKey` are matched case-insensitively
- String literals can contain commas, the word `AND`, and operator characters — the parser is quote-aware
- `<>` is accepted as an alias for `!=` and normalized internally to `ne` (OData has no `<>`)

#### SELECT WHERE

`SELECT` supports all comparison operators. A query is treated as a **point read** (using `GetEntity`) only when the `WHERE` clause is exactly `PartitionKey = ? AND RowKey = ?` with both operators being `=`. Any other predicate — including range scans like `PartitionKey >= 'a' AND PartitionKey < 'b'` or `RowKey > ?` — falls back to `ListEntities` with an OData filter.

```sql
SELECT * FROM People WHERE PartitionKey = ? AND Age > ?
SELECT * FROM People WHERE PartitionKey >= 'a' AND PartitionKey < 'b'
SELECT * FROM People WHERE Name != 'Bob'
```

#### UPDATE / DELETE WHERE

`UPDATE` and `DELETE` are **point operations only**. Their `WHERE` clause must be exactly `PartitionKey = ? AND RowKey = ?` (literals also accepted), and both operators must be `=`. Non-`=` operators on the key columns are rejected at parse time, as are any extra conditions (other than the optional `AND ETag = ?` described below). This is intentional: Table Storage has no conditional range delete, and accepting a non-`=` predicate would silently delete/update the wrong single entity.

### Optimistic concurrency (ETag)

Every entity has a server-managed `ETag` that changes on each write. `UPDATE` and `DELETE` accept an optional `AND ETag = ?` condition in the `WHERE` clause, mapped to the Table Storage `If-Match` header. This enables optimistic concurrency: the operation succeeds only if the entity's current ETag matches the supplied value, preventing lost updates when multiple writers race.

```sql
-- Read the entity to obtain its ETag
SELECT * FROM People WHERE PartitionKey = ? AND RowKey = ?

-- Update only if the entity has not been modified since
UPDATE People SET Age = ? WHERE PartitionKey = ? AND RowKey = ? AND ETag = ?
```

- The ETag value is the opaque string returned by Table Storage (e.g. `W/"datetime'...'"`).
- `ETag = '*'` matches any existing entity — the operation fails (404) if the entity does not exist.
- A mismatched ETag produces a `412 Precondition Failed` error, wrapped with a clearer `aztablessql: ETag precondition failed` message. The original SDK error is preserved via `%w` so `errors.Is`/`errors.As` still work.
- Only one `ETag` condition is allowed; the operator must be `=`.
- `ETag` is not accepted in `INSERT`/`UPSERT` column lists or in `SET` clauses — it is server-managed.

### Pseudo-columns

`ETag` and `Timestamp` are **reserved** server-managed pseudo-columns surfaced in `SELECT` results:

- **`ETag`** — the entity's OData ETag (from the `odata.etag` JSON field). Changes on every write.
- **`Timestamp`** — the entity's last-modified time (ISO-8601 string). Server-managed.

Both are available via `SELECT *` (included in the column set) and via explicit selection (`SELECT ETag, Timestamp FROM ...`). They are read-only:

- Rejected in `INSERT`/`UPSERT` column lists and `UPDATE` `SET` clauses (they are server-managed).
- Rejected in `SELECT` `WHERE` filters — they are not stored, OData-queryable properties, so a filter like `WHERE ETag = ?` would be rejected by the Table Storage service.

`Timestamp` is returned as a `string` for consistency with the JSON-decode-as-is policy; callers can parse it to `time.Time` if needed.

> **Reserved names:** Because `ETag` and `Timestamp` are intercepted by the driver and mapped to entity meta fields, you cannot use them as ordinary property names. If your entity has a stored property that happens to be named `ETag` or `Timestamp`, the driver will surface the server-managed meta-field value instead of the stored property value in `SELECT` results. Avoid using these names for your own properties.

### What's NOT supported

- Joins, subqueries, `ORDER BY`, `LIMIT`, `TOP`, `GROUP BY`
- `OR`, `LIKE`, `IS NULL`, `IN`
- Bare numeric literals in `WHERE` (use a `?` placeholder or a quoted string literal instead)
- Transactions (`BEGIN`/`COMMIT`/`ROLLBACK`) — use the [Batch API](#batch-entity-group-transactions) for atomic multi-entity writes within a single partition

## Batch / Entity Group Transactions

Table Storage supports atomic batches of up to 100 operations **within a single partition** (entity-group transactions). `database/sql` has no native batch-exec concept, so this driver exposes a typed helper reached through `database/sql`'s `Conn.Raw` escape hatch — do the batch work inside the callback, since `Raw` forbids using the driver conn after the callback returns.

```go
conn, err := db.Conn(ctx)
if err != nil { /* ... */ }
defer conn.Close()

ops := []aztablessql.BatchOp{
    {Kind: aztablessql.BatchInsert, Partition: "pk", Row: "r1", Properties: map[string]interface{}{"Name": "Ada", "Age": int64(36)}},
    {Kind: aztablessql.BatchInsert, Partition: "pk", Row: "r2", Properties: map[string]interface{}{"Name": "Bob"}},
    {Kind: aztablessql.BatchDelete, Partition: "pk", Row: "r3"},
}

err = conn.Raw(func(driverConn any) error {
    bc := driverConn.(*aztablessql.Conn).BatchClient("People")
    return bc.SubmitBatch(ctx, ops)
})
```

`BatchKind` values: `BatchInsert`, `BatchInsertMerge`, `BatchInsertReplace`, `BatchUpdateMerge`, `BatchUpdateReplace`, `BatchDelete`.

Each op may carry an optional `ETag` (the special value `"*"` matches any existing ETag). The driver validates client-side that all ops share one `PartitionKey`, that there are at most 100 ops, and that no `RowKey` is targeted twice within a batch — a single failing op rolls back the entire batch atomically. The total payload size limit (~4 MiB per batch) is enforced by the service; oversized batches are rejected server-side.

## Table management (DDL)

The driver supports basic table-level DDL mapped onto `ServiceClient.CreateTable` / `DeleteTable` / `NewListTablesPager`:

```sql
CREATE TABLE People
CREATE TABLE IF NOT EXISTS People
DROP TABLE People
DROP TABLE IF EXISTS People
SHOW TABLES
```

- `CREATE TABLE` / `DROP TABLE` go through `db.Exec` and return a `RowsAffected` of 1 on success, 0 when an `IF NOT EXISTS` / `IF EXISTS` clause turns the call into a no-op.
- `SHOW TABLES` goes through `db.Query` and returns a single `TableName` column with one row per table on the account.
- Column definitions are **rejected** (`CREATE TABLE People (PartitionKey, RowKey, Name)`) with a clear "Table Storage is schemaless" error — properties are per-entity, not per-table.
- Table names are validated by the service (must start with a letter, be 3–63 chars, alphanumeric). The parser stays permissive and lets the server reject invalid names.

## Type handling

### Insert/Update values

Placeholder (`?`) values are mapped to Azure Table Storage EDM types:

| Go type | EDM type | Notes |
|---------|----------|-------|
| `time.Time` | `Edm.DateTime` | Wrapped in `aztables.EDMDateTime` |
| `[]byte` | `Edm.Binary` | Wrapped in `aztables.EDMBinary` |
| `int64` | `Edm.Int64` | Wrapped in `aztables.EDMInt64` |
| `string` | `Edm.String` | Passed through; type inferred by service |
| `bool` | `Edm.Boolean` | Passed through; type inferred by service |
| `float64` | `Edm.Double` | Passed through; type inferred by service |
| `nil` | `null` | Passed through |

For precise integer type control, pass `int64` values. Passing `float64` for a column that was created as `Edm.Int32` may cause the service to store it as `Edm.Double`.

### SELECT return values

Values returned from `SELECT` are whatever `encoding/json` produces when decoding the entity JSON:
- Numbers → `float64`
- Strings → `string`
- Booleans → `bool`
- `null` → `nil`

Use `spf13/cast` or manual type conversion when scanning into typed variables.

## Local development with Azurite

[Azurite](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azurite) is a local emulator for Azure Storage. A `docker-compose.yml` is included:

```bash
docker compose up -d
```

This starts the Azurite Table service on port `10002`. Use this connection string:

```
DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;TableEndpoint=http://127.0.0.1:10002/devstoreaccount1;
```

## Testing

### Unit tests

No external dependencies required:

```bash
go test ./...
```

### Integration tests

Requires the Azurite container to be running:

```bash
docker compose up -d
go test -tags integration -v ./...
```

Integration tests auto-create and drop tables per test for isolation. The connection string can be overridden via the `AZTABLES_TEST_CONNSTR` environment variable.

## CI

A GitHub Actions workflow (`.github/workflows/tests.yml`) runs on push/PR to `main`:
- Starts an Azurite service container
- Runs `go build`, `golangci-lint`, `gosec`
- Runs unit and integration tests with coverage
- Uploads coverage to Codecov

## License

See [LICENSE](LICENSE).
