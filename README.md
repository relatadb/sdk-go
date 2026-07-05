# Relata Go SDK

Go client for the [RelataDB](https://github.com/OpenWorkBench-Co/RelataDB) engine.

Module: `github.com/OpenWorkBench-Co/RelataDB/sdks/go`  
Go: 1.22+  
Dependencies: none (stdlib only)

---

## Installation

```bash
go get github.com/OpenWorkBench-Co/RelataDB/sdks/go
```

---

## Quickstart

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/OpenWorkBench-Co/RelataDB/sdks/go/relata"
)

func main() {
    client := relata.New("http://localhost:9090", &relata.ClientOptions{
        BearerToken:    "your-token",
        DefaultPurpose: "investigation",
    })

    result, err := client.Query(context.Background(), "SELECT * FROM Person LIMIT 10")
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("%d rows in %d ms\n", result.RowCount, result.ElapsedMs)
    result.Each(func(row map[string]any) {
        fmt.Println(row)
    })
}
```

---

## Core concept: mandatory purpose

Every Relata query **must** declare a `purpose` token registered in the tenant's
`PurposeRegistry`. The server rejects any query that omits this field (SPECS §5.22.4).

Set a default on the client:

```go
client := relata.New(url, &relata.ClientOptions{
    DefaultPurpose: "investigation",
})
```

Or override per-call:

```go
result, err := client.Query(ctx, sql, relata.WithPurpose("audit"))
```

Calling `Query` without a purpose (and no `DefaultPurpose`) returns
`relata.ErrPurposeRequired` before any network call is made.

---

## QueryBuilder

The fluent `QueryBuilder` composes Relata's SQL extensions ergonomically:

```go
result, err := relata.NewQuery("SELECT * FROM Person").
    Purpose("investigation").
    Where("nationality = 'PK'").
    Where("risk_score > 0.7").
    AsOf("2025-01-01").           // bi-temporal point-in-time
    WithProvenance().             // attach PROV-O lineage columns
    Limit(50).
    Execute(ctx, client)
```

Inspect the generated SQL before executing:

```go
sql := relata.NewQuery("SELECT * FROM Person").
    Where("risk_score > 0.7").
    AsOf("2025-01-01").
    SQL()
// → SELECT * FROM Person WHERE risk_score > 0.7 AS OF '2025-01-01'
```

### Convenience constructors

```go
// Graph traversal: Pregel BFS between two entities
result, err := relata.PathsBetween("entity-A", "entity-B", 4).
    Purpose("investigation").
    Execute(ctx, client)

// Biometric face search
result, err := relata.MatchFace("s3://faces/suspect.jpg", 10).
    Purpose("investigation").
    Execute(ctx, client)

// IdentityIndex resolver (phone, Aadhaar, IMEI, IP, …)
result, err := relata.LookupIdentity("+919876543210").
    Purpose("investigation").
    Execute(ctx, client)

// BM25 + vector hybrid search
result, err := relata.HybridSearch("Person", "Ahmed Khalil Karachi", 25).
    Purpose("analysis").
    Execute(ctx, client)
```

---

## Client API

```go
// Endpoints
client.Health(ctx)        // GET /health        → *HealthResponse
client.Status(ctx)        // GET /status        → *StatusResponse
client.AuditCount(ctx)    // GET /audit/count   → *AuditCountResponse
client.ClusterNodes(ctx)  // GET /cluster/nodes → []ClusterNode
client.Query(ctx, sql, opts...)  // POST /query → *QueryResult
```

`AuditCount` also returns `ErrChainCorrupted` when `ChainValid == false` so
callers who do not inspect the struct still receive an actionable error.

---

## Errors

```go
import "errors"

_, err := client.Query(ctx, sql)
switch {
case errors.Is(err, relata.ErrPurposeRequired):
    // No purpose declared — add WithPurpose or set DefaultPurpose
case errors.Is(err, relata.ErrUnauthorized):
    // Bearer token missing or revoked
case errors.Is(err, relata.ErrForbidden):
    // Cedar ACL denied the query
case errors.Is(err, relata.ErrQuotaExhausted):
    // Per-principal cost-unit quota exceeded
case errors.Is(err, relata.ErrChainCorrupted):
    // Audit hash chain integrity failure — treat as security event
}

// HTTP status + body always available via type assertion
var re *relata.RelataError
if errors.As(err, &re) {
    fmt.Println(re.StatusCode, re.Message)
}
```

---

## ResultSet helpers

```go
// Iterate rows
result.Each(func(row map[string]any) { ... })

// Extract a column as a slice (nil for missing values)
names := result.Column("name") // []any
```

---

## Authentication

Set `BearerToken` in `ClientOptions`:

```go
client := relata.New(url, &relata.ClientOptions{
    BearerToken: os.Getenv("RELATA_BEARER_TOKEN"),
})
```

The token is sent as `Authorization: Bearer <token>` on every request.
Deployments with `RELATA_BEARER_TOKEN` unset on the server accept unauthenticated
requests; set `BearerToken: ""` in that case.

---

## Custom HTTP client

Inject a custom `*http.Client` for mutual TLS, proxies, or distributed tracing:

```go
transport := &http.Transport{TLSClientConfig: tlsCfg}
client := relata.New(url, &relata.ClientOptions{
    HTTPClient: &http.Client{
        Transport: transport,
        Timeout:   45 * time.Second,
    },
})
```

---

## Examples

| Example | Description |
|---|---|
| `examples/basic/` | Health check + simple SELECT |
| `examples/investigation/` | Full FIU investigation workflow (identity → transactions → CDR) |
| `examples/face_search/` | Biometric MATCH_FACE + identity resolution |
| `examples/graph_traversal/` | PATHS_BETWEEN Pregel BFS + edge distribution |
| `examples/audit/` | Daily audit-chain verification + compliance report |

Run any example against a local server:

```bash
./target/debug/relata serve &

go run ./sdks/go/examples/basic \
    -url http://localhost:9090

go run ./sdks/go/examples/investigation \
    -url   http://localhost:9090 \
    -phone "+919876543210"
```

---

## SQL extensions reference

| Clause / Operator | Description |
|---|---|
| `AS OF 'timestamp'` | Bi-temporal point-in-time read (SPECS §5.5) |
| `WITH PROVENANCE` | Attach PROV-O lineage columns to every row (SPECS §5.9) |
| `PATHS_BETWEEN(a, b, max_hops => N)` | Pregel BFS graph traversal (SPECS §5.13) |
| `MATCH_FACE(ref, top_k => N)` | Biometric face similarity search |
| `LOOKUP_IDENTITY(id)` | IdentityIndex resolver (SPECS §5.8) |
| `HYBRID_SCORE(query)` | BM25 + vector combined score (SPECS §5.14) |

---

## Licence

AGPL-3.0-only — see the root `LICENSE` file.
