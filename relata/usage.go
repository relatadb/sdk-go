package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// UsageAggregate contains counts derived from the supplied post-migration
// events only. Legacy feedback means cannot be merged without their old
// `_feedback_n` sample counts.
type UsageAggregate struct {
	TargetType     string
	TargetID       string
	EventCount     int
	CitationCount  int
	RetrievalCount int
	FeedbackCount  int
	FeedbackAvg    *float64
	LastCitedAt    *int64
}

// LegacyUsageBaseline is a frozen, versioned pre-event snapshot. A nil
// FeedbackCount means the historical mean cannot be combined exactly.
type LegacyUsageBaseline struct {
	FormatVersion  string
	Target         EventTarget
	CutoverAt      int64
	CitationCount  int
	RetrievalCount int
	FeedbackAvg    *float64
	FeedbackCount  *int
	LastCitedAt    *int64
}

// MigratedUsageAggregate keeps the old baseline and new event reduction
// separately visible while exposing safely combined counters.
type MigratedUsageAggregate struct {
	Legacy         LegacyUsageBaseline
	PostCutover    UsageAggregate
	CitationCount  int
	RetrievalCount int
	FeedbackCount  *int
	FeedbackAvg    *float64
	LastCitedAt    *int64
}

type usageEventPayload struct {
	targetType string
	targetID   string
	observedAt int64
	cited      bool
	retrieved  bool
	score      *float64
}

// AggregateUsageEvents rebuilds the former usage counters from an already
// fetched, complete `DocumentUsageEvent` inventory. The caller must use a
// governed query for the same tenant/purpose and detect truncation before
// calling. Exact duplicate event IDs count once; conflicting payloads fail
// closed. This does not merge historical feedback means because the old
// `_feedback_n` value was not retained.
func AggregateUsageEvents(rows []map[string]any, target EventTarget) (UsageAggregate, error) {
	if strings.TrimSpace(target.ObjectType) == "" || strings.TrimSpace(target.Value) == "" {
		return UsageAggregate{}, fmt.Errorf("target object type and value are required")
	}
	unique := make(map[string]usageEventPayload, len(rows))
	for _, row := range rows {
		eventID, ok := row["event_id"].(string)
		if !ok || strings.TrimSpace(eventID) == "" {
			return UsageAggregate{}, fmt.Errorf("every usage event must have an event ID")
		}
		targetType, typeOK := row["target_type"].(string)
		targetID, idOK := row["target_id"].(string)
		if !typeOK || !idOK || targetType != target.ObjectType || targetID != target.Value {
			return UsageAggregate{}, fmt.Errorf("usage inventory contains a different target")
		}
		observedAt, ok := usageTimestamp(row["observed_at"])
		if !ok {
			return UsageAggregate{}, fmt.Errorf("observed_at must be an integer timestamp")
		}
		payload := usageEventPayload{targetType: targetType, targetID: targetID, observedAt: observedAt}
		if cited, present, ok := usageBool(row, "cited"); !ok {
			return UsageAggregate{}, fmt.Errorf("cited must be boolean when present")
		} else if present {
			payload.cited = cited
		}
		if retrieved, present, ok := usageBool(row, "retrieved"); !ok {
			return UsageAggregate{}, fmt.Errorf("retrieved must be boolean when present")
		} else if present {
			payload.retrieved = retrieved
		}
		if rawScore, present := row["feedback_score"]; present && rawScore != nil {
			score, ok := usageScore(rawScore)
			if !ok {
				return UsageAggregate{}, fmt.Errorf("feedback score must be numeric when present")
			}
			if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
				return UsageAggregate{}, fmt.Errorf("feedback score must be finite and between 0 and 1")
			}
			payload.score = &score
		}
		if previous, ok := unique[eventID]; ok {
			if !sameUsagePayload(previous, payload) {
				return UsageAggregate{}, fmt.Errorf("conflicting usage event ID %s", eventID)
			}
			continue
		}
		unique[eventID] = payload
	}

	result := UsageAggregate{TargetType: target.ObjectType, TargetID: target.Value, EventCount: len(unique)}
	var scoreTotal float64
	eventIDs := make([]string, 0, len(unique))
	for eventID := range unique {
		eventIDs = append(eventIDs, eventID)
	}
	sort.Strings(eventIDs)
	for _, eventID := range eventIDs {
		row := unique[eventID]
		if row.cited {
			result.CitationCount++
			if result.LastCitedAt == nil || row.observedAt > *result.LastCitedAt {
				timestamp := row.observedAt
				result.LastCitedAt = &timestamp
			}
		}
		if row.retrieved {
			result.RetrievalCount++
		}
		if row.score != nil {
			result.FeedbackCount++
			scoreTotal += *row.score
		}
	}
	if result.FeedbackCount > 0 {
		average := scoreTotal / float64(result.FeedbackCount)
		result.FeedbackAvg = &average
	}
	return result, nil
}

