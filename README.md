# Relata Go SDK

Go client for the [Relata DB](https://relatadb.dev) engine. Stdlib-only (no external
dependencies), context-based blocking API, fluent query builder, Mem0-style Memory
client, 15 typed v1.1 clients, and RFC 7807 problem+json-aware errors.

- **Repo:** [github.com/relatadb/sdk-go/v3](https://github.com/relatadb/sdk-go/v3)
- **Module:** `github.com/relatadb/sdk-go/v3`
- **Docs:** [relatadb.dev](https://relatadb.dev) · [Issues](https://github.com/relatadb/sdk-go/v3/issues)
- **Go:** 1.25+
- **Dependencies:** none (stdlib only — `net/http`, `encoding/json`, `crypto/rand`,
  `context`, `time`)
- **By:** [ZySec AI](https://zysec.ai) — Frontier Sovereign Intelligence · hello@zysec.ai

For the v3.0 platform boundary and SDK migration plan, see the
[SDK migration guide](../README-v3-migration.md).
Go is one of the three supported consumer SDKs (Python, TypeScript, Go).
Other languages use the HTTP API and its OpenAPI reference at `GET /openapi.json`.

## Install

```bash
go get github.com/relatadb/sdk-go/v3
```

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/relatadb/sdk-go/v3/relata"
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

## Hybrid search — `VectorClient.HybridSearch`

`VectorClient` (below) wraps the server's `HYBRID_SEARCH` operator (ADR-175):
supply `queryText` (BM25 leg), `queryEmbedding` + `embeddingSlot` (vector
leg), or both — when both are present the server fuses the two rankings via
reciprocal rank fusion. This is Relata's genuine edge over a plain vector DB
or a plain full-text engine. (The `relata.HybridSearch(...)` convenience
constructor shown above under "Convenience constructors" is BM25-only — use
`VectorClient.HybridSearch` for the fused vector+text form.)

```go
vectors := relata.NewVectorClient(client)

emb, err := vectors.Embed(ctx, "graph retrieval", "")
if err != nil {
    log.Fatal(err)
}

hits, err := vectors.HybridSearch(ctx, "Document", "graph retrieval", emb.Embedding, "_emb_text",
    &relata.HybridSearchOptions{K: 10})
for _, row := range hits {
    fmt.Println(row["title"], row["_score"])
}
```

See `examples/hybrid_search/main.go` for a full runnable walkthrough (embed →
ingest → BM25-only vs. vector-only vs. fused search).

## Agent memory

`Memory` is a Mem0-style surface over the governed `/v1/memory/*` verbs — purpose + ACL
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
| `Status(ctx)` | `(*StatusResponse, error)` | `GET /v1/system/status` — profile, role, quota |
| `Stats(ctx)` | `(*Stats, error)` | `GET /v1/system/stats` — records / states / snapshot_rows / log_leaves / tokens |
| `Version(ctx)` | `(*VersionInfo, error)` | `GET /version` — build info |
| `Ready(ctx)` | `(*ReadyReport, error)` | `GET /health/ready` — 9-condition readiness |
| `AuditCount(ctx)` | `(*AuditCountResponse, error)` | `GET /v1/audit/count` — returns `ErrChainCorrupted` if `ChainValid == false` |
| `IngestDocument(ctx, objects, purpose)` | `(*IngestDocumentResponse, error)` | Queues prepared mixed-type rows via `POST /v1/ingest/bulk` (`queued: true`) and returns one process-local task receipt |
| `IngestDocumentStatus(ctx, taskID)` | `(*IngestDocumentTaskStatus, error)` | Polls `GET /v1/ingest/tasks/:task_id` for terminal complete/partial/failed/uncertain/incomplete state and confirmed written/dropped row counts |

`IngestDocument` takes application-prepared governed rows (for example a
`DocumentSource`, linked `DocumentChunk` rows, and optional evidence rows with
`_type` and stable `_pk` fields). It does not parse raw documents or datagrep
extractor files. Parsing extractor output into an `objects` array is OpenETL's job
(the former `relata-connectors prepare-document` adapter was removed).
Queue admission is process-local and not crash-durable; poll
`IngestDocumentStatus` for terminal complete/partial/failed/uncertain/incomplete state and confirmed written/dropped row counts. `Code` and `DerivedError` can be present while `Status` remains pending; after all primary batches are accounted for, the status becomes incomplete. If the primary outcome is uncertain, uncertain takes precedence. A recorded derived-write failure has `Retryable` false and no completion watermark. Uncertain outcomes are unresolved and must not be blindly retried. The separate MCP
`ingest_document(source, text, ...)` tool remains a direct application workflow.

### `ClientOptions`

```go
type ClientOptions struct {
    BearerToken    string            // Authorization: Bearer <token>
    DefaultPurpose string            // applied when WithPurpose is not passed
    Timeout        time.Duration     // per-request timeout (default 30s)
    HTTPClient     *http.Client      // inject for mTLS, proxies, tracing

    // Multi-tenant + delegation (#220, ADR-132)
    Tenant         string            // X-Relata-Tenant-Id (multi-tenant)
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
    Tenant:      "org-acme",     // X-Relata-Tenant-Id
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
// → SELECT * FROM Person AS OF '2025-01-01' WHERE risk_score > 0.7
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

## HTTP Arrow IPC

For columnar query results, `StreamingClient.QueryArrowRaw` returns an
`io.Reader` over the bytes from `POST /v1/query/arrow`. Pass that reader to an
Arrow IPC reader from the application you use for analysis.

```go
streaming := relata.NewStreamingClient(client)
reader, err := streaming.QueryArrowRaw(ctx,
    "SELECT * FROM Person LIMIT 1000",
    &relata.StreamingQueryOptions{Purpose: "analytics"},
)
if err != nil {
    log.Fatal(err)
}
defer reader.Close()

out, err := os.Create("query.arrow")
if err != nil {
    log.Fatal(err)
}
defer out.Close()
if _, err := io.Copy(out, reader); err != nil {
    log.Fatal(err)
}
```

The Go SDK does not decode Arrow IPC into a typed table. `POST /ingest/media`
and its status route were removed (#5942, they answer `410 media-ingest-removed`),
and so were `IngestClient.MediaStatus` and `McpClient.IngestMedia`. Media is
uploaded through the Files API with `process=true` and followed with
`MediaWorkClient`; the separate `FilesClient` below wraps `/v1/files/content`.

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
| `SearchDetailed(ctx, query, opts...)` | `(RecallResult, error)` | Like `Search` but returns the full envelope, incl. read-only `RecallCostTokens`/`Cancelled` |
| `SearchStream(ctx, query, opts...)` | `(io.ReadCloser, error)` | Recall as an SSE stream (`GET /v1/memory/recall/stream`); caller closes |
| `RememberProcedure(ctx, agentID, name, text)` | `(map[string]any, error)` | `POST /v1/memory/remember-procedure` |
| `RecallProcedure(ctx, agentID, opts)` | `(map[string]any, error)` | `GET /v1/memory/recall-procedure` |
| `Get(ctx, id)` | `(map[string]any, error)` | Fetch one memory (nil if absent) |
| `Update(ctx, id, content)` | `(string, error)` | Supersede → new id |
| `Forget(ctx, id)` | `(map[string]any, error)` | Governed retention-policy retract |
| `Associate(ctx, src, tgt, rel, opts...)` | `(map[string]any, error)` | Link two memories |
| `Episodes(ctx, opts...)` | `([]map[string]any, error)` | List episodes |
| `Justify(ctx, id)` | `(map[string]any, error)` | PROV-O derivation chain |
| `Resolve(ctx, id)` | `(map[string]any, error)` | Resolve a supersession chain to its canonical head |
| `Summarise(ctx, ids, opts...)` | `(map[string]any, error)` | Produce a summary belief |

Per-call options use functional setters: `WithTopK`, `WithConfidence`,
`WithMemoryClass`, `WithSessionID`, `WithAsOf`, `WithSummaryContent`.
`Forget` is a governed retention-policy retract, **not** a hard delete — it returns the
policy decision (`memory_item_id`, `policy`, `forget_at_ns`).

`Search`/`SearchDetailed` also take the ADR-145 retrieval-quality operators
(#2674) as `MemoryOption`s: `WithMinConfidence`, `WithRecencyHalfLife`,
`WithBudgetTokens`, `WithStabilityDays`, `WithCancelThreshold`. Example:

```go
result, err := m.SearchDetailed(ctx, "ui preferences",
    relata.WithMinConfidence(0.5), relata.WithBudgetTokens(200))
// result.RecallCostTokens / result.Cancelled surface BUDGET / CANCEL_WHEN effects.
```

## v1.1 typed clients — `New<Type>FromClient`

Each typed client inherits the parent client's auth, tenant, and purpose context.
Construct with the `New<Type>FromClient(c *relata.Client)` factory, which mirrors the
Python `from_client` classmethod. Optional parameters use struct-pointer options
(`*Options`) matching the `ClientOptions` style — pass `nil` for defaults.

| Module | Constructor | Surface |
|---|---|---|
| `relata.governance` | `NewGovernanceClient` | Prepared rules, retention (holds + WORM), breakglass, alerts, DSAR, GDPR `Erase`/`DeletionStatus`/`VerifyReceipt` |
| `relata.mcp` | `NewMcpClient` | 22 MCP tool wrappers + generic `CallTool` + `Initialize`/`ListTools` |
| `relata.a2a` | `NewA2AClient` | A2A task submit/poll + LangGraph checkpoints + agent card |
| `relata.audit` | `NewAuditClient` | Audit count + filtered audit log (`GET /v1/audit/log`) |
| `relata.identity` | `NewIdentityClient` | Identity label/uncertainty + lookup tables + `ERASE SUBJECT` |
| `relata.objects` | `NewObjectClient` | Typed upsert + batch via `/v1/ingest?object_type=` (NDJSON) |
| `relata.ingest` | `NewIngestClient` | Bulk NDJSON + CSV + media task status; it does not upload raw media bytes |
| `relata.files` | `NewFilesClient` | Buffered Files `Put`/`Get`/`Head`/`List`/`Delete`, `MediaContent`; byte content, ETag and content type metadata |
| `relata.vectors` | `NewVectorClient` | KNN + hybrid search + similar-to (SQL-backed) + `Embed`/`EmbedBatch` (`POST /v1/embed` with `text` / `texts`) |
| `relata.search` (`SearchClient`) | `NewSearchClient` | Typed `POST /v1/search` JSON query door (`rank_by`/`filters`) — separate from the untyped `Client.Search` |
| `relata.system` | `NewSystemClient` | Jobs status and notification rules (`/v1/notification-rules`; a webhook is a delivery channel of a rule via `webhook_url`/`webhook_headers`); no feed broker routing |
| `relata` (`FeedBrokerClient`) | `NewFeedBrokerClient` | Optional RIFN broker health, channels, signed-entry publishing at an explicit broker URL |
| `relata.streaming` | `NewStreamingClient` | NDJSON `RowIterator` + SSE `SSEIterator` + Arrow `BytesIterator` |
| `relata.tenants` | `NewTenantAdminClient` | Caller's own tenant record (`Me`, `GET /v1/tenants/me`) |
| `relata.backup` | `NewBackupClient` | Backup / restore / PITR (admin-auth) |
| `relata.tokens` | `NewTokenClient` | Tenant self-service tokens: `ListSelf`, `SelfAudit`, `RotateSelf`, `RevokeSelf` (`/v1/auth/tokens/self`) |

The former `get_domain_summary` MCP route is not a core tool in v3.0. To build
the same application-facing inventory, call generic, tenant-scoped
`mcp.ListEntityTypes(ctx)` and pass its result to
`relata.SummarizeDomain("financial", inventory)`. That helper supplies local
profile metadata; it performs no extra query or authorization check.

```go
package main

import (
    "context"
    "fmt"
    "github.com/relatadb/sdk-go/v3/relata"
)

func main() {
    ctx := context.Background()
    db := relata.New("http://localhost:9090", &relata.ClientOptions{DefaultPurpose: "analytics"})
    defer db.Close()
    mcp := relata.NewMcpClient(db)
    inventory, err := mcp.ListEntityTypes(ctx)
    if err != nil { panic(err) }
    summary, err := relata.SummarizeDomain("financial", inventory)
    if err != nil { panic(err) }
    fmt.Printf("%v\n", summary)
}
```

RIFN is an optional broker service with its own URL, separate from the Relata
database listener. Construct `NewFeedBrokerClient` with that explicit broker
URL and its own `ClientOptions`. `Publish` sends a complete, caller-signed
feed entry plus the required `X-Publisher-Did`; the SDK does not create or
sign entries, and `NewSystemClient` does not inherit or configure a broker.

**Admin-listener reachability (`ClientOptions.AdminBaseURL`, #2321):** on a
hardened `server`/`cluster` deployment, `/admin/*` is
mounted only on the loopbound admin control-plane listener
(`RELATA_ADMIN_BIND`, default `127.0.0.1:9091` — ADR-0261), never the main
data-plane `BaseURL`. Set `AdminBaseURL` when constructing the `Client` so
`NewBackupClient` reaches those routes. Leave it unset (the default) on the free profile, where the
admin listener isn't split from the data plane. A bare 404 from an
admin-only route with no error detail is rewritten into a hint
pointing at this option instead of a confusing plain 404.

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
rule, _ := gov.CreateRule(ctx, map[string]any{
    "name": "high-transfer",
    "condition_sql": "amount_usd > 1000000",
    "target_type": "Transaction",
    "severity": "high",
}, nil)
```

Sigma YAML conversion is an optional application workflow, not an SDK method.
Convert Sigma YAML in OpenETL (the former `relata-connectors import-sigma` was removed),
or convert externally and submit prepared rule
JSON with `GovernanceClient.CreateRule`. The v3 SDK no longer sends raw YAML to
the database listener or MCP `import_sigma` tool; `POST /v1/rules/sigma` answers `410 sigma-import-removed`. What a native rule can express is in the [Sigma target contract](../../docs/src/openetl-handoff/sigma-target-contract.md).

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

`QueryRows` uses `/v1/query/stream` with NDJSON. It yields data rows and validates
leading and terminal metadata. Always check `Err()` after iteration: missing,
truncated, malformed or count-mismatched completion returns
`*relata.IncompleteQueryStreamError` with `QueryID` and `RowsReceived`. Partial
rows are not a complete answer, and the iterator never retries automatically.
`MaxResponseBytes` bounds each NDJSON frame (default 64 MiB), not total stream
length. Completion, failure and caller `Close()` release the response body.

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
| `VersionInfo` | `Version`, `Commit`, `Profile`, `SchemaVersion`, `Features` |
| `Stats` | `Records`, `States`, `SnapshotRows`, `LogLeaves`, `Tokens`, `Raw` (full server response) |
| `ReadyReport` | `IsReady`, `Status`, `Reason`, `Detail` |
| `IngestDocumentResponse` | `TaskID`, `RowsQueued`, `RowsRejected`, `Errors`, `QueueDepth` |
| `BackupRef` | `SnapshotID`, `Kind`, `WrittenAtNS`, `SizeBytes`, `SHA256`, `RowCount`, `Path` |
| `RestoreStatus` | `RestoreID`, `SnapshotID`, `Status`, `RTOActualSecs`, `Error` + `IsCompleted()` / `IsFailed()` |

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
    fmt.Println(re.Code)                      // access-denied
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

## Governed Files client — v3.0 candidate, SDK 2.7.1

For the v3.0 candidate (engine source `57e11676c`), version-aligned SDK source
`8924d6ce8` builds cleanly and Go tests pass in two packages. The five-lane
Files fixture passes in 10.020s; the prepared-document matrix passes in 37.110s
across raw HTTP and official TypeScript, Python sync/async, and Go queries. It
checks synthetic DataGrep and Unstructured JSON citation anchors, bounded ingest
receipts, auth/tenant behavior and a persistent single-node restart. Citation
comparison waits for stable index visibility; TypeScript and Python required
two before/after stabilization attempts for DataGrep, so this does not promise
immediate parity. See the [Files result](../../scripts/qualification/followup_pr5786_sdk_files_common_live.json)
and [prepared result](../../scripts/qualification/followup_pr5786_sdk_files_prepared_live.json).
This candidate-source evidence does not claim package publication, PDF/OCR or
provider extraction, replicas, throughput, interrupted-task resumption, or
server-side cancellation. Prepared receipts describe historical local task
outcomes; volatile profiles remain process-local.

`NewFilesClient(client)` borrows the parent's auth, tenant, timeout and response
cap. `Put(ctx, key, data, opts)` sends raw bytes to `PUT /v1/files/content` and
returns current-file metadata; `Get` returns buffered bytes plus ETag/content
type, and `Delete` acknowledges the exact-key `DELETE /v1/files/content`. A blank
bucket uses the authenticated tenant default; `FilesOptions` can select a
bucket and upload content type. The server upload limit is 128 MiB; downloads
are limited by `ClientOptions.MaxResponseBytes` (64 MiB by default). PUT and
DELETE are not automatically retried, and uncertain outcomes require caller
reconciliation. This client does not provide immutable versions, conditional
writes, ranges, physical-erasure guarantees, or media processing.
The caller's `ctx` cancels the client request; it does not promise that server
work already accepted by the engine is aborted or rolled back. See the
[HTTP Files contract](../../docs/src/specs/files-api.md) for server behavior.
This client wraps only `/v1/files/content`; it does not wrap the Files list or
folder operations.

## File-ingest limits

`POST /ingest/media` was removed (#5942); poll OpenETL work items with
`MediaWorkClient.Get`. `FilesClient` transfers
opaque tenant-owned content through `/v1/files/content`; it does not invoke media
processing or document extraction. Use the deployment's supported connector
integration when source-file transfer must also trigger ingestion.

`ObjectClient.List`/`.Get` (present in the TypeScript SDK) are intentionally **not**
ported — see the doc comment on `ObjectClient` in `relata/objects.go` for why (the
server has no GET handler for `/v1/objects/{type}(/{id})`, and the internal Rust
reference client and Python SDK both lack them too).

## Examples

| Example | Description |
|---|---|
| [`examples/basic/`](./examples/basic) | Health check + simple SELECT |
| [`examples/investigation/`](./examples/investigation) | Full FIU investigation workflow (identity → transactions → CDR) |
| [`examples/hybrid_search/`](./examples/hybrid_search) | `VectorClient.HybridSearch` — embed, ingest, BM25-only vs. vector-only vs. fused (RRF) search |
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
| `LOOKUP_IDENTITY(id)` | IdentityIndex resolver (SPECS §5.8) |
| `HYBRID_SEARCH FROM <Type> QUERY '<text>' LIMIT n` | BM25 + vector combined retrieval (SPECS §5.14) |

See the [SQL reference](https://www.relatadb.dev/docs/reference/sql) for full syntax.

## Environment variables

The SDK reads no environment variables directly — all configuration is explicit on
`ClientOptions` / `MemoryOptions`. The server, however, honours:

| Variable | Effect |
|---|---|
| `RELATA_BEARER_TOKEN` | When unset, the server accepts unauthenticated requests |
| `RELATA_HTTP_BIND` | Bind address; a non-loopback bind makes the node network-facing (token required) |
| `RELATA_DOMAIN_PROFILE` | `enterprise` / `lea` / `finint` / `security` / `custom` |
| `RELATA_ACL_GRANT` | `<Type>:read+write` grants for strict-mode ACL |

> `RELATA_PROFILE` accepts only `free` (the default). `server`, `cluster` and `lite` are rejected at startup (#5809).

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
