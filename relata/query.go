package relata

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// QueryOption is a functional option applied to a single Query call.
// Options override the defaults set on ClientOptions.
type QueryOption func(*queryConfig)

// queryConfig holds the per-call settings resolved from QueryOptions.
type queryConfig struct {
	purpose string
	timeout time.Duration
}

// WithPurpose sets the purpose token for a single query call. This overrides
// the DefaultPurpose configured on the Client. The purpose must be registered
// in the tenant's PurposeRegistry.
//
//	client.Query(ctx, sql, relata.WithPurpose("audit"))
func WithPurpose(purpose string) QueryOption {
	return func(cfg *queryConfig) {
		cfg.purpose = purpose
	}
}

// WithTimeout sets a per-query HTTP timeout that overrides the client-level
// timeout. Useful for long-running graph traversal or full-corpus BM25 queries.
//
//	client.Query(ctx, sql, relata.WithTimeout(2*time.Minute))
func WithTimeout(d time.Duration) QueryOption {
	return func(cfg *queryConfig) {
		cfg.timeout = d
	}
}

// QueryBuilder assembles a Relata SQL query using a fluent API and executes it
// against a Client. Use NewQuery to start a builder chain.
//
// Example:
//
//	result, err := relata.NewQuery("SELECT * FROM Person").
//	    Purpose("investigation").
//	    Where("name LIKE 'Ahmed%'").
//	    AsOf("2025-01-01").
//	    WithProvenance().
//	    Limit(20).
//	    Execute(ctx, client)
type QueryBuilder struct {
	baseSQL      string
	purpose      string
	whereClauses []string
	asOf         string
	withProv     bool
	limitVal     int
	timeout      time.Duration
}

// NewQuery creates a QueryBuilder with the given base SQL statement.
// The base SQL may be a complete statement or a fragment that the builder
// will augment with WHERE, AS OF, WITH PROVENANCE, and LIMIT clauses.
//
// Important: if the base SQL already contains a LIMIT clause, calling Limit()
// on the builder appends a second LIMIT which may produce a server error.
// Supply either a fragment or a complete statement — not both.
func NewQuery(sql string) *QueryBuilder {
	return &QueryBuilder{
		baseSQL: sql,
	}
}

// Purpose sets the mandatory purpose token for this query.
// This is equivalent to passing WithPurpose as a QueryOption to client.Query.
func (b *QueryBuilder) Purpose(purpose string) *QueryBuilder {
	b.purpose = purpose
	return b
}

// Where appends a WHERE predicate. Multiple calls are ANDed together.
// If the base SQL already contains a WHERE clause the builder prepends AND
// so the predicates compose correctly.
//
//	relata.NewQuery("SELECT * FROM PhoneCall").
//	    Where("duration_s > 300").
//	    Where("direction = 'outgoing'")
//	// → SELECT * FROM PhoneCall WHERE duration_s > 300 AND direction = 'outgoing'
func (b *QueryBuilder) Where(predicate string) *QueryBuilder {
	b.whereClauses = append(b.whereClauses, predicate)
	return b
}

// AsOf sets the bi-temporal point-in-time clause. timestamp may be an RFC 3339
// string or any format accepted by the Relata SQL parser.
//
//	.AsOf("2025-06-01T00:00:00Z")
//	.AsOf("2025-06-01")   // date-only; interpreted as midnight UTC
func (b *QueryBuilder) AsOf(timestamp string) *QueryBuilder {
	b.asOf = timestamp
	return b
}

// WithProvenance requests PROV-O lineage columns on every result row.
// The returned rows will include additional columns:
//
//	_source, _collector, _method, _confidence, _observed_at, _recorded_at
//
// This maps to the WITH PROVENANCE SQL clause (SPECS §5.9).
func (b *QueryBuilder) WithProvenance() *QueryBuilder {
	b.withProv = true
	return b
}

// Limit sets the maximum number of rows returned. n must be positive.
// This appends "LIMIT n" to the generated SQL.
func (b *QueryBuilder) Limit(n int) *QueryBuilder {
	b.limitVal = n
	return b
}

// Timeout sets a per-execution HTTP timeout, overriding the client default.
func (b *QueryBuilder) Timeout(d time.Duration) *QueryBuilder {
	b.timeout = d
	return b
}

