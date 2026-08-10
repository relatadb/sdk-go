package tests

/**
 * RelataDB Go SDK — smoke test stub.
 *
 * Verifies the SDK package compiles and the client constructs. Full E2E parity
 * with the Python sdk_e2e_test.py harness is tracked at check_sdk_parity.py.
 *
 * Run: go test ./tests/ (from sdks/go/)
 * Needs: a live server at localhost:9090 with RELATA_BEARER_TOKEN=perftoken
 */

import (
	"context"
	"os"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestClientConstructs(t *testing.T) {
	client := relata.New(
		getEnv("RELATA_HOST", "http://localhost:9090"),
		&relata.ClientOptions{BearerToken: getEnv("RELATA_TOKEN", "perftoken")},
	)
	if client == nil {
		t.Fatal("client should not be nil")
	}
}

func TestHealthAndQuery(t *testing.T) {
	client := relata.New(
		getEnv("RELATA_HOST", "http://localhost:9090"),
		&relata.ClientOptions{
			BearerToken: getEnv("RELATA_TOKEN", "perftoken"),
			Timeout:     10 * time.Second,
		},
	)
	result, err := client.Query(context.Background(), "SELECT 1", relata.WithPurpose("analytics"))
	if err != nil {
		t.Skipf("server not reachable: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
}
