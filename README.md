# Relata Go SDK

Go client for the [RelataDB](../../README.md) engine. Stdlib-only (no external
dependencies), context-based blocking API, fluent query builder, Mem0-style Memory
client, 15 typed v1.1 clients, and RFC 7807 problem+json-aware errors.

- **Source:** [`sdks/go/relata/`](./relata/)
- **Module:** `github.com/OpenWorkBench-Co/RelataDB/sdks/go`
- **Go:** 1.25+
- **Dependencies:** none (stdlib only — `net/http`, `encoding/json`, `crypto/rand`,
  `context`, `time`)

## Install

```bash
go get github.com/OpenWorkBench-Co/RelataDB/sdks/go
```

## Quick start

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
        DefaultPurpose: "analytics",
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

## Cypher

RelataDB auto-detects Cypher: any query starting with `MATCH` is translated to SQL
before execution. The SDK is language-agnostic — send the string as you would SQL:

```go
result, err := client.Query(ctx, "MATCH (n:Person {id: 'p1'}) RETURN *")
// → SELECT * FROM Person WHERE id = 'p1'
```

Supported: `MATCH` / `OPTIONAL MATCH`, `WHERE`, `RETURN`, `UNION` / `UNION ALL`
(#378), and `CALL traverse.*` / `CALL gds.*` procedures (#377). `CREATE` / `MERGE`
writes route through the governed write door. See the
[SQL reference](https://www.relatadb.dev/docs/reference/sql).

## Agent memory

`Memory` is a Mem0-style surface over the governed `/memory/*` verbs — purpose + ACL
stay on by default. It owns its own transport (it does not take a `*Client`).

```go
m, err := relata.NewMemory("http://localhost:9090", "agent-notes",
    &relata.MemoryOptions{BearerToken: token})
if err != nil {
    log.Fatal(err) // ErrPurposeRequired when purpose is empty
}

id, _ := m.Add(ctx, "Alice prefers dark mode")               // store → memory id
hits, _ := m.Search(ctx, "ui preferences", relata.WithTopK(5)) // recall
dec, _ := m.Forget(ctx, id)                                    // governed retract, not a hard delete
```

The full cognitive-verb surface (`Add`, `AddBatch`, `Search`, `Get`, `Update`,
`Forget`, `Associate`, `Episodes`, `Justify`, `Resolve`, `Summarise`) is documented in
the Memory table below.

## Context & cancellation

Every method takes a `context.Context` as its first argument. Cancel the context to
abort an in-flight request; the SDK surfaces `context.DeadlineExceeded` /
`context.Canceled` as a `*RelataError` wrapping `ErrConnection`. There are no separate
async types — Go's context IS the cancellation mechanism, which is the idiomatic parity
for the Python SDK's sync + async split.

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
result, err := client.Query(ctx, "SELECT * FROM Person")
```

## `Client` — the main client

```go
client := relata.New(baseURL string, opts *relata.ClientOptions) *relata.Client
```

`New` is thread-safe — a single `*Client` may be shared across goroutines (it shares
one `*http.Client`, like `*sql.DB`).

### Methods

| Method | Returns | Description |
|---|---|---|
| `Query(ctx, sql, opts...)` | `(*QueryResult, error)` | Execute a SQL query |
| `Health(ctx)` | `(*HealthResponse, error)` | `GET /health` |
| `Status(ctx)` | `(*StatusResponse, error)` | `GET /status` — profile, role, quota |
| `Stats(ctx)` | `(*Stats, error)` | `GET /debug/stats` — records / states / snapshot_rows / log_leaves / tokens |
| `Version(ctx)` | `(*VersionInfo, error)` | `GET /version` — build info |
| `Ready(ctx)` | `(*ReadyReport, error)` | `GET /health/ready` — 9-condition readiness |
| `AuditCount(ctx)` | `(*AuditCountResponse, error)` | `GET /audit/count` — returns `ErrChainCorrupted` if `ChainValid == false` |
| `ClusterNodes(ctx)` | `([]ClusterNode, error)` | `GET /cluster/nodes` |
| `IngestDocument(ctx, chunksJSONL, manifestJSON)` | `(*IngestDocumentResponse, error)` | `POST /ingest/document` — datagrep-envelope document ingest |

### `ClientOptions`

```go
type ClientOptions struct {
    BearerToken    string            // Authorization: Bearer <token>
    DefaultPurpose string            // applied when WithPurpose is not passed
    Timeout        time.Duration     // per-request timeout (default 30s)
    HTTPClient     *http.Client      // inject for mTLS, proxies, tracing

    // Multi-tenant + delegation (#220, ADR-132)
    Tenant         string            // X-Organization-Id (multi-tenant)
    ActingAs       string            // X-Acting-As (delegation)
    DelegatedBy    string            // X-Delegated-By

    // Static caller headers (win over SDK defaults; X-Request-ID pinned here
    // disables the per-request auto-generation)
    Headers        map[string]string

    // Transport hardening (mirrors _http.py)
    MaxRetries     int               // retry 502/503/504 + network errors (default 0)
    RetryBackoff   time.Duration     // exponential base (default 500ms)
}
```

## Purpose (mandatory)

Every Relata query **must** declare a `purpose` registered in the tenant's
`PurposeRegistry` (SPECS §5.22.4). Calling `Query` without a purpose (and no
`DefaultPurpose`) returns `relata.ErrPurposeRequired` **before any network call is made**.

```go
// Default on the client
client := relata.New(url, &relata.ClientOptions{DefaultPurpose: "investigation"})

// Or override per-call
result, err := client.Query(ctx, sql, relata.WithPurpose("audit"))
```

## Transport hardening

The Go SDK mirrors the Python `_http.py` transport layer exactly:

- **`X-Request-ID`** is auto-generated per request (a v4 UUID via `crypto/rand`, no
  external deps). Pin your own in `Headers["X-Request-ID"]` and that value wins on every
  request — useful for distributed-tracing correlation.
- **Retry** on HTTP 502 / 503 / 504 and network errors, with exponential backoff
  (`RetryBackoff * 2^attempt`). Off by default; opt in via `MaxRetries` (capped at 5
  attempts).
- **RFC 7807 problem+json** parsing — every error carries `Code`, `TypeURL`,
  `Retryable`, `RequestID`, and `RetryAfter` (from the `Retry-After` header).

## Authentication

Set `BearerToken` in `ClientOptions`. The token is sent as
`Authorization: Bearer <token>` on every request. Deployments with
`RELATA_BEARER_TOKEN` unset on the server accept unauthenticated requests.

## Multi-tenant + delegation headers

```go
client := relata.New(url, &relata.ClientOptions{
    BearerToken: token,
    Tenant:      "org-acme",     // X-Organization-Id
    ActingAs:    "user-bob",     // X-Acting-As
    DelegatedBy: "user-alice",   // X-Delegated-By
})
```

## `QueryBuilder` — fluent SQL

```go
result, err := relata.NewQuery("SELECT * FROM Person").
    Purpose("investigation").
    Where("nationality = 'PK'").
    Where("risk_score > 0.7").         // multiple .Where() → AND
    AsOf("2025-01-01").                // bi-temporal point-in-time
    WithProvenance().                  // attach PROV-O lineage columns
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

| Method | Description |
|---|---|
| `.Purpose(p)` | Set the purpose for this query |
| `.Where(predicate)` | Append AND predicate |
| `.AsOf(timestamp)` | Bi-temporal AS OF clause |
| `.WithProvenance()` | Append `WITH PROVENANCE` |
| `.Limit(n)` | Set LIMIT |
| `.Timeout(d)` | Per-request timeout override |
| `.SQL()` | Return assembled SQL string |
| `.Execute(ctx, client)` | Execute against a `*Client` |

### Convenience constructors

```go
// Graph traversal: Pregel BFS between two entities
result, err := relata.PathsBetween("entity-A", "entity-B", 4).
    Purpose("investigation").Execute(ctx, client)

// Biometric face search
result, err := relata.MatchFace("s3://faces/suspect.jpg", 10).
    Purpose("investigation").Execute(ctx, client)

// IdentityIndex resolver (phone, Aadhaar, IMEI, IP, …)
result, err := relata.LookupIdentity("+919876543210").
    Purpose("investigation").Execute(ctx, client)

// BM25 + vector hybrid search
result, err := relata.HybridSearch("Person", "Ahmed Khalil Karachi", 25).
    Purpose("analysis").Execute(ctx, client)
```

## `Memory` — governed agent memory

`NewMemory(baseURL, purpose, opts)` constructs a standalone Memory client (it owns its
own `*http.Client`, mirroring Python where Memory has its own transport). A non-empty
`purpose` is required — governance is always on.

```go
m, err := relata.NewMemory(url, "agent-notes", &relata.MemoryOptions{
    BearerToken: token,
    SessionID:   "sess-1",
})
```

| Method | Returns | Description |
|---|---|---|
| `Add(ctx, content, opts...)` | `(string, error)` | Store a memory → id |
| `AddBatch(ctx, items, opts...)` | `([]string, error)` | Batch store → ids in order |
| `Search(ctx, query, opts...)` | `([]map[string]any, error)` | Recall (confidence × recency × relevance) |
| `Get(ctx, id)` | `(map[string]any, error)` | Fetch one memory (nil if absent) |
| `Update(ctx, id, content)` | `(string, error)` | Supersede → new id |
| `Forget(ctx, id)` | `(map[string]any, error)` | Governed retention-policy retract |
| `Associate(ctx, src, tgt, rel, opts...)` | `(map[string]any, error)` | Link two memories |
| `Episodes(ctx, opts...)` | `([]map[string]any, error)` | List episodes |
| `Justify(ctx, id)` | `(map[string]any, error)` | PROV-O derivation chain |
| `Resolve(ctx, id, opts...)` | `(map[string]any, error)` | Resolve a contradiction |
| `Summarise(ctx, ids, opts...)` | `(map[string]any, error)` | Produce a summary belief |

Per-call options use functional setters: `WithTopK`, `WithConfidence`,
`WithMemoryClass`, `WithSessionID`, `WithAsOf`, `WithPolicy`, `WithSummaryContent`.
`Forget` is a governed retention-policy retract, **not** a hard delete — it returns the
policy decision (`memory_item_id`, `policy`, `forget_at_ns`).

## v1.1 typed clients — `New<Type>FromClient`

Each typed client inherits the parent client's auth, tenant, and purpose context.
Construct with the `New<Type>FromClient(c *relata.Client)` factory, which mirrors the
Python `from_client` classmethod. Optional parameters use struct-pointer options
(`*Options`) matching the `ClientOptions` style — pass `nil` for defaults.

| Module | Constructor | Surface |
|---|---|---|
| `relata.governance` | `NewGovernanceClient` | Rules, Sigma import, retention (holds + WORM), breakglass, alerts, DSAR |
| `relata.mcp` | `NewMcpClient` | 22 MCP tool wrappers + generic `CallTool` + `Initialize`/`ListTools` |
| `relata.a2a` | `NewA2AClient` | A2A task submit/poll + LangGraph checkpoints + agent card |
| `relata.audit` | `NewAuditClient` | Audit entries (filtered/paginated) + signed receipts + PDF export |
| `relata.identity` | `NewIdentityClient` | Identity label/uncertainty + lookup tables + `ERASE SUBJECT` |
| `relata.objects` | `NewObjectClient` | Typed upsert + batch via `/ingest?object_type=` (NDJSON) |
| `relata.ingest` | `NewIngestClient` | Bulk NDJSON + CSV + media status |
| `relata.vectors` | `NewVectorClient` | KNN + hybrid search + similar-to (SQL-backed) |
| `relata.s3` | `NewS3Client` | Native `net/http` wrapper for the S3 protocol door (`HTTP()`) |
| `relata.system` | `NewSystemClient` | LLM config + test + jobs status |
| `relata.streaming` | `NewStreamingClient` | NDJSON `RowIterator` + SSE `SSEIterator` + Arrow `BytesIterator` |
| `relata.tenants` | `NewTenantAdminClient` | Tenant CRUD + quota + sharing agreements + platform admin |
| `relata.backup` | `NewBackupClient` | Backup / restore / PITR (admin-auth) |
| `relata.tokens` | `NewTokenClient` | Dedup / uniqueness tokens (test-and-set, check, revoke, stats) |
| `relata.log` | `NewLogClient` | Ordered integrity log (append, head, load leaves) |

### Example — typed client chain

```go
client := relata.New(url, &relata.ClientOptions{
    BearerToken:    token,
    DefaultPurpose: "compliance",
    Tenant:         "org-acme",
})

mcp := relata.NewMcpClient(client)
gov := relata.NewGovernanceClient(client)

profile, _ := mcp.GetEntityProfile(ctx, "person-123", "compliance")
holds, _ := gov.ListLegalHolds(ctx)
sigma, _ := gov.ImportSigma(ctx, sigmaYAML)
```

### Streaming iterators

The streaming surface uses idiomatic Go iterators (like `*sql.Rows`):

```go
s := relata.NewStreamingClient(client)
it, err := s.QueryRows(ctx, "SELECT * FROM Person", nil) // *RowIterator
if err != nil { log.Fatal(err) }
defer it.Close()
for it.Next() {
    row := it.Row()
    fmt.Println(row["name"])
}
if err := it.Err(); err != nil { log.Fatal(err) }
```

`Watch(ctx, sql, opts)` and `Alerts(ctx, opts)` return `*SSEIterator` with
`.Next()` / `.Event()` / `.Close()` and optional exponential-backoff reconnect.
`QueryArrowRaw(ctx, sql, opts)` returns a `*BytesIterator` that satisfies `io.Reader`
for Arrow IPC consumption.

## Response structs

| Struct | Fields |
|---|---|
| `QueryResult` | `Rows`, `QueryID`, `ElapsedMs`, `RowCount`, `Columns` — normalises the wire shape (`rows`-as-int + `data` array) |
| `HealthResponse` | `Status`, `Profile`, `NodeID` + `IsHealthy()` |
| `StatusResponse` | `Profile`, `Role`, `QueryQuota` |
| `AuditCountResponse` | `Entries`, `ChainValid` + `IsTampered()` |
| `ClusterNode` | `NodeID`, `Role`, `URL` |
| `VersionInfo` | `Version`, `Commit`, `Profile`, `SchemaVersion`, `Features` |
| `Stats` | `Records`, `States`, `SnapshotRows`, `LogLeaves`, `Tokens`, `Raw` (full server response) |
| `ReadyReport` | `IsReady`, `Status`, `Reason`, `Detail` |
| `IngestDocumentResponse` | `ReportID`, `ChunksIngested`, `Warnings`, `SchemaVersion`, `QueueDepth` |
| `BackupRef` | `SnapshotID`, `Kind`, `WrittenAtNS`, `SizeBytes`, `SHA256`, `RowCount`, `Path` |
| `RestoreStatus` | `RestoreID`, `SnapshotID`, `Status`, `RTOActualSecs`, `Error` + `IsCompleted()` / `IsFailed()` |
| `TokenStats` | `Count`, `OldestNS`, `EvictionPolicy` |
| `LogLeaf` | `Index`, `Data`, `CommitNS` |

`QueryResult` is normalised automatically: the server sends
`{"rows": <int>, "data": [...]}` on the wire and the SDK surfaces `Rows` as a slice
regardless.

```go
result, _ := client.Query(ctx, "SELECT * FROM Person LIMIT 10")
for _, row := range result.Rows {        // iterate
    fmt.Println(row["name"])
}
names := result.Column("name")           // []any, nil for missing values
fmt.Printf("%d rows in %d ms\n", result.RowCount, result.ElapsedMs)
```

## Errors

Sentinel errors for the common governance / ops failure modes, plus a typed
`*RelataError` carrying HTTP status, body, and RFC 7807 fields:

```go
import "errors"

_, err := client.Query(ctx, sql)
switch {
case errors.Is(err, relata.ErrPurposeRequired):
    // No purpose declared — add WithPurpose or set DefaultPurpose
case errors.Is(err, relata.ErrUnauthorized):
    // Bearer token missing or revoked (401)
case errors.Is(err, relata.ErrForbidden):
    // Cedar ACL denied the query (403)
case errors.Is(err, relata.ErrNotFound):
    // Resource not found (404)
case errors.Is(err, relata.ErrConflict):
    // Version / uniqueness conflict (409)
case errors.Is(err, relata.ErrValidation):
    // Request body failed validation (422)
case errors.Is(err, relata.ErrRateLimited):
    // Rate limit or quota exhausted (429) — ErrQuotaExhausted is an alias
case errors.Is(err, relata.ErrServerError):
    // 5xx (after retries exhausted)
case errors.Is(err, relata.ErrConnection):
    // Network error / timeout / server unreachable
case errors.Is(err, relata.ErrChainCorrupted):
    // Audit hash chain integrity failure — treat as a security event
}

// HTTP status + RFC 7807 fields via type assertion
var re *relata.RelataError
if errors.As(err, &re) {
    fmt.Println(re.StatusCode, re.Message)   // HTTP 403: Forbidden: policy deny
    fmt.Println(re.Code)                      // RELATA.ACL.DENY
    fmt.Println(re.TypeURL)                   // https://relatadb.dev/errors/forbidden
    fmt.Println(re.RequestID)                 // rid-xyz
    fmt.Println(re.RetryAfter)                // 5s (from Retry-After header)
}
```

`ErrQuotaExhausted` is kept as an alias for `ErrRateLimited` for backward compatibility —
`errors.Is(err, relata.ErrQuotaExhausted)` still matches HTTP 429.

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

## Custom object types

RelataDB is ontology-governed — unknown types are rejected on both ingest and read
(fail-closed). Register custom types via the HTTP surface, then upsert through
`ObjectClient`:

```go
client := relata.New(url, &relata.ClientOptions{BearerToken: tok})
oc := relata.NewObjectClient(client)
oc.Upsert(ctx, "AgentTask", "t-1",
    map[string]any{"task_id": "t-1", "status": "done"}, nil)
```

For ACL access in strict mode, grant via env var: `RELATA_ACL_GRANT=AgentTask:read+write`.

## S3 protocol door

The S3 door is reachable via a native `net/http` wrapper (no boto3, no external S3
library). `NewS3Client(c).HTTP(nil)` returns a `*http.Client` pre-configured with bearer
+ tenant headers; callers issue standard S3 REST verbs against `/<bucket>/<key>`:

```go
s3 := relata.NewS3Client(client)
hc := s3.HTTP(nil)
req, _ := http.NewRequestWithContext(ctx, "PUT",
    s3.BaseURL()+"/acme-intel/report.pdf", bytes.NewReader(data))
resp, err := hc.Do(req)
```

## Examples

| Example | Description |
|---|---|
| [`examples/basic/`](./examples/basic) | Health check + simple SELECT |
| [`examples/investigation/`](./examples/investigation) | Full FIU investigation workflow (identity → transactions → CDR) |
| [`examples/face_search/`](./examples/face_search) | Biometric `MATCH_FACE` + identity resolution |
| [`examples/graph_traversal/`](./examples/graph_traversal) | `PATHS_BETWEEN` Pregel BFS + edge distribution |
| [`examples/audit/`](./examples/audit) | Daily audit-chain verification + compliance report |

Run any example against a local server:

```bash
./target/debug/relata serve &

go run ./sdks/go/examples/basic -url http://localhost:9090

go run ./sdks/go/examples/investigation \
    -url   http://localhost:9090 \
    -phone "+919876543210"
```

## SQL extensions reference

| Clause / Operator | Description |
|---|---|
| `AS OF 'timestamp'` | Bi-temporal point-in-time read (SPECS §5.5) |
| `WITH PROVENANCE` | Attach PROV-O lineage columns to every row (SPECS §5.9) |
| `PATHS_BETWEEN(a, b, max_hops => N)` | Pregel BFS graph traversal (SPECS §5.13) |
| `MATCH_FACE(ref, top_k => N)` | Biometric face similarity search |
| `LOOKUP_IDENTITY(id)` | IdentityIndex resolver (SPECS §5.8) |
| `HYBRID_SCORE(query)` | BM25 + vector combined score (SPECS §5.14) |

See the [SQL reference](https://www.relatadb.dev/docs/reference/sql) for full syntax.

## Environment variables

The SDK reads no environment variables directly — all configuration is explicit on
`ClientOptions` / `MemoryOptions`. The server, however, honours:

| Variable | Effect |
|---|---|
| `RELATA_BEARER_TOKEN` | When unset, the server accepts unauthenticated requests |
| `RELATA_PROFILE` | `free` / `server` / `cluster` deployment profile |
| `RELATA_DOMAIN_PROFILE` | `enterprise` / `lea` / `finint` / `security` / `custom` |
| `RELATA_ACL_GRANT` | `<Type>:read+write` grants for strict-mode ACL |

> `lite` is kept as a silent legacy alias for `free` (ADR-204).

## Testing with an ephemeral server

`relata/ephemeral_test.go` provides `StartEphemeral(t)` — starts a `relata serve`
process on a free port and registers `t.Cleanup` to kill it.

```go
func TestHealth(t *testing.T) {
    srv := StartEphemeral(t)
    resp, err := http.Get(srv.BaseURL + "/health")
    if err != nil || resp.StatusCode != 200 {
        t.Fatalf("health check failed: %v %v", err, resp)
    }
}
```

Set `RELATA_BIN` to the binary path and `RELATA_TEST_TOKEN` to override the
bearer token (defaults: `relata` / `relata-test`).

## License

AGPL-3.0-only — see the root `LICENSE` file.
