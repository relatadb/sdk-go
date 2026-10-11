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
