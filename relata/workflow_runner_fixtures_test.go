package relata

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// #5733: shared workflow-runner step fixtures (Go leg).
//
// sdks/shared/workflow-runner-fixtures.json is replayed by the Python and
// TypeScript SDKs and checked against the Rust relata-workflow-runner request
// shape. One engine response must normalize to the same error class, status,
// retry delay and request count in every consumer.

type wrWire struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
	Reset   bool              `json:"reset"`
}

type wrExpect struct {
	Outcome           string   `json:"outcome"`
	Error             string   `json:"error"`
	Status            *int     `json:"status"`
	RetryAfterSeconds *float64 `json:"retry_after_seconds"`
	Requests          int      `json:"requests"`
}

type wrCase struct {
	Name   string   `json:"name"`
	Step   string   `json:"step"`
	Wire   wrWire   `json:"wire"`
	Expect wrExpect `json:"expect"`
}

type wrFixtures struct {
	Contract string `json:"contract"`
	Requests struct {
		Job struct {
			Method string         `json:"method"`
			Path   string         `json:"path"`
			Body   map[string]any `json:"body"`
		} `json:"job"`
		Query struct {
			SQL     string `json:"sql"`
			Purpose string `json:"purpose"`
		} `json:"query"`
	} `json:"requests"`
	Cases []wrCase `json:"cases"`
}

func loadWorkflowRunnerFixtures(t *testing.T) wrFixtures {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "shared", "workflow-runner-fixtures.json"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var f wrFixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return f
}

func TestWorkflowRunnerFixturesAreTheDocumentedContract(t *testing.T) {
	f := loadWorkflowRunnerFixtures(t)
	if f.Contract != "relata.workflow-runner-fixtures/v1" {
		t.Fatalf("contract = %q", f.Contract)
	}
	seen := map[string]bool{}
	steps := map[string]bool{}
	for _, c := range f.Cases {
		if seen[c.Name] {
			t.Fatalf("duplicate case %q", c.Name)
		}
		seen[c.Name] = true
		steps[c.Step] = true
	}
	if len(f.Cases) < 8 || !steps["job"] || !steps["query"] {
		t.Fatalf("fixture too thin: %d cases, steps %v", len(f.Cases), steps)
	}
}

func TestWorkflowRunnerFixturesNormalizeToTheSharedOutcome(t *testing.T) {
	f := loadWorkflowRunnerFixtures(t)
	kinds := map[string]error{
		"not_found":    ErrNotFound,
		"rate_limited": ErrRateLimited,
		"server_error": ErrServerError,
		"forbidden":    ErrForbidden,
		"connection":   ErrConnection,
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var requests atomic.Int32
			var gotMethod, gotPath string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				gotMethod, gotPath = r.Method, r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				if c.Wire.Reset {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				for k, v := range c.Wire.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.Wire.Status)
				_, _ = w.Write(c.Wire.Body)
			}))
			defer srv.Close()
			client := newTestClient(srv, &ClientOptions{BearerToken: "t", DefaultPurpose: f.Requests.Query.Purpose})

			var err error
			if c.Step == "job" {
				body := f.Requests.Job.Body
				_, err = NewMcpClient(client).CallTool(context.Background(),
					body["name"].(string), body["arguments"].(map[string]any))
			} else {
				_, err = client.Query(context.Background(), f.Requests.Query.SQL)
			}

			if c.Expect.Outcome == "ok" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
			} else {
				if err == nil || !errors.Is(err, kinds[c.Expect.Error]) {
					t.Fatalf("err = %v, want %s", err, c.Expect.Error)
				}
				var re *RelataError
				if !errors.As(err, &re) {
					t.Fatalf("not a *RelataError: %v", err)
				}
				if c.Expect.Status != nil && re.StatusCode != *c.Expect.Status {
					t.Fatalf("status = %d, want %d", re.StatusCode, *c.Expect.Status)
				}
				want := time.Duration(0)
				if c.Expect.RetryAfterSeconds != nil {
					want = time.Duration(*c.Expect.RetryAfterSeconds * float64(time.Second))
				}
				if re.RetryAfter != want {
					t.Fatalf("RetryAfter = %v, want %v", re.RetryAfter, want)
				}
			}
			if int(requests.Load()) != c.Expect.Requests {
				t.Fatalf("requests = %d, want %d: a runner step is never replayed by the transport",
					requests.Load(), c.Expect.Requests)
			}
			if c.Step == "job" {
				if gotMethod != f.Requests.Job.Method || gotPath != f.Requests.Job.Path {
					t.Fatalf("sent %s %s", gotMethod, gotPath)
				}
				want, _ := json.Marshal(f.Requests.Job.Body)
				got, _ := json.Marshal(gotBody)
				if string(want) != string(got) {
					t.Fatalf("body = %s, want %s", got, want)
				}
			}
		})
	}
}
