package relata

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type seenRequest struct {
	method  string
	path    string
	query   string
	headers http.Header
	body    []byte
}

func conditionalServer(t *testing.T, status int, response string) (*ObjectClient, *seenRequest) {
	t.Helper()
	seen := &seenRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method, seen.path, seen.query, seen.headers = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		seen.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, &ClientOptions{BearerToken: "t", DefaultPurpose: "audit"})
	return NewObjectClient(c), seen
}

func TestUpsertIfAbsentUsesPutWithIfNoneMatch(t *testing.T) {
	oc, seen := conditionalServer(t, 201, `{"type":"Doc","id":"a","version":7}`)
	out, err := oc.Upsert(context.Background(), "Doc", "a", map[string]any{"n": "x", "id": "ignored"}, &UpsertOptions{IfAbsent: true})
	if err != nil {
		t.Fatal(err)
	}
	if out["version"] != float64(7) {
		t.Fatalf("version = %v", out["version"])
	}
	if seen.method != "PUT" || seen.path != "/v1/objects/Doc/a" || seen.query != "purpose=audit" {
		t.Fatalf("request = %s %s?%s", seen.method, seen.path, seen.query)
	}
	if seen.headers.Get("If-None-Match") != "*" || seen.headers.Get("If-Match") != "" {
		t.Fatalf("headers = %v", seen.headers)
	}
	var body map[string]any
	_ = json.Unmarshal(seen.body, &body)
	if len(body) != 1 || body["n"] != "x" {
		t.Fatalf("body = %v", body)
	}
}

func TestUpsertExpectedVersionLostRaceIsPreconditionFailed(t *testing.T) {
	oc, seen := conditionalServer(t, 412, `{"status":412,"detail":"'Doc/a' is at version 42, expected 41","code":"precondition-failed","current_version":42,"failed_operation":0}`)
	v := int64(41)
	_, err := oc.Upsert(context.Background(), "Doc", "a", map[string]any{"n": "x"}, &UpsertOptions{ExpectedVersion: &v})
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("err = %v, want ErrPreconditionFailed", err)
	}
	var re *RelataError
	if !errors.As(err, &re) || re.StatusCode != 412 || re.Code != "precondition-failed" {
		t.Fatalf("relata error = %+v", re)
	}
	if re.CurrentVersion == nil || *re.CurrentVersion != 42 || re.FailedOperation == nil || *re.FailedOperation != 0 {
		t.Fatalf("metadata = %v %v", re.CurrentVersion, re.FailedOperation)
	}
	if seen.headers.Get("If-Match") != `"41"` {
		t.Fatalf("If-Match = %q", seen.headers.Get("If-Match"))
	}
}

func TestUpsertPreconditionsAreMutuallyExclusive(t *testing.T) {
	oc, _ := conditionalServer(t, 200, `{}`)
	v := int64(1)
	if _, err := oc.Upsert(context.Background(), "Doc", "a", nil, &UpsertOptions{ExpectedVersion: &v, IfAbsent: true}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestUnconditionalUpsertStillUsesIngest(t *testing.T) {
	oc, seen := conditionalServer(t, 200, `{"rows_queued":1}`)
	if _, err := oc.Upsert(context.Background(), "Doc", "a", map[string]any{"n": "x"}, nil); err != nil {
		t.Fatal(err)
	}
	if seen.method != "POST" || seen.path != "/v1/ingest" {
		t.Fatalf("request = %s %s", seen.method, seen.path)
	}
}

func TestDeleteWithExpectedVersionIsConditional(t *testing.T) {
	oc, seen := conditionalServer(t, 200, `{"deleted":true,"version":9,"commitId":"c"}`)
	v := int64(8)
	out, err := oc.Delete(context.Background(), "Doc", "a", &DeleteOptions{ExpectedVersion: &v})
	if err != nil {
		t.Fatal(err)
	}
	if out["deleted"] != true || seen.method != "DELETE" || seen.path != "/v1/objects/Doc/a" || seen.headers.Get("If-Match") != `"8"` {
		t.Fatalf("out=%v request=%s %s if-match=%q", out, seen.method, seen.path, seen.headers.Get("If-Match"))
	}
}

func TestGetReturnsVersionAndNilOnMissing(t *testing.T) {
	oc, seen := conditionalServer(t, 200, `{"id":"a","_version":5}`)
	row, err := oc.Get(context.Background(), "Doc", "a")
	if err != nil || row["_version"] != float64(5) || seen.method != "GET" {
		t.Fatalf("row=%v err=%v method=%s", row, err, seen.method)
	}
	missing, _ := conditionalServer(t, 404, `{"error":"row not found"}`)
	row, err = missing.Get(context.Background(), "Doc", "gone")
	if row != nil || err != nil {
		t.Fatalf("row=%v err=%v", row, err)
	}
}

func TestCommitPostsBundleAndReportsFailingOperation(t *testing.T) {
	oc, seen := conditionalServer(t, 200, `{"commitId":"c1","operations":[{"op":"update","type":"Doc","id":"a","version":3}],"replayed":false}`)
	v := int64(2)
	res, err := oc.Commit(context.Background(), []CommitOperation{{Op: "update", Type: "Doc", ID: "a", ExpectedVersion: &v, Data: map[string]any{"n": "y"}}}, &CommitOptions{IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.CommitID != "c1" || len(res.Operations) != 1 || res.Operations[0].Version != 3 || res.Replayed {
		t.Fatalf("result = %+v", res)
	}
	var body map[string]any
	_ = json.Unmarshal(seen.body, &body)
	ops := body["operations"].([]any)[0].(map[string]any)
	if seen.method != "POST" || seen.path != "/v1/commit" || body["idempotencyKey"] != "k1" || body["purpose"] != "audit" || ops["expectedVersion"] != float64(2) {
		t.Fatalf("request = %s %s body=%v", seen.method, seen.path, body)
	}

	failing, _ := conditionalServer(t, 412, `{"status":412,"detail":"missing","code":"precondition-failed","current_version":null,"failed_operation":2}`)
	_, err = failing.Commit(context.Background(), []CommitOperation{{Op: "delete", Type: "Doc", ID: "z"}}, nil)
	var re *RelataError
	if !errors.Is(err, ErrPreconditionFailed) || !errors.As(err, &re) || re.CurrentVersion != nil || re.FailedOperation == nil || *re.FailedOperation != 2 {
		t.Fatalf("err = %v re=%+v", err, re)
	}
}

func TestDeleteWhereSendsPredicateBodyAndRefusesBlank(t *testing.T) {
	oc, seen := conditionalServer(t, 200, `{"deleted":3,"type":"Doc"}`)
	out, err := oc.DeleteWhere(context.Background(), "Doc", "status = 'stale'")
	if err != nil {
		t.Fatal(err)
	}
	if out["deleted"] != float64(3) || seen.method != "DELETE" || seen.path != "/v1/objects/Doc" || string(seen.body) != `{"where":"status = 'stale'"}` {
		t.Fatalf("out=%v request=%s %s body=%s", out, seen.method, seen.path, seen.body)
	}
	blank, blankSeen := conditionalServer(t, 200, `{}`)
	if _, err := blank.DeleteWhere(context.Background(), "Doc", "  "); err == nil || blankSeen.method != "" {
		t.Fatalf("blank where must fail before any request: err=%v method=%q", err, blankSeen.method)
	}
}
