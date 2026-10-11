package relata

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
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

func TestAggregateUsageEventsDeduplicatesAndRebuildsFormerCounters(t *testing.T) {
	target := EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"}
	rows := []map[string]any{
		{"event_id": "evt-b", "target_type": target.ObjectType, "target_id": target.Value, "observed_at": int64(20), "cited": true, "retrieved": true, "feedback_score": 0.4},
		{"event_id": "evt-a", "target_type": target.ObjectType, "target_id": target.Value, "observed_at": int64(10), "cited": true, "retrieved": true, "feedback_score": 0.8},
		{"event_id": "evt-b", "target_type": target.ObjectType, "target_id": target.Value, "observed_at": int64(20), "cited": true, "retrieved": true, "feedback_score": 0.4},
		{"event_id": "evt-c", "target_type": target.ObjectType, "target_id": target.Value, "observed_at": int64(30), "retrieved": true},
	}
	got, err := AggregateUsageEvents(rows, target)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventCount != 3 || got.CitationCount != 2 || got.RetrievalCount != 3 || got.FeedbackCount != 2 || got.FeedbackAvg == nil || math.Abs(*got.FeedbackAvg-0.6) > 1e-12 || got.LastCitedAt == nil || *got.LastCitedAt != 20 {
		t.Fatalf("aggregate = %+v", got)
	}
	reversed, err := AggregateUsageEvents([]map[string]any{rows[3], rows[2], rows[1], rows[0]}, target)
	if err != nil || reversed.EventCount != got.EventCount || reversed.CitationCount != got.CitationCount || reversed.RetrievalCount != got.RetrievalCount || reversed.FeedbackAvg == nil || *reversed.FeedbackAvg != *got.FeedbackAvg || reversed.LastCitedAt == nil || *reversed.LastCitedAt != *got.LastCitedAt {
		t.Fatalf("order changed aggregate: first=%+v reversed=%+v err=%v", got, reversed, err)
	}
}

func TestAggregateUsageEventsRejectsConflictTargetAndInvalidScore(t *testing.T) {
	target := EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"}
	row := map[string]any{"event_id": "evt-1", "target_type": target.ObjectType, "target_id": target.Value, "observed_at": int64(10), "cited": true}
	conflict := map[string]any{"event_id": "evt-1", "target_type": target.ObjectType, "target_id": target.Value, "observed_at": int64(10), "cited": false}
	wrongTarget := map[string]any{"event_id": "evt-2", "target_type": target.ObjectType, "target_id": "other", "observed_at": int64(10)}
	badScore := map[string]any{"event_id": "evt-3", "target_type": target.ObjectType, "target_id": target.Value, "observed_at": int64(10), "feedback_score": math.NaN()}
	for name, rows := range map[string][]map[string]any{
		"conflict":   {row, conflict},
		"target":     {wrongTarget},
		"score":      {badScore},
		"missing id": {{"target_type": target.ObjectType, "target_id": target.Value, "observed_at": int64(10)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := AggregateUsageEvents(rows, target); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestAggregateUsageEventsPreservesModernEpochNanoseconds(t *testing.T) {
	const base int64 = 1791710000000000000
	target := EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"}
	rows := []map[string]any{
		{"event_id": "evt-later", "target_type": target.ObjectType, "target_id": target.Value, "observed_at": json.Number(strconv.FormatInt(base+1, 10)), "cited": true},
		{"event_id": "evt-earlier", "target_type": target.ObjectType, "target_id": target.Value, "observed_at": strconv.FormatInt(base, 10), "cited": true},
	}
	got, err := AggregateUsageEvents(rows, target)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastCitedAt == nil || *got.LastCitedAt != base+1 {
		t.Fatalf("last cited timestamp = %v, want %d", got.LastCitedAt, base+1)
	}
	rows[0]["observed_at"] = float64(base + 1)
	if _, err := AggregateUsageEvents(rows, target); err == nil {
		t.Fatal("unsafe JSON float timestamp should be rejected")
	}
}

func TestAggregateUsageEventsDecodesUseNumberTimestampAndScore(t *testing.T) {
	const payload = `{"data":[{"event_id":"evt-live","target_type":"DocumentSource","target_id":"report-42","observed_at":1791710000000000001,"cited":true,"retrieved":true,"feedback_score":0.9}]}`
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	var wire struct {
		Data []map[string]any `json:"data"`
	}
	if err := decoder.Decode(&wire); err != nil {
		t.Fatal(err)
	}
	target := EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"}
	got, err := AggregateUsageEvents(wire.Data, target)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventCount != 1 || got.CitationCount != 1 || got.RetrievalCount != 1 || got.FeedbackCount != 1 || got.FeedbackAvg == nil || math.Abs(*got.FeedbackAvg-0.9) > 1e-12 || got.LastCitedAt == nil || *got.LastCitedAt != 1791710000000000001 {
		t.Fatalf("aggregate from UseNumber response = %+v", got)
	}
}

func boolPointer(value bool) *bool { return &value }

func floatPointer(value float64) *float64 { return &value }
