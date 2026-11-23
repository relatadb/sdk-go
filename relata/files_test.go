package relata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFilesExactBinaryMetadataAndDelete(t *testing.T) {
	payload := []byte{0, 255, 254, 'x', 0}
	key := "nested/a+b ?&é.bin"
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		if r.URL.Path != "/v1/files/content" || r.URL.Query().Get("key") != key || r.URL.Query().Get("bucket") != "tenant-a" {
			t.Errorf("route/query: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer scoped" || r.Header.Get("X-Relata-Tenant-Id") != "tenant-a" || r.Header.Get("X-Request-ID") == "" {
			t.Errorf("missing shared headers: %v", r.Header)
		}
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			if !bytes.Equal(body, payload) || r.Header.Get("Content-Type") != "application/x-fixture" {
				t.Errorf("upload bytes/type: %v %s", body, r.Header.Get("Content-Type"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			fmt.Fprint(w, `{"bucket":"tenant-a","key":"nested/a+b ?&é.bin","row_id":"row-1","size":5,"etag":"\"abc\"","content_type":"application/x-fixture","modified_ns":42,"is_folder":false,"download_url":"/v1/files/content"}`)
		case http.MethodGet:
			if r.Header.Get("Accept") != "application/octet-stream" {
				t.Errorf("accept: %s", r.Header.Get("Accept"))
			}
			w.Header().Set("ETag", `"abc"`)
			w.Header().Set("Content-Type", "application/x-fixture")
			w.Write(payload)
		case http.MethodDelete:
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()
	f := NewFilesClient(newTestClient(srv, &ClientOptions{BearerToken: "scoped", Tenant: "tenant-a"}))
	opts := &FilesOptions{Bucket: "tenant-a", ContentType: "application/x-fixture"}
	meta, err := f.Put(context.Background(), key, payload, opts)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Size != 5 || meta.RowID == nil || *meta.RowID != "row-1" || meta.ETag != `"abc"` || meta.ModifiedNS != 42 || meta.Key != key {
		t.Fatalf("metadata: %+v", meta)
	}
	got, err := f.Get(context.Background(), key, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes, payload) || got.ETag != meta.ETag || got.ContentType != "application/x-fixture" {
		t.Fatalf("content: %+v", got)
	}
	if err := f.Delete(context.Background(), key, opts); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(calls) != "[PUT GET DELETE]" {
		t.Fatalf("calls: %v", calls)
	}
}

func TestFilesWritesDoNotRetryUncertain503(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("Retry-After", "2")
				w.Header().Set("X-Request-ID", "file-request")
				w.WriteHeader(503)
				fmt.Fprint(w, `{"type":"urn:relata:write-uncertain","code":"write-uncertain","detail":"commit uncertain","retryable":false}`)
			}))
			defer srv.Close()
			f := NewFilesClient(newTestClient(srv, &ClientOptions{MaxRetries: 2}))
			var err error
			if method == http.MethodPut {
				_, err = f.Put(context.Background(), "k", []byte{0}, nil)
			} else {
				err = f.Delete(context.Background(), "k", nil)
			}
			var problem *RelataError
			if !errors.As(err, &problem) || problem.StatusCode != 503 || problem.Code != "write-uncertain" || problem.RequestID != "file-request" || problem.RetryAfter != 2*time.Second {
				t.Fatalf("typed failure: %#v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("non-idempotent calls=%d", calls.Load())
			}
		})
	}
}

func TestFilesGetCapAndContext(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte{0, 1, 2, 3, 4}) }))
	defer srv.Close()
	f := NewFilesClient(newTestClient(srv, &ClientOptions{MaxResponseBytes: 4}))
	if _, err := f.Get(context.Background(), "k", nil); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("cap: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Get(ctx, "k", nil); err == nil {
		t.Fatal("pre-canceled GET succeeded")
	}
	if calls.Load() != 1 {
		t.Fatalf("canceled request reached server: %d", calls.Load())
	}
}

func TestFilesRejectUnexpectedSuccessProtocol(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202); fmt.Fprint(w, `{}`) }))
			defer srv.Close()
			f := NewFilesClient(newTestClient(srv, nil))
			var err error
			switch method {
			case http.MethodPut:
				_, err = f.Put(context.Background(), "k", nil, nil)
			case http.MethodGet:
				_, err = f.Get(context.Background(), "k", nil)
			default:
				err = f.Delete(context.Background(), "k", nil)
			}
			if err == nil {
				t.Fatal("unexpected202 accepted as completed file operation")
			}
		})
	}
}

