package relata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// pauseBody is the router-wide admission-pause 503 body (#5876, #6302).
func pauseBody(reason string, retryable bool) string {
	return fmt.Sprintf(`{"type":"about:blank","title":"Service Unavailable","status":503,`+
		`"detail":"a backup snapshot, whole-store restore or generation rollover is briefly pausing this node; retry shortly",`+
		`"retryable":%t,"retry_after_secs":0,"pause_reason":%q}`, retryable, reason)
}

// pausingServer answers `pauses` maintenance-pause 503s with body, then 200.
func pausingServer(t *testing.T, pauses int32, body string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= pauses {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"query_id":"q","row_count":0,"columns":[],"rows":[]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// #6302: a POST refused by a maintenance pause is resent and succeeds, with
// MaxRetries left at zero.
func TestMaintenancePause_PostIsResent(t *testing.T) {
	for _, reason := range []string{"rollover_begin", "rollover_commit", "backup", "restore"} {
		t.Run(reason, func(t *testing.T) {
			srv, calls := pausingServer(t, 1, pauseBody(reason, true))
			c := newTestClient(srv, &ClientOptions{DefaultPurpose: "p"})
			if _, err := c.Query(context.Background(), "SELECT 1"); err != nil {
				t.Fatalf("Query: %v", err)
			}
			if got := atomic.LoadInt32(calls); got != 2 {
				t.Fatalf("calls = %d, want 2", got)
			}
		})
	}
}

// #6302: other 503s keep their existing behaviour: a POST is not resent.
func TestMaintenancePause_OtherServiceUnavailableIsNotResent(t *testing.T) {
	for name, body := range map[string]string{
		"poisoned":             pauseBody("poisoned", false),
		"storage_recovering":   pauseBody("storage_recovering", true),
		"not retryable":        pauseBody("rollover_begin", false),
		"memory-pressure":      `{"code":"memory-pressure","retryable":true}`,
		"not a problem body":   `busy`,
		"retryable missing":    `{"pause_reason":"backup"}`,
		"unknown pause reason": `{"pause_reason":"later","retryable":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, calls := pausingServer(t, 5, body)
			c := newTestClient(srv, &ClientOptions{DefaultPurpose: "p"})
			_, err := c.Query(context.Background(), "SELECT 1")
			var rerr *RelataError
			if !errors.As(err, &rerr) || rerr.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("err = %v, want a 503 RelataError", err)
			}
			if got := atomic.LoadInt32(calls); got != 1 {
				t.Fatalf("calls = %d, want 1", got)
			}
		})
	}
}

// #6302: resends stop once PauseRetryBudget is spent and the 503 is returned;
// a negative budget disables them.
func TestMaintenancePause_BudgetBoundsResends(t *testing.T) {
	srv, calls := pausingServer(t, 100, pauseBody("rollover_commit", true))
	// Each wait is clamped to 100 ms: 100 + 100 + the 50 ms left.
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "p", PauseRetryBudget: 250 * time.Millisecond})
	started := time.Now()
	_, err := c.Query(context.Background(), "SELECT 1")
	var rerr *RelataError
	if !errors.As(err, &rerr) || rerr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want a 503 RelataError", err)
	}
	if got := atomic.LoadInt32(calls); got != 4 {
		t.Fatalf("calls = %d, want 4", got)
	}
	if elapsed := time.Since(started); elapsed < 250*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("elapsed = %v", elapsed)
	}

	srv, calls = pausingServer(t, 100, pauseBody("rollover_commit", true))
	c = newTestClient(srv, &ClientOptions{DefaultPurpose: "p", PauseRetryBudget: -1})
	if _, err := c.Query(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("want the 503 with resends disabled")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	if New("http://x", nil).pauseRetryBudget != defaultPauseRetryBudget {
		t.Fatal("default budget not applied")
	}
}

// #6302: Retry-After wins, then retry_after_secs, then 1 s; clamped to 100 ms..5 s.
func TestMaintenancePauseDelay(t *testing.T) {
	body := func(extra string) []byte {
		return []byte(`{"pause_reason":"backup","retryable":true` + extra + `}`)
	}
	cases := []struct {
		retryAfter time.Duration
		extra      string
		want       time.Duration
	}{
		{60 * time.Second, "", 5 * time.Second},
		{2 * time.Second, `,"retry_after_secs":4`, 2 * time.Second},
		{0, `,"retry_after_secs":3`, 3 * time.Second},
		{0, `,"retry_after_secs":1e12`, 5 * time.Second},
		{0, "", time.Second},
		{time.Millisecond, "", 100 * time.Millisecond},
	}
	for _, tc := range cases {
		got, ok := maintenancePauseDelay(http.StatusServiceUnavailable, tc.retryAfter, body(tc.extra))
		if !ok || got != tc.want {
			t.Fatalf("retryAfter=%v extra=%q: got %v %v, want %v", tc.retryAfter, tc.extra, got, ok, tc.want)
		}
	}
	if _, ok := maintenancePauseDelay(http.StatusBadGateway, 0, body("")); ok {
		t.Fatal("a 502 is not a maintenance pause")
	}
}
