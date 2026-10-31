package relata

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// #5666: cross-SDK shared RFC 9457 fixtures (Go leg). See
// sdks/shared/problem-details-fixtures.json and the Rust live replay test
// (http_e2e::contract_matrix::problem_fixtures_match_live_server) that keeps it faithful.

type problemFixtureCase struct {
	Name string `json:"name"`
	Wire struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	} `json:"wire"`
	Expect struct {
		Kind              string   `json:"kind"`
		Status            int      `json:"status"`
		Code              *string  `json:"code"`
		Type              *string  `json:"type"`
		Retryable         bool     `json:"retryable"`
		RequestID         *string  `json:"request_id"`
		RetryAfterSeconds *float64 `json:"retry_after_seconds"`
	} `json:"expect"`
}

type problemFixtureFile struct {
	Contract string               `json:"contract"`
	Cases    []problemFixtureCase `json:"cases"`
}

func loadProblemFixtures(t *testing.T) problemFixtureFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "shared", "problem-details-fixtures.json"))
	if err != nil {
		t.Fatalf("read shared fixtures: %v", err)
	}
	var f problemFixtureFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse shared fixtures: %v", err)
	}
	return f
}

// Sentinel per fixture kind. The Go SDK has no purpose / bad-request / unmapped-status
// class: those carry no sentinel (Err == nil) and are distinguished by StatusCode and Code.
var problemKindSentinel = map[string]error{
	"auth":                ErrUnauthorized,
	"forbidden":           ErrForbidden,
	"not_found":           ErrNotFound,
	"conflict":            ErrConflict,
	"precondition_failed": ErrPreconditionFailed,
	"validation":          ErrValidation,
	"rate_limited":        ErrRateLimited,
	"server":              ErrServerError,
}

func TestProblemDetailsSharedFixtures(t *testing.T) {
	fx := loadProblemFixtures(t)
	if fx.Contract != "relata.problem-details-fixtures/v1" || len(fx.Cases) < 10 {
		t.Fatalf("unexpected fixture file: %q with %d cases", fx.Contract, len(fx.Cases))
	}
	for _, c := range fx.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.Wire.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.Wire.Status)
				if len(c.Wire.Body) > 0 && c.Wire.Body[0] == '"' {
					var s string
					_ = json.Unmarshal(c.Wire.Body, &s)
					_, _ = w.Write([]byte(s))
					return
				}
				_, _ = w.Write(c.Wire.Body)
			}))
			defer srv.Close()
			client := newTestClient(srv, &ClientOptions{})
			_, err := client.Version(context.Background())
			var re *RelataError
			if !errors.As(err, &re) {
				t.Fatalf("expected *RelataError, got %v", err)
			}
			if re.StatusCode != c.Expect.Status {
				t.Fatalf("status %d want %d", re.StatusCode, c.Expect.Status)
			}
			if want, ok := problemKindSentinel[c.Expect.Kind]; ok {
				if !errors.Is(err, want) {
					t.Fatalf("kind %s: %v does not wrap %v", c.Expect.Kind, err, want)
				}
			} else if re.Err != nil {
				t.Fatalf("kind %s has no Go sentinel but got %v", c.Expect.Kind, re.Err)
			}
			if got := optString(c.Expect.Code); re.Code != got {
				t.Fatalf("code %q want %q", re.Code, got)
			}
			if got := optString(c.Expect.Type); re.TypeURL != got {
				t.Fatalf("type %q want %q", re.TypeURL, got)
			}
			if re.Retryable != c.Expect.Retryable {
				t.Fatalf("retryable %v want %v", re.Retryable, c.Expect.Retryable)
			}
			if got := optString(c.Expect.RequestID); re.RequestID != got {
				t.Fatalf("request id %q want %q", re.RequestID, got)
			}
			var wantRetry time.Duration
			if c.Expect.RetryAfterSeconds != nil {
				wantRetry = time.Duration(*c.Expect.RetryAfterSeconds * float64(time.Second))
			}
			if re.RetryAfter != wantRetry {
				t.Fatalf("retry-after %v want %v", re.RetryAfter, wantRetry)
			}
		})
	}
}

func optString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
