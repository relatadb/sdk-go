package relata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type seenReq struct {
	Method, Path, Query string
	Body                map[string]any
}

func recordingServer(status int, reply string, seen *[]seenReq) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&b)
		}
		*seen = append(*seen, seenReq{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, b})
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
}

func TestIngestControlRoutesAndBodies(t *testing.T) {
	var seen []seenReq
	srv := recordingServer(200, `{}`, &seen)
	defer srv.Close()
	ic := NewIngestControlClient(newTestClient(srv, &ClientOptions{BearerToken: "s"}))
	ctx := context.Background()
	contract := map[string]any{"version": float64(2), "fields": []any{}}
	for _, f := range []func() error{
		func() error { _, e := ic.Checkpoint(ctx, "kafka/orders 1"); return e },
		func() error { _, e := ic.ListContracts(ctx); return e },
		func() error { _, e := ic.GetContract(ctx, "orders"); return e },
		func() error { _, e := ic.PutContract(ctx, "orders", contract); return e },
		func() error { _, e := ic.ContractQuality(ctx, "orders"); return e },
		func() error {
			_, e := ic.ListQuarantine(ctx, &QuarantineListOptions{Dataset: "orders", AfterID: 5, Limit: 50})
			return e
		},
		func() error { _, e := ic.ListQuarantine(ctx, nil); return e },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	want := []seenReq{
		{"GET", "/ingest/checkpoints/kafka%2Forders%201", "", nil},
		{"GET", "/ingest/contracts", "", nil},
		{"GET", "/ingest/contracts/orders", "", nil},
		{"PUT", "/ingest/contracts/orders", "", contract},
		{"GET", "/ingest/contracts/orders/quality", "", nil},
		{"GET", "/ingest/quarantine", "after_id=5&dataset=orders&limit=50", nil},
		{"GET", "/ingest/quarantine", "", nil},
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("got %#v\nwant %#v", seen, want)
	}
}

func TestIngestControlDeleteQuarantine(t *testing.T) {
	var seen []seenReq
	srv := recordingServer(http.StatusNoContent, "", &seen)
	defer srv.Close()
	ic := NewIngestControlClient(newTestClient(srv, nil))
	if err := ic.DeleteQuarantine(context.Background(), 9); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Method != "DELETE" || seen[0].Path != "/ingest/quarantine/9" {
		t.Fatalf("seen = %#v", seen)
	}
	var none []seenReq
	nf := recordingServer(404, `{"title":"nf","status":404}`, &none)
	defer nf.Close()
	if err := NewIngestControlClient(newTestClient(nf, nil)).DeleteQuarantine(context.Background(), 1); err == nil {
		t.Fatal("expected 404 error")
	}
}

func TestMediaQualificationRoute(t *testing.T) {
	var seen []seenReq
	srv := recordingServer(200, `{"tasks":[]}`, &seen)
	defer srv.Close()
	out, err := NewMediaWorkClient(newTestClient(srv, nil)).Qualification(context.Background())
	if err != nil || out["tasks"] == nil {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if seen[0].Method != "GET" || seen[0].Path != "/media/qualification" {
		t.Fatalf("seen = %#v", seen)
	}
}

func TestMemoryAssociateBatch(t *testing.T) {
	var seen []seenReq
	srv := recordingServer(200, `{"stored":2,"submitted":2,"results":[]}`, &seen)
	defer srv.Close()
	m, _ := NewMemory(srv.URL, "notes", nil)
	out, err := m.AssociateBatch(context.Background(), []map[string]any{
		{"from_id": "a", "to_id": "b", "relation": "causes"},
		{"from_id": "c", "to_id": "d", "purpose": "x"},
	})
	if err != nil || out["stored"] != float64(2) {
		t.Fatalf("out=%v err=%v", out, err)
	}
	items, _ := seen[0].Body["items"].([]any)
	if seen[0].Path != "/memory/associate/batch" || len(items) != 2 {
		t.Fatalf("seen = %#v", seen)
	}
	if items[0].(map[string]any)["purpose"] != "notes" || items[1].(map[string]any)["purpose"] != "x" {
		t.Fatalf("purpose fill wrong: %v", items)
	}
	if _, err := m.AssociateBatch(context.Background(), []map[string]any{{"from_id": "a"}}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestFilesCreateFolder(t *testing.T) {
	var seen []seenReq
	srv := recordingServer(201, `{"bucket":"t","key":"a/","row_id":null,"size":0,"etag":"e","content_type":"application/x-directory","modified_ns":1,"is_folder":true,"download_url":"/x"}`, &seen)
	defer srv.Close()
	meta, err := NewFilesClient(newTestClient(srv, nil)).CreateFolder(context.Background(), "a", &FilesOptions{Bucket: "t"})
	if err != nil || !meta.IsFolder {
		t.Fatalf("meta=%v err=%v", meta, err)
	}
	if seen[0].Method != "POST" || seen[0].Path != "/files/folders" || seen[0].Body["key"] != "a" || seen[0].Body["bucket"] != "t" {
		t.Fatalf("seen = %#v", seen)
	}
}
