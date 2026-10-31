package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/relatadb/sdk-go/v2/relata"
)

func main() {
	targetID := env("RELATA_USAGE_TARGET_ID", "report-42")
	idempotencyKey := env("RELATA_USAGE_IDEMPOTENCY_KEY", "answer-42:citation-1")
	tenant := requiredEnv("RELATA_TENANT")
	client := relata.New(env("RELATA_URL", "http://localhost:9090"), &relata.ClientOptions{
		BearerToken: os.Getenv("RELATA_BEARER_TOKEN"),
		Tenant:      tenant,
	})
	defer client.Close()

	retrieved := true
	receipt, err := relata.RecordUsage(context.Background(), client, relata.DocumentUsageObservation{
		// Keep stable across retries after a response is lost.
		IdempotencyKey: idempotencyKey,
		Target:         relata.EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: targetID},
		Purpose:        "rag-usage",
		Cited:          &retrieved,
		Retrieved:      &retrieved,
		FeedbackScore:  floatPointer(0.9),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Also grant read permission for DocumentUsageEvent. Fail closed at this
	// query cap rather than presenting a truncated inventory as complete.
	const eventLimit = 10000
	// The SDK's QueryResult follows ordinary JSON decoding, which represents
	// JSON integers as float64. Read this exact-timestamp projection with
	// encoding/json.UseNumber so observed_at's i64 nanoseconds remain intact.
	queryRows, err := queryUsageInventory(
		context.Background(), env("RELATA_URL", "http://localhost:9090"),
		os.Getenv("RELATA_BEARER_TOKEN"), tenant,
		"SELECT event_id, target_type, target_id, observed_at, cited, retrieved, feedback_score FROM DocumentUsageEvent WHERE target_type = 'DocumentSource' AND target_id = '"+strings.ReplaceAll(targetID, "'", "''")+"' LIMIT 10000",
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(queryRows) >= eventLimit {
		fmt.Fprintf(os.Stderr, "usage event inventory reached the %d row cap; paginate before aggregation\n", eventLimit)
		os.Exit(1)
	}
	summary, err := relata.AggregateUsageEvents(queryRows, relata.EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: targetID})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	target := relata.EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: targetID}
	// Synthetic pre-cutover state for demonstrating the versioned reducer. Real
	// applications load this record from their governed migration export.
	legacyMean, trustedCount := 0.6, 2
	baseline := relata.LegacyUsageBaseline{
		FormatVersion: "document-usage-baseline-v1", Target: target, CutoverAt: 0,
		CitationCount: 7, RetrievalCount: 11, FeedbackAvg: &legacyMean,
	}
	migrated, err := relata.AggregateUsageWithBaseline(queryRows, target, baseline)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	trustedBaseline := baseline
	trustedBaseline.FeedbackCount = &trustedCount
	trustedMigrated, err := relata.AggregateUsageWithBaseline(queryRows, target, trustedBaseline)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	wrongTargetBaseline := baseline
	wrongTargetBaseline.Target.Value = "other"
	_, wrongTargetErr := relata.AggregateUsageWithBaseline(queryRows, target, wrongTargetBaseline)
	var lastCitedAt *string
	if summary.LastCitedAt != nil {
		value := strconv.FormatInt(*summary.LastCitedAt, 10)
		lastCitedAt = &value
	}
	output := struct {
		Receipt struct {
			EventID    string `json:"event_id"`
			Status     string `json:"status"`
			Durability string `json:"durability"`
		} `json:"receipt"`
		Summary struct {
			TargetType     string   `json:"target_type"`
			TargetID       string   `json:"target_id"`
			EventCount     int      `json:"event_count"`
			CitationCount  int      `json:"citation_count"`
			RetrievalCount int      `json:"retrieval_count"`
			FeedbackCount  int      `json:"feedback_count"`
			FeedbackAvg    *float64 `json:"feedback_avg"`
			LastCitedAtNS  *string  `json:"last_cited_at_ns"`
		} `json:"summary"`
		Migration struct {
			BaselineFormatVersion  string   `json:"baseline_format_version"`
			CitationCount          int      `json:"citation_count"`
			RetrievalCount         int      `json:"retrieval_count"`
			LegacyFeedbackAvg      *float64 `json:"legacy_feedback_avg"`
			PostCutoverFeedbackAvg *float64 `json:"post_cutover_feedback_avg"`
			FeedbackCount          *int     `json:"feedback_count"`
			FeedbackAvg            *float64 `json:"feedback_avg"`
			TrustedFeedbackCount   *int     `json:"trusted_feedback_count"`
			TrustedFeedbackAvg     *float64 `json:"trusted_feedback_avg"`
			WrongTargetRejected    bool     `json:"wrong_target_rejected"`
		} `json:"migration"`
	}{}
	output.Receipt.EventID, output.Receipt.Status, output.Receipt.Durability = receipt.EventID, receipt.Status, receipt.Durability
	output.Summary.TargetType, output.Summary.TargetID = summary.TargetType, summary.TargetID
	output.Summary.EventCount, output.Summary.CitationCount = summary.EventCount, summary.CitationCount
	output.Summary.RetrievalCount, output.Summary.FeedbackCount = summary.RetrievalCount, summary.FeedbackCount
	output.Summary.FeedbackAvg, output.Summary.LastCitedAtNS = summary.FeedbackAvg, lastCitedAt
	output.Migration.BaselineFormatVersion = migrated.Legacy.FormatVersion
	output.Migration.CitationCount, output.Migration.RetrievalCount = migrated.CitationCount, migrated.RetrievalCount
	output.Migration.LegacyFeedbackAvg = migrated.Legacy.FeedbackAvg
	output.Migration.PostCutoverFeedbackAvg = migrated.PostCutover.FeedbackAvg
	output.Migration.FeedbackCount, output.Migration.FeedbackAvg = migrated.FeedbackCount, migrated.FeedbackAvg
	output.Migration.TrustedFeedbackCount, output.Migration.TrustedFeedbackAvg = trustedMigrated.FeedbackCount, trustedMigrated.FeedbackAvg
	output.Migration.WrongTargetRejected = wrongTargetErr != nil
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func requiredEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		fmt.Fprintf(os.Stderr, "Set %s to the destination tenant ID\n", key)
		os.Exit(1)
	}
	return value
}

func floatPointer(value float64) *float64 { return &value }

func queryUsageInventory(ctx context.Context, baseURL, token, tenant, sql string) ([]map[string]any, error) {
	body, err := json.Marshal(map[string]string{"purpose": "rag-usage", "sql": sql})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/query", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Relata-Tenant-Id", tenant)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("usage inventory query returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	var wire struct {
		Data []map[string]any `json:"data"`
		Rows json.RawMessage  `json:"rows"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return nil, err
	}
	if wire.Data != nil {
		return wire.Data, nil
	}
	if len(wire.Rows) > 0 && wire.Rows[0] == '[' {
		var rows []map[string]any
		rowsDecoder := json.NewDecoder(bytes.NewReader(wire.Rows))
		rowsDecoder.UseNumber()
		if err := rowsDecoder.Decode(&rows); err != nil {
			return nil, err
		}
		return rows, nil
	}
	return nil, fmt.Errorf("usage inventory response has no row data")
}
