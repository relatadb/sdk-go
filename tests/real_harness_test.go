package tests

/**
 * Shared harness for the "real use" Go SDK suite (docs/testing/sdk-real/go).
 * Every test in real_*_test.go drives a live server through the published SDK
 * surface only (no mocks, no recorded responses) and skips when no server is
 * configured, so `go test ./...` stays server-free.
 *
 *   RELATA_HOST         data-plane URL           (default http://localhost:9090)
 *   RELATA_TOKEN        registry data token      (required, else skip)
 *   RELATA_ADMIN_HOST   loopback admin listener  (default http://localhost:9091)
 *   RELATA_ADMIN_TOKEN  admin bearer             (admin-plane tests skip without)
 */

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

var uniqCounter atomic.Int64

// uniq returns a name unique to this process run (type names are global state).
func uniq(prefix string) string {
	return fmt.Sprintf("%s%d%d", prefix, time.Now().UnixNano()%1_000_000_000, uniqCounter.Add(1))
}

func realHost() string { return getEnv("RELATA_HOST", "http://localhost:9090") }

func realClient(t *testing.T, mutate ...func(*relata.ClientOptions)) *relata.Client {
	t.Helper()
	tok := os.Getenv("RELATA_TOKEN")
	if tok == "" {
		t.Skip("RELATA_TOKEN not set; live SDK run skipped")
	}
	o := &relata.ClientOptions{BearerToken: tok, DefaultPurpose: "investigation", Timeout: 30 * time.Second}
	for _, m := range mutate {
		m(o)
	}
	return relata.New(realHost(), o)
}

func adminClient(t *testing.T) *relata.Client {
	t.Helper()
	tok := os.Getenv("RELATA_ADMIN_TOKEN")
	if tok == "" {
		t.Skip("RELATA_ADMIN_TOKEN not set; admin-plane test skipped")
	}
	return relata.New(realHost(), &relata.ClientOptions{
		BearerToken: tok, DefaultPurpose: "investigation", Timeout: 30 * time.Second,
		AdminBaseURL: getEnv("RELATA_ADMIN_HOST", "http://localhost:9091"),
	})
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

func must(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantProblem asserts err is a *RelataError with the given HTTP status and
// (when non-empty) a problem code containing codeSub; returns it.
func wantProblem(t *testing.T, err error, status int, codeSub string) *relata.RelataError {
	t.Helper()
	var re *relata.RelataError
	if !errors.As(err, &re) {
		t.Fatalf("want *RelataError HTTP %d, got %T %v", status, err, err)
	}
	if re.StatusCode != status {
		t.Fatalf("want HTTP %d got %d (%s / %s)", status, re.StatusCode, re.Code, re.Message)
	}
	if codeSub != "" && !strings.Contains(strings.ToLower(re.Code), strings.ToLower(codeSub)) {
		t.Fatalf("want problem code ~%q got %q (%s)", codeSub, re.Code, re.Message)
	}
	return re
}

func sel(t *testing.T, c *relata.Client, sql string) []map[string]any {
	t.Helper()
	r, err := c.Query(ctxT(t), sql, relata.WithPurpose("investigation"))
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return r.Rows
}

// waitRows polls until the SELECT returns at least want rows (ingest is async).
func waitRows(t *testing.T, c *relata.Client, sql string, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		rows := sel(t, c, sql)
		if len(rows) >= want {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q: want >=%d rows, got %d after 30s", sql, want, len(rows))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func str(v any) string { return fmt.Sprint(v) }

// ingestRows loads rows of objectType and waits until they are queryable.
func ingestRowsWait(t *testing.T, c *relata.Client, ty string, rows []map[string]any) {
	t.Helper()
	ing := relata.NewIngestClient(c)
	if _, err := ing.Bulk(ctxT(t), ty, rows, &relata.BulkOptions{Durability: "sync"}); err != nil {
		t.Fatalf("ingest %s: %v", ty, err)
	}
	waitRows(t, c, "SELECT * FROM "+ty, len(rows))
}

// mintToken mints a registry token on the loopback admin listener (the only
// supported way to obtain role tokens: schema-admin, platform-admin,
// tenant-admin, oversight, auditor, media-processor). role "" = ordinary data
// token. Optional tenant scopes it. The admin listener is plain HTTP and there
// is no SDK method for minting, so this one call uses net/http directly.
func mintToken(t *testing.T, role, tenant string) string {
	t.Helper()
	admin := os.Getenv("RELATA_ADMIN_TOKEN")
	if admin == "" {
		t.Skip("RELATA_ADMIN_TOKEN not set; token minting skipped")
	}
	secret, status, raw := tryMint(admin, role, tenant)
	if secret == "" {
		t.Fatalf("mint %s: HTTP %d %s", role, status, raw)
	}
	return secret
}

// tryMint is mintToken without the test plumbing: it returns "" on any failure.
func tryMint(admin, role, tenant string) (secret string, status int, raw []byte) {
	body := map[string]any{"description": "sdk-real-" + role}
	if role != "" {
		body["role"] = role
	}
	if tenant != "" {
		body["tenant_id"] = tenant
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", getEnv("RELATA_ADMIN_HOST", "http://localhost:9091")+"/admin/tokens", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	raw, _ = io.ReadAll(resp.Body)
	var out struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Secret, resp.StatusCode, raw
}

// roleClient returns a client authenticated with a freshly minted role token.
func roleClient(t *testing.T, role string, mutate ...func(*relata.ClientOptions)) *relata.Client {
	t.Helper()
	tok := mintToken(t, role, "")
	o := &relata.ClientOptions{BearerToken: tok, DefaultPurpose: "investigation", Timeout: 30 * time.Second}
	for _, m := range mutate {
		m(o)
	}
	return relata.New(realHost(), o)
}
