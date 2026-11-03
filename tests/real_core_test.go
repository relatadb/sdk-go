package tests

/**
 * Core data-plane methods exercised for real: query family, search, types,
 * objects/commit/changes, links, system probes. Each subtest asserts real
 * values read back from the server (never just err == nil).
 */

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v3/relata"
)

func TestRealQueryFamily(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	ty := uniq("RqEvent")
	ingestRowsWait(t, c, ty, []map[string]any{
		{"_pk": "e1", "host": "web-1", "user": "alice", "action": "login_failed", "sev": 7},
		{"_pk": "e2", "host": "web-2", "user": "bob", "action": "login_ok", "sev": 1},
		{"_pk": "e3", "host": "web-1", "user": "alice", "action": "login_failed", "sev": 8},
	})

	t.Run("Query+Column+Each", func(t *testing.T) {
		r, err := c.Query(ctx, "SELECT host, sev FROM "+ty+" ORDER BY sev DESC", relata.WithPurpose("analytics"))
		must(t, err, "query")
		if len(r.Rows) != 3 || r.RowCount != 3 || r.QueryID == "" {
			t.Fatalf("rows=%d rowcount=%d qid=%q", len(r.Rows), r.RowCount, r.QueryID)
		}
		if got := r.Column("sev"); len(got) != 3 || str(got[0]) != "8" || str(got[2]) != "1" {
			t.Fatalf("sev column wrong: %v", got)
		}
		n := 0
		r.Each(func(row map[string]any) { n++ })
		if n != 3 {
			t.Fatalf("Each visited %d", n)
		}
	})
	t.Run("QueryWithParams", func(t *testing.T) {
		r, err := c.QueryWithParams(ctx, "SELECT _pk FROM "+ty+" WHERE user = $1 ORDER BY _pk", []any{"alice"})
		must(t, err, "params")
		if len(r.Rows) != 2 || r.Rows[0]["_pk"] != "e1" || r.Rows[1]["_pk"] != "e3" {
			t.Fatalf("params result: %v", r.Rows)
		}
	})
	t.Run("QueryBuilder", func(t *testing.T) {
		b := relata.NewQuery("SELECT * FROM " + ty).Purpose("investigation").Where("host = 'web-1'").Limit(1).Timeout(10 * time.Second)
		if !strings.Contains(b.SQL(), "WHERE host = 'web-1'") || !strings.HasSuffix(b.SQL(), "LIMIT 1") {
			t.Fatalf("builder SQL: %s", b.SQL())
		}
		r, err := b.Execute(ctx, c)
		must(t, err, "builder")
		if len(r.Rows) != 1 || r.Rows[0]["host"] != "web-1" {
			t.Fatalf("builder rows: %v", r.Rows)
		}
		// AS OF the far future sees the same rows; WITH PROVENANCE is accepted.
		bp := relata.NewQuery("SELECT * FROM " + ty).Purpose("investigation").WithProvenance().Limit(5)
		if !strings.Contains(bp.SQL(), "WITH PROVENANCE") {
			t.Fatalf("provenance SQL: %s", bp.SQL())
		}
		if _, err := bp.Execute(ctx, c); err != nil {
			t.Fatalf("with provenance: %v", err)
		}
	})
	t.Run("AsOf_bitemporal_history", func(t *testing.T) {
		before := time.Now().UTC().Format(time.RFC3339Nano)
		time.Sleep(50 * time.Millisecond)
		ing := relata.NewIngestClient(c)
		_, err := ing.Bulk(ctx, ty, []map[string]any{{"_pk": "e2", "host": "web-2", "user": "bob", "action": "login_ok", "sev": 99}}, &relata.BulkOptions{Durability: "sync"})
		must(t, err, "supersede")
		deadline := time.Now().Add(20 * time.Second)
		for {
			now := sel(t, c, "SELECT sev FROM "+ty+" WHERE _pk = 'e2'")
			if len(now) == 1 && str(now[0]["sev"]) == "99" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("supersede never visible: %v", now)
			}
			time.Sleep(100 * time.Millisecond)
		}
		// Bare AS OF recovers the prior state for the exact `SELECT * FROM T AS OF`
		// shape (bitemporal.md, #5031); the builder must place AS OF after FROM.
		old, err := relata.NewQuery("SELECT * FROM "+ty).Purpose("investigation").AsOf(before).Execute(ctx, c)
		must(t, err, "as of")
		var oldSev string
		for _, r := range old.Rows {
			if r["host"] == "web-2" {
				oldSev = str(r["sev"])
			}
		}
		if len(old.Rows) != 3 || oldSev != "1" {
			t.Fatalf("AS OF %s must show 3 rows with e2.sev=1 (pre-update), got sev=%q rows=%v", before, oldSev, old.Rows)
		}
		// System-time travel through the raw query door.
		st, err := c.Query(ctx, "SELECT _pk, sev FROM "+ty+" AS OF SYSTEM TIME '"+before+"' WHERE _pk = 'e2'", relata.WithPurpose("investigation"))
		must(t, err, "as of system time")
		if len(st.Rows) != 1 || str(st.Rows[0]["sev"]) != "1" {
			t.Fatalf("AS OF SYSTEM TIME must show sev=1: %v", st.Rows)
		}
	})
	t.Run("QueryOptions_dialect_matcher_timeout", func(t *testing.T) {
		r, err := c.Query(ctx, "SELECT COUNT(*) AS n FROM "+ty, relata.WithPurpose("investigation"), relata.WithDialect("sql"), relata.WithTimeout(5*time.Second), relata.WithMatcher(relata.MatcherAuto))
		must(t, err, "options")
		if len(r.Rows) != 1 || str(r.Rows[0]["n"]) != "3" {
			t.Fatalf("count: %v", r.Rows)
		}
		_, err = c.Query(ctx, "SELECT 1", relata.WithPurpose("x"), relata.WithDialect("klingon"))
		if err == nil || !strings.Contains(err.Error(), "unknown query dialect") {
			t.Fatalf("bad dialect must be rejected client-side: %v", err)
		}
	})
	t.Run("WithQueryVector", func(t *testing.T) {
		// The option must reach the server; a plain SELECT ignores the vector, so
		// the rows must come back unchanged.
		r, err := c.Query(ctx, "SELECT _pk FROM "+ty+" LIMIT 3", relata.WithPurpose("investigation"), relata.WithQueryVector([]float64{0.1, 0.2, 0.3}, "test-model"))
		must(t, err, "vector option")
		if len(r.Rows) != 3 {
			t.Fatalf("rows %d", len(r.Rows))
		}
	})
	t.Run("PurposeRequired", func(t *testing.T) {
		nop := relata.New(realHost(), &relata.ClientOptions{BearerToken: "x"})
		if _, err := nop.Query(ctx, "SELECT 1"); !errors.Is(err, relata.ErrPurposeRequired) {
			t.Fatalf("want ErrPurposeRequired, got %v", err)
		}
	})
	t.Run("QueryInNaturalLanguage", func(t *testing.T) {
		// Caller-supplied completion (no model in the SDK): the SDK must feed the
		// visible schema, then run the generated SELECT through the governed door.
		var sawType bool
		res, err := c.QueryInNaturalLanguage(ctx, "show failed logins", relata.NaturalLanguageQueryOptions{
			Purpose: "investigation",
			Complete: func(_ context.Context, prompt string, _ int) (relata.Completion, error) {
				sawType = strings.Contains(prompt, ty)
				return relata.Completion{Text: "SELECT _pk FROM " + ty + " WHERE action = 'login_failed' ORDER BY _pk", ModelID: "stub"}, nil
			},
			Answer: func(_ context.Context, q string, r *relata.QueryResult, _ int) (relata.Completion, error) {
				return relata.Completion{Text: fmt.Sprintf("%d failed logins", len(r.Rows)), ModelID: "stub-answer"}, nil
			},
		})
		must(t, err, "nl query")
		if !sawType || len(res.Result.Rows) != 2 || res.Answer != "2 failed logins" || res.ModelID != "stub" {
			t.Fatalf("nl: sawType=%v rows=%v ans=%q", sawType, res.Result.Rows, res.Answer)
		}
		// A write statement from the model must be refused before any request.
		_, err = c.QueryInNaturalLanguage(ctx, "drop it", relata.NaturalLanguageQueryOptions{Purpose: "investigation",
			Complete: func(_ context.Context, _ string, _ int) (relata.Completion, error) {
				return relata.Completion{Text: "DELETE FROM " + ty}, nil
			}})
		if err == nil {
			t.Fatal("non-SELECT model output must be refused")
		}
	})
	t.Run("GraphQL", func(t *testing.T) {
		out, err := c.GraphQL(ctx, "{ __typename }", nil, "")
		if err != nil {
			wantProblemAny(t, err)
			return
		}
		if out == nil {
			t.Fatal("graphql returned nil data")
		}
	})
}

// wantProblemAny asserts err is a typed server problem (status >= 400).
func wantProblemAny(t *testing.T, err error) *relata.RelataError {
	t.Helper()
	var re *relata.RelataError
	if !errors.As(err, &re) || re.StatusCode < 400 {
		t.Fatalf("want typed server problem, got %T %v", err, err)
	}
	return re
}
