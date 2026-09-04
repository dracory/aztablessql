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
| **UPDATE** | `UPDATE <table> SET col1 = ?, col2 = ? WHERE PartitionKey = ? AND RowKey = ?` | Merge semantics — only SET columns are touched, existing properties are preserved. `WHERE` must be exactly `PartitionKey = ? AND RowKey = ?`. Cannot SET `PartitionKey` or `RowKey`. |
| **DELETE** | `DELETE FROM <table> WHERE PartitionKey = ? AND RowKey = ?` | `WHERE` must be exactly `PartitionKey = ? AND RowKey = ?`. Extra conditions are rejected. |

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

`UPDATE` and `DELETE` are **point operations only**. Their `WHERE` clause must be exactly `PartitionKey = ? AND RowKey = ?` (literals also accepted), and both operators must be `=`. Non-`=` operators on the key columns are rejected at parse time, as are any extra conditions. This is intentional: Table Storage has no conditional range delete, and accepting a non-`=` predicate would silently delete/update the wrong single entity.

### What's NOT supported

- Joins, subqueries, `ORDER BY`, `LIMIT`, `TOP`, `GROUP BY`
- `OR`, `LIKE`, `IS NULL`, `IN`
- Bare numeric literals in `WHERE` (use a `?` placeholder or a quoted string literal instead)
- Transactions (`BEGIN`/`COMMIT`/`ROLLBACK`)
- Optimistic concurrency (ETag / `If-Match`)
- Batch operations

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
