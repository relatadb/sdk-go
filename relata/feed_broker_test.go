package relata

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFeedBrokerClientUsesExplicitBrokerAndPublisherContract(t *testing.T) {
	var paths []string
	var publishBody map[string]any
	var publisherDID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/feed/publish" {
			publisherDID = r.Header.Get("X-Publisher-Did")
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &publishBody)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/feed/channels" {
			io.WriteString(w, `[{"path":"relata-feed/osint-ioc","min_tlp":"TLP:WHITE"}]`)
		} else {
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()

	did := "did:web:feeds.example.test"
	entry := map[string]any{
		"entry_id": "entry-1", "channel": "osint-ioc", "publisher_id": did,
		"published_at": "2026-10-11T00:00:00Z", "valid_from": "2026-10-11T00:00:00Z",
		"tlp": "WHITE", "confidence": 90, "payload_kind": "object_assertion",
		"payload": map[string]any{"indicator": "example.test"}, "publisher_signature": "ed25519-abcd",
	}
	client := NewFeedBrokerClient(srv.URL, &ClientOptions{BearerToken: "broker-token"})
	defer client.Close()
	ctx := context.Background()
	if _, err := client.Health(ctx); err != nil {
		t.Fatal(err)
	}
	channels, err := client.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0]["path"] != "relata-feed/osint-ioc" {
		t.Fatalf("channels = %v", channels)
	}
	if _, err := client.Publish(ctx, did, entry); err != nil {
		t.Fatal(err)
	}
	if got := publisherDID; got != did {
		t.Fatalf("publisher header = %q, want %q", got, did)
	}
	if len(paths) != 3 || paths[0] != "/feed/health" || paths[1] != "/feed/channels" || paths[2] != "/feed/publish" {
		t.Fatalf("paths = %v", paths)
	}
	if publishBody["entry_id"] != "entry-1" || publishBody["publisher_id"] != did {
		t.Fatalf("publish body = %v", publishBody)
	}
	if payload, ok := publishBody["payload"].(map[string]any); !ok || payload["indicator"] != "example.test" {
		t.Fatalf("payload = %v", publishBody["payload"])
	}
}

func TestFeedBrokerPublishRequiresPublisherDID(t *testing.T) {
	client := NewFeedBrokerClient("http://127.0.0.1:1", nil)
	defer client.Close()
	if _, err := client.Publish(context.Background(), "", map[string]any{}); err == nil {
		t.Fatal("expected missing publisher DID error")
	}
}
