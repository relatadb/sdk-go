package relata

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type usageRoundTripFunc func(*http.Request) (*http.Response, error)

func (f usageRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestRecordUsageBuildsGenericEventRequest(t *testing.T) {
	var got map[string]any
	client := New("http://relata.invalid", &ClientOptions{HTTPClient: &http.Client{
		Transport: usageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost || r.URL.Path != "/events/append" {
				t.Fatalf("method/path = %s %s", r.Method, r.URL.Path)
			}
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			return &http.Response{
				StatusCode: http.StatusCreated,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"event_id":"evt_test","object_type":"DocumentUsageEvent","status":"accepted","durability":"local_wal_sync"}`)),
				Request:    r,
			}, nil
		}),
	}, MaxRetries: 0})
	cited, retrieved, score := false, true, 0.75
	receipt, err := RecordUsage(context.Background(), client, DocumentUsageObservation{
		IdempotencyKey: "answer-42:citation-1",
		Target:         EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"},
		Purpose:        "rag-usage",
		Cited:          &cited,
		Retrieved:      &retrieved,
		FeedbackScore:  &score,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.EventID != "evt_test" || receipt.Status != "accepted" || receipt.Durability != "local_wal_sync" {
		t.Fatalf("receipt = %+v", receipt)
	}
	attrs := got["attributes"].(map[string]any)
	if got["object_type"] != "DocumentUsageEvent" || got["idempotency_key"] != "answer-42:citation-1" || attrs["cited"] != false || attrs["retrieved"] != true || attrs["feedback_score"] != 0.75 {
		t.Fatalf("request = %#v", got)
	}
}

func TestRecordUsageValidatesBeforeNetworkCall(t *testing.T) {
	client := New("http://relata.invalid", nil)
	for _, observation := range []DocumentUsageObservation{
		{IdempotencyKey: " ", Target: EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"}, Cited: boolPointer(true)},
		{IdempotencyKey: "stable", Target: EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"}},
		{IdempotencyKey: "stable", Target: EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"}, FeedbackScore: floatPointer(1.1)},
	} {
		if _, err := RecordUsage(context.Background(), client, observation); err == nil {
			t.Fatalf("observation should have failed validation: %+v", observation)
		}
	}
	_, err := RecordUsage(context.Background(), client, DocumentUsageObservation{
		IdempotencyKey: strings.Repeat("x", 257),
		Target:         EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"},
		Cited:          boolPointer(true),
	})
	if err == nil {
		t.Fatal("oversized key should fail before network call")
	}
}

func boolPointer(value bool) *bool { return &value }

func floatPointer(value float64) *float64 { return &value }