// AggregateUsageWithBaseline combines a frozen v1 snapshot with a complete
// post-cutover event inventory. Old writers must be drained before snapshot;
// pre-cutover events are rejected. Nil legacy FeedbackCount keeps the two
// means separate and returns nil combined feedback fields.
func AggregateUsageWithBaseline(rows []map[string]any, target EventTarget, baseline LegacyUsageBaseline) (MigratedUsageAggregate, error) {
	if baseline.FormatVersion != "document-usage-baseline-v1" {
		return MigratedUsageAggregate{}, fmt.Errorf("unsupported legacy usage baseline format version")
	}
	if baseline.Target != target {
		return MigratedUsageAggregate{}, fmt.Errorf("legacy usage baseline belongs to a different target")
	}
	if baseline.CitationCount < 0 || baseline.RetrievalCount < 0 {
		return MigratedUsageAggregate{}, fmt.Errorf("legacy counters must be non-negative")
	}
	if baseline.FeedbackCount != nil && *baseline.FeedbackCount < 0 {
		return MigratedUsageAggregate{}, fmt.Errorf("feedback count must be non-negative")
	}
	if baseline.FeedbackAvg != nil && (math.IsNaN(*baseline.FeedbackAvg) || math.IsInf(*baseline.FeedbackAvg, 0) || *baseline.FeedbackAvg < 0 || *baseline.FeedbackAvg > 1) {
		return MigratedUsageAggregate{}, fmt.Errorf("feedback average must be finite and between 0 and 1")
	}
	if baseline.FeedbackCount != nil && *baseline.FeedbackCount == 0 && baseline.FeedbackAvg != nil {
		return MigratedUsageAggregate{}, fmt.Errorf("feedback average must be nil when feedback count is zero")
	}
	if baseline.FeedbackCount != nil && *baseline.FeedbackCount > 0 && baseline.FeedbackAvg == nil {
		return MigratedUsageAggregate{}, fmt.Errorf("feedback average is required when feedback count is positive")
	}
	post, err := AggregateUsageEvents(rows, target)
	if err != nil {
		return MigratedUsageAggregate{}, err
	}
	maxInt := int(^uint(0) >> 1)
	if post.CitationCount > maxInt-baseline.CitationCount || post.RetrievalCount > maxInt-baseline.RetrievalCount {
		return MigratedUsageAggregate{}, fmt.Errorf("combined usage counter exceeds the platform integer range")
	}
	for _, row := range rows {
		observedAt, ok := usageTimestamp(row["observed_at"])
		if !ok {
			return MigratedUsageAggregate{}, fmt.Errorf("observed_at must be an integer timestamp")
		}
		if observedAt < baseline.CutoverAt {
			return MigratedUsageAggregate{}, fmt.Errorf("usage event predates the baseline cutover")
		}
	}
	result := MigratedUsageAggregate{
		Legacy: baseline, PostCutover: post,
		CitationCount:  baseline.CitationCount + post.CitationCount,
		RetrievalCount: baseline.RetrievalCount + post.RetrievalCount,
	}
	if baseline.LastCitedAt != nil {
		timestamp := *baseline.LastCitedAt
		result.LastCitedAt = &timestamp
	}
	if post.LastCitedAt != nil && (result.LastCitedAt == nil || *post.LastCitedAt > *result.LastCitedAt) {
		timestamp := *post.LastCitedAt
		result.LastCitedAt = &timestamp
	}
	if baseline.FeedbackCount != nil {
		if post.FeedbackCount > maxInt-*baseline.FeedbackCount {
			return MigratedUsageAggregate{}, fmt.Errorf("combined feedback count exceeds the platform integer range")
		}
		count := *baseline.FeedbackCount + post.FeedbackCount
		result.FeedbackCount = &count
		if count > 0 {
			total := 0.0
			if baseline.FeedbackAvg != nil {
				total += *baseline.FeedbackAvg * float64(*baseline.FeedbackCount)
			}
			if post.FeedbackAvg != nil {
				total += *post.FeedbackAvg * float64(post.FeedbackCount)
			}
			average := total / float64(count)
			result.FeedbackAvg = &average
		}
	}
	return result, nil
}

