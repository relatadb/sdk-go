package relata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestS3Client_ListBuckets(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-Relata-Tenant-Id")
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<ListBucketsResult></ListBucketsResult>`))
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{BearerToken: "tok-1", Tenant: "org-1"})
	s3 := NewS3Client(c)

	resp, err := s3.ListBuckets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet || gotPath != "/" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotTenant != "org-1" {
		t.Fatalf("X-Relata-Tenant-Id = %q", gotTenant)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d", resp.Status)
	}
	if string(resp.Body) != `<ListBucketsResult></ListBucketsResult>` {
		t.Fatalf("body = %q", resp.Body)
	}
	if resp.Headers["content-type"] != "application/xml" {
		t.Fatalf("headers = %v", resp.Headers)
	}
}

func TestS3Client_CreateBucket(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{BearerToken: "tok"})
	s3 := NewS3Client(c)

	resp, err := s3.CreateBucket(context.Background(), "acme-intel")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/acme-intel" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d", resp.Status)
	}
}

func TestS3Client_PutAndGetObject(t *testing.T) {
	var gotContentType string
	var gotBody []byte
	stored := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			gotContentType = r.Header.Get("Content-Type")
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			gotBody = buf
			stored[r.URL.Path] = buf
			w.Header().Set("ETag", `"abc123"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			body, ok := stored[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{BearerToken: "tok"})
	s3 := NewS3Client(c)
	ctx := context.Background()

	putResp, err := s3.PutObject(ctx, "acme-intel", "report.pdf", []byte("pdf-bytes"),
		&PutObjectOptions{ContentType: "application/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if gotContentType != "application/pdf" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	if string(gotBody) != "pdf-bytes" {
		t.Fatalf("body = %q", gotBody)
	}
	if putResp.Headers["etag"] != `"abc123"` {
		t.Fatalf("etag = %q", putResp.Headers["etag"])
	}

	getResp, err := s3.GetObject(ctx, "acme-intel", "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if string(getResp.Body) != "pdf-bytes" {
		t.Fatalf("get body = %q", getResp.Body)
	}

	missing, err := s3.GetObject(ctx, "acme-intel", "missing.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Status != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404 (not a Go error)", missing.Status)
	}
}

func TestS3Client_DeleteObject(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{BearerToken: "tok"})
	s3 := NewS3Client(c)

	resp, err := s3.DeleteObject(context.Background(), "acme-intel", "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/acme-intel/report.pdf" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if resp.Status != http.StatusNoContent {
		t.Fatalf("status = %d", resp.Status)
	}
}

func TestS3Client_UserAgentTracksVersion(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv, nil)
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "relata-go/" + Version
	if gotUA != want {
		t.Fatalf("User-Agent = %q, want %q", gotUA, want)
	}
}

// TestS3Client_ThreadsTenantScope verifies PutObject carries the tenant /
// acting-as / delegated-by headers plus a per-request X-Request-ID (#3213) —
// without them the S3 door falls back to default-tenant resolution.
func TestS3Client_ThreadsTenantScope(t *testing.T) {
	var gotTenant, gotActingAs, gotDelegatedBy, gotRequestID, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-Relata-Tenant-Id")
		gotActingAs = r.Header.Get("X-Acting-As")
		gotDelegatedBy = r.Header.Get("X-Delegated-By")
		gotRequestID = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv, &ClientOptions{
		BearerToken: "tok",
		Tenant:      "org-acme",
		ActingAs:    "alice",
		DelegatedBy: "root-admin",
	})
	s3 := NewS3Client(c)
	if _, err := s3.PutObject(context.Background(), "b", "k", []byte("x"), nil); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotTenant != "org-acme" {
		t.Fatalf("X-Relata-Tenant-Id = %q", gotTenant)
	}
	if gotActingAs != "alice" {
		t.Fatalf("X-Acting-As = %q", gotActingAs)
	}
	if gotDelegatedBy != "root-admin" {
		t.Fatalf("X-Delegated-By = %q", gotDelegatedBy)
	}
	if gotRequestID == "" {
		t.Fatal("expected a per-request X-Request-ID")
	}
}
