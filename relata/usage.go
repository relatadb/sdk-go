package relata

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// DocumentUsageObservation is an optional recipe for the
// packages/ontology/v1/document-usage.json schema. The application supplies
// its stable retry key and decides how to aggregate observations.
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
