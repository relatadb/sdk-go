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

const twoPersonBody = `{"type":"about:blank","title":"Forbidden","status":403,` +
	`"code":"TWO_PERSON_REQUIRED","detail":"requires a second, distinct admin credential",` +
	`"confirmation_id":"2P-01a21e15-8481-7f63-8906-a40bbeba95b6","expires_at_ns":1795801956385709000}`

func TestRestore_TwoPersonRefusalExposesConfirmationID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, twoPersonBody)
	}))
	defer srv.Close()

	_, err := NewBackupClient(newTestClient(srv, nil)).Restore(context.Background(), "snap-1", nil)
	var re *RelataError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RelataError", err)
	}
	if re.Code != "TWO_PERSON_REQUIRED" || !errors.Is(err, ErrForbidden) {
		t.Fatalf("code = %q, forbidden = %v", re.Code, errors.Is(err, ErrForbidden))
	}
	if re.ConfirmationID != "2P-01a21e15-8481-7f63-8906-a40bbeba95b6" {
		t.Fatalf("ConfirmationID = %q", re.ConfirmationID)
	}
	if re.ConfirmationExpiresAtNs != 1795801956385709000 {
		t.Fatalf("ConfirmationExpiresAtNs = %d", re.ConfirmationExpiresAtNs)
	}
}

func TestRestore_SendsConfirmationIDAndForce(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = io.WriteString(w, `{"restore_id":"res-2"}`)
	}))
	defer srv.Close()

	b := NewBackupClient(newTestClient(srv, nil))
	rid, err := b.Restore(context.Background(), "snap-1", &RestoreOptions{Force: true, ConfirmationID: "2P-abc"})
	if err != nil || rid != "res-2" {
		t.Fatalf("rid = %q, err = %v", rid, err)
	}
	if _, err := b.Restore(context.Background(), "snap-1", nil); err != nil {
		t.Fatal(err)
	}
	if bodies[0]["force"] != true || bodies[0]["confirmation_id"] != "2P-abc" {
		t.Fatalf("first body = %v", bodies[0])
	}
	if _, ok := bodies[1]["force"]; ok {
		t.Fatalf("second body sent force: %v", bodies[1])
	}
	if _, ok := bodies[1]["confirmation_id"]; ok {
		t.Fatalf("second body sent confirmation_id: %v", bodies[1])
	}
}
