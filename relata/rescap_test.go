package relata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #3214 — bearer zeroization + response-body cap regression tests.

func TestClient_Close_ZeroizesAndFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{BearerToken: "s3cr3t"})
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.bearerToken != "" {
		t.Fatalf("bearer not zeroized: %q", c.bearerToken)
	}
	if _, err := c.Health(context.Background()); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("post-Close request err = %v, want ErrClientClosed", err)
	}
}

func TestClient_PrintfDoesNotLeakBearer(t *testing.T) {
	c := New("http://localhost:9090", &ClientOptions{BearerToken: "s3cr3t"})
	if dump := fmt.Sprintf("%+v", c); strings.Contains(dump, "s3cr3t") {
		t.Fatalf("%%+v leaks the bearer token: %s", dump)
	}
}

func TestS3Client_Close_ZeroizesAndPrintfSafe(t *testing.T) {
	c := New("http://localhost:9090", &ClientOptions{BearerToken: "s3cr3t"})
	s3 := NewS3Client(c)
	if s3.bearerToken != "s3cr3t" {
		t.Fatalf("bearerToken = %q", s3.bearerToken)
	}
	// The unexported field must not be reachable via %+v.
	if dump := fmt.Sprintf("%+v", s3); strings.Contains(dump, "s3cr3t") {
		t.Fatalf("%%+v leaks the bearer token: %s", dump)
	}
	if err := s3.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s3.bearerToken != "" {
		t.Fatalf("bearer not zeroized after Close: %q", s3.bearerToken)
	}
}

func TestResponseCap_ReturnsTypedErrorWithoutOOM(t *testing.T) {
	// A body far larger than the configured cap must be refused with
	// ErrResponseTooLarge, never buffered whole.
	big := strings.Repeat("a", 1<<20) // 1 MiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, big)
	}))
	defer srv.Close()

	c := New(srv.URL, &ClientOptions{MaxResponseBytes: 1024, Timeout: 2e9})
	_, err := c.Health(context.Background())
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
}

func TestResponseCap_WithinCapSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, &ClientOptions{MaxResponseBytes: 1024, Timeout: 2e9})
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("a body within the cap must succeed, got %v", err)
	}
}

func TestS3Client_GetObject_CappedAndStreamable(t *testing.T) {
	big := strings.Repeat("o", 1<<20) // 1 MiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, big)
	}))
	defer srv.Close()

	c := New(srv.URL, &ClientOptions{Timeout: 2e9})
	s3 := NewS3Client(c)
	s3.maxResponseBytes = 1024

	// Buffered GetObject is capped.
	if _, err := s3.GetObject(context.Background(), "b", "k"); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("GetObject err = %v, want ErrResponseTooLarge", err)
	}

	// GetObjectStream hands back the body without buffering it whole.
	stream, err := s3.GetObjectStream(context.Background(), "b", "k")
	if err != nil {
		t.Fatalf("GetObjectStream: %v", err)
	}
	defer stream.Body.Close()
	if stream.Status != http.StatusOK {
		t.Fatalf("status = %d", stream.Status)
	}
	n, err := io.Copy(io.Discard, stream.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if n != int64(len(big)) {
		t.Fatalf("streamed %d bytes, want %d", n, len(big))
	}
}