// SQL returns the fully assembled SQL string that will be sent to the server.
// Call this to inspect the generated query before executing it.
func (b *QueryBuilder) SQL() string {
	var sb strings.Builder
	sb.WriteString(strings.TrimRight(b.baseSQL, " \t\n\r;"))

	// WHERE clauses
	if len(b.whereClauses) > 0 {
		upper := strings.ToUpper(b.baseSQL)
		if strings.Contains(upper, " WHERE ") {
			sb.WriteString(" AND ")
		} else {
			sb.WriteString(" WHERE ")
		}
		sb.WriteString(strings.Join(b.whereClauses, " AND "))
	}

	// AS OF — must precede WITH PROVENANCE per the Relata grammar. The
	// timestamp is escaped into a contained literal (#3211).
	if b.asOf != "" {
		sb.WriteString(fmt.Sprintf(" AS OF '%s'", escapeSQLString(b.asOf)))
	}

	// WITH PROVENANCE
	if b.withProv {
		sb.WriteString(" WITH PROVENANCE")
	}

	// LIMIT
	if b.limitVal > 0 {
		sb.WriteString(fmt.Sprintf(" LIMIT %d", b.limitVal))
	}

	return sb.String()
}

// Execute assembles the SQL and executes it against client, returning the
// result set. The purpose must have been set via Purpose(); if it is empty
// and the client has no DefaultPurpose, ErrPurposeRequired is returned.
func (b *QueryBuilder) Execute(ctx context.Context, client *Client) (*QueryResult, error) {
	opts := []QueryOption{}
	if b.purpose != "" {
		opts = append(opts, WithPurpose(b.purpose))
	}
	if b.timeout > 0 {
		opts = append(opts, WithTimeout(b.timeout))
	}
	return client.Query(ctx, b.SQL(), opts...)
}

// ---- convenience constructors for common Relata operator patterns -----------

// PathsBetween builds a PATHS_BETWEEN query between two entity IDs.
// maxHops controls the traversal depth; recommended values are 2–6.
//
//	result, err := relata.PathsBetween("entity-123", "entity-456", 4).
//	    Purpose("investigation").
//	    Execute(ctx, client)
func PathsBetween(fromID, toID string, maxHops int) *QueryBuilder {
	sql := fmt.Sprintf(
		"SELECT * FROM PATHS_BETWEEN('%s', '%s', max_hops => %d)",
		escapeSQLString(fromID), escapeSQLString(toID), maxHops,
	)
	return NewQuery(sql)
}

// MatchFace builds a MATCH_FACE operator query for biometric face search.
// imageRef is a reference understood by the Relata face-search backend
// (e.g. a stored object key or a base64-encoded JPEG fragment).
// topK limits the number of candidate matches returned.
//
//	result, err := relata.MatchFace("s3://faces/suspect-front.jpg", 10).
//	    Purpose("investigation").
//	    AsOf("2025-01-01").
//	    Execute(ctx, client)
func MatchFace(imageRef string, topK int) *QueryBuilder {
	sql := fmt.Sprintf(
		"SELECT * FROM MATCH_FACE('%s', top_k => %d)",
		escapeSQLString(imageRef), topK,
	)
	return NewQuery(sql)
}

// LookupIdentity builds a LOOKUP_IDENTITY query to resolve any canonical
// identifier (phone number, Aadhaar, IMEI, IP address, …) through the
// IdentityIndex.
//
//	result, err := relata.LookupIdentity("+919876543210").
//	    Purpose("investigation").
//	    Execute(ctx, client)
func LookupIdentity(identifier string) *QueryBuilder {
	sql := fmt.Sprintf("SELECT * FROM LOOKUP_IDENTITY('%s')", escapeSQLString(identifier))
	return NewQuery(sql)
}

// HybridSearch builds a HYBRID_SCORE full-text + vector search query against
// the given object type. query is the natural-language search string;
// topK limits results.
//
// #3211: objectType is validated against the identifier allowlist (it lands in
// a FROM clause, so escaping cannot protect it) and query is escaped into a
// contained literal. The error return is a deliberate breaking change — the
// previous signature pasted both raw.
//
//	result, err := relata.HybridSearch("Person", "Ahmed Khalil Karachi", 25).
//	    Purpose("analysis").
//	    Execute(ctx, client)
func HybridSearch(objectType, query string, topK int) (*QueryBuilder, error) {
	if err := validateIdentifier(objectType, "object_type"); err != nil {
		return nil, err
	}
	sql := fmt.Sprintf(
		"SELECT *, HYBRID_SCORE('%s') AS score FROM %s WHERE score > 0 ORDER BY score DESC LIMIT %d",
		escapeSQLString(query), objectType, topK,
	)
	return NewQuery(sql), nil
}
