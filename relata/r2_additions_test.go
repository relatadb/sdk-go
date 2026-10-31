package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type r2Call struct {
	method, path, query string
	body                map[string]any
}

func r2Server(t *testing.T, status int, respBody string) (*httptest.Server, *[]r2Call) {
	t.Helper()
	var calls []r2Call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := r2Call{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery}
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &c.body)
		}
		calls = append(calls, c)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "\"e1\"")
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			fmt.Fprint(w, respBody)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestMediaWorkRequeue(t *testing.T) {
	srv, calls := r2Server(t, 200, `{"state":"queued"}`)
	m := NewMediaWorkClient(newTestClient(srv, nil))
	if _, err := m.Requeue(context.Background(), "w/1"); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.method != "POST" || c.path != "/v1/media/work/w%2F1/requeue" {
		t.Fatalf("call = %+v", c)
	}
}

func TestFilesHeadListAndMediaContent(t *testing.T) {
	srv, calls := r2Server(t, 200, `{"files":[]}`)
	f := NewFilesClient(newTestClient(srv, nil))
	ctx := context.Background()
	head, err := f.Head(ctx, "a/b.txt", nil)
	if err != nil || head == nil || head.ETag != "\"e1\"" {
		t.Fatalf("Head = %+v, %v", head, err)
	}
	if _, err := f.List(ctx, &ListFilesOptions{Prefix: "a/", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.MediaContent(ctx, "row/1"); err != nil {
		t.Fatal(err)
	}
	cs := *calls
	if cs[0].method != "HEAD" || cs[0].path != "/v1/files/content" {
		t.Fatalf("head call = %+v", cs[0])
	}
	if cs[1].method != "GET" || cs[1].path != "/v1/files" || cs[1].query != "limit=10&prefix=a%2F" {
		t.Fatalf("list call = %+v", cs[1])
	}
	if cs[2].method != "GET" || cs[2].path != "/v1/media/row%2F1/content" {
		t.Fatalf("media call = %+v", cs[2])
	}
}

func TestFilesHeadMissingReturnsNil(t *testing.T) {
	srv, _ := r2Server(t, 404, ``)
	f := NewFilesClient(newTestClient(srv, nil))
	got, err := f.Head(context.Background(), "nope", nil)
	if err != nil || got != nil {
		t.Fatalf("Head = %+v, %v; want nil, nil", got, err)
	}
}

func TestGDPREraseDeletionStatusVerifyReceipt(t *testing.T) {
	srv, calls := r2Server(t, 200, `{"ok":true}`)
	g := NewGovernanceClient(newTestClient(srv, nil))
	ctx := context.Background()
	if _, err := g.Erase(ctx, "subj-1", &EraseOptions{Reason: "dsar", Physical: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.DeletionStatus(ctx, "op/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.VerifyReceipt(ctx, map[string]any{"receipt_id": "r1"}); err != nil {
		t.Fatal(err)
	}
	cs := *calls
	if cs[0].method != "POST" || cs[0].path != "/v1/gdpr/erase" || cs[0].body["subject_id"] != "subj-1" || cs[0].body["physical"] != true {
		t.Fatalf("erase = %+v", cs[0])
	}
	if cs[1].method != "GET" || cs[1].path != "/v1/gdpr/deletions/op%2F1" {
		t.Fatalf("status = %+v", cs[1])
	}
	if cs[2].method != "POST" || cs[2].path != "/v1/gdpr/verify-receipt" || cs[2].body["receipt_id"] != "r1" {
		t.Fatalf("verify = %+v", cs[2])
	}
}