func sameUsagePayload(a, b usageEventPayload) bool {
	if a.targetType != b.targetType || a.targetID != b.targetID || a.observedAt != b.observedAt || a.cited != b.cited || a.retrieved != b.retrieved {
		return false
	}
	if a.score == nil || b.score == nil {
		return a.score == nil && b.score == nil
	}
	return *a.score == *b.score
}

func usageTimestamp(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case json.Number:
		parsed, err := strconv.ParseInt(v.String(), 10, 64)
		return parsed, err == nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || math.Abs(v) > 9007199254740991 {
			return 0, false
		}
		return int64(v), true
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func usageBool(row map[string]any, field string) (bool, bool, bool) {
	value, present := row[field]
	if !present || value == nil {
		return false, present, true
	}
	v, ok := value.(bool)
	return v, true, ok
}

func usageScore(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(v.String(), 64)
		return parsed, err == nil
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

// DocumentUsageObservation is an optional recipe for the
// packages/ontology/v1/document-usage.json schema. The application supplies
// its stable retry key; AggregateUsageEvents reduces a complete governed
// inventory separately.
type DocumentUsageObservation struct {
	IdempotencyKey string
	Target         EventTarget
	Purpose        string
	Cited          *bool
	Retrieved      *bool
	FeedbackScore  *float64
}

// RecordUsage appends one immutable DocumentUsageEvent using the generic
// schema-governed event contract. The target is a logical key/value reference,
// not a version-pinned snapshot; the current contract has no target-revision
// field. The server enforces tenant/target visibility and acknowledges only
// after local WAL sync. It rechecks visibility on exact retries, so a
// deleted/hidden target returns 404 without appending another row. Cluster
// mode is currently rejected.
func RecordUsage(ctx context.Context, client *Client, observation DocumentUsageObservation) (*EventAppendReceipt, error) {
	if strings.TrimSpace(observation.IdempotencyKey) == "" || len(observation.IdempotencyKey) > 256 {
		return nil, fmt.Errorf("idempotency key must contain 1 to 256 non-whitespace bytes")
	}
	attributes := make(map[string]any, 3)
	if observation.Cited != nil {
		attributes["cited"] = *observation.Cited
	}
	if observation.Retrieved != nil {
		attributes["retrieved"] = *observation.Retrieved
	}
	if observation.FeedbackScore != nil {
		if math.IsNaN(*observation.FeedbackScore) || math.IsInf(*observation.FeedbackScore, 0) || *observation.FeedbackScore < 0 || *observation.FeedbackScore > 1 {
			return nil, fmt.Errorf("feedback score must be finite and between 0 and 1")
		}
		attributes["feedback_score"] = *observation.FeedbackScore
	}
	if len(attributes) == 0 {
		return nil, fmt.Errorf("at least one usage observation must be supplied")
	}
	return client.AppendEvent(ctx, EventAppendRequest{
		ObjectType:     "DocumentUsageEvent",
		IdempotencyKey: observation.IdempotencyKey,
		Target:         observation.Target,
		Attributes:     attributes,
		Purpose:        observation.Purpose,
	})
}