func TestFilesGetCancellationReleasesActiveRequest(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer srv.Close()
	f := NewFilesClient(newTestClient(srv, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := f.Get(ctx, "k", nil); result <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached transport")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled active request succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled request did not return")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("server request context remained active")
	}
}

func TestFilesUploadResumesAfterTransientFailure(t *testing.T) {
	payload := []byte("0123456789")
	var data []byte
	var failAt = 4
	var statusReads, chunks int
	var completed []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := func() string {
			return fmt.Sprintf(`{"upload_id":"u1","bucket":"b","key":"k","size":10,"offset":%d,"complete":%v,"sha256":null,"expires_ns":1,"max_chunk_bytes":4}`, len(data), len(data) == 10)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/files/uploads":
			w.WriteHeader(201)
			fmt.Fprint(w, session())
		case r.Method == http.MethodPut:
			if failAt == len(data) {
				failAt = -1
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(503)
				fmt.Fprint(w, `{"code":"busy","detail":"retry","retryable":true}`)
				return
			}
			if r.URL.Query().Get("offset") != fmt.Sprint(len(data)) || len(r.Header.Get("X-Relata-Chunk-Sha256")) != 64 {
				t.Errorf("offset/digest: %s %v", r.URL.RawQuery, r.Header)
			}
			body, _ := io.ReadAll(r.Body)
			data = append(data, body...)
			chunks++
			fmt.Fprint(w, session())
		case r.Method == http.MethodGet:
			statusReads++
			fmt.Fprint(w, session())
		case r.Method == http.MethodPost:
			completed = append([]byte(nil), data...)
			w.WriteHeader(201)
			fmt.Fprint(w, `{"bucket":"b","key":"k","row_id":null,"size":10,"etag":"\"h\"","content_type":"x","modified_ns":1,"is_folder":false,"download_url":"/v1/files/content"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	f := NewFilesClient(newTestClient(srv, nil))
	meta, err := f.Upload(context.Background(), "k", bytes.NewReader(payload), int64(len(payload)), &UploadOptions{ChunkSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Size != 10 || !bytes.Equal(completed, payload) || statusReads != 1 || chunks != 3 {
		t.Fatalf("meta=%+v completed=%q statusReads=%d chunks=%d", meta, completed, statusReads, chunks)
	}
}

func TestFilesUploadSendsProcessAndAccessOnlyWhenSet(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		if r.URL.Path == "/v1/files/uploads" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			bodies = append(bodies, body)
			fmt.Fprint(w, `{"upload_id":"u1","bucket":"b","key":"k","size":0,"offset":0,"complete":true,"sha256":null,"expires_ns":1}`)
			return
		}
		work := ""
		if bodies[len(bodies)-1]["process"] == true {
			work = `,"media_work":{"work_id":"w1","state":"queued","created":true,"status_url":"/v1/media/work/w1"}`
		}
		fmt.Fprintf(w, `{"bucket":"b","key":"k","row_id":null,"size":0,"etag":"\"h\"","content_type":"image/png","modified_ns":1,"is_folder":false,"download_url":"/v1/files/content"%s}`, work)
	}))
	defer srv.Close()
	f := NewFilesClient(newTestClient(srv, nil))
	ctx := context.Background()
	plain, err := f.Upload(ctx, "k", bytes.NewReader(nil), 0, &UploadOptions{ContentType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := f.Upload(ctx, "k", bytes.NewReader(nil), 0, &UploadOptions{
		ContentType: "image/png", Process: true, Access: map[string]any{"classification": "internal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies[0]["process"]; ok {
		t.Fatalf("process sent by default: %v", bodies[0])
	}
	if _, ok := bodies[0]["access"]; ok || plain.MediaWork != nil {
		t.Fatalf("access/media_work by default: %v %v", bodies[0], plain.MediaWork)
	}
	acc, _ := bodies[1]["access"].(map[string]any)
	if bodies[1]["process"] != true || acc["classification"] != "internal" {
		t.Fatalf("process/access not sent: %v", bodies[1])
	}
	if processed.MediaWork["work_id"] != "w1" {
		t.Fatalf("media_work not exposed: %v", processed.MediaWork)
	}
}
