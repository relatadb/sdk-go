// Command investigation goes from ingested events to an archived alert record
// (#5702).
//
// It is the SDK version of the curl walk-through in
// docs/src/guides/siem-detection-and-investigation.md ("Worked example: from
// ingest to export") and docs/src/guides/investigation-archive-workflow.md:
//
//  1. ingest a few fixed AuthEvent and Session rows
//  2. create a window rule and wait for its alert
//  3. fetch the alert evidence and reproduce the alert from its query
//  4. read the ordered timeline
//  5. pivot on the alert's user into the other readable records
//  6. keep the reproduction query as a saved search and run it
//  7. archive the evidence export as a file and check its SHA-256
//
// The token needs read+write on AuthEvent, Session, Alert, SavedSearch and
// S3Object. The rule fires on the next evaluation tick
// (RELATA_RULE_EVAL_INTERVAL_SECS, default 30), so the program waits up to 90 s.
//
// Usage (from sdks/go):
//
//	RELATA_URL=http://localhost:9090 RELATA_TOKEN=<data-plane token> \
//	    go run ./examples/investigation
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/relatadb/sdk-go/v3/relata"
)

const (
	purpose    = "audit"
	minute     = int64(60_000_000_000)
	t0         = int64(1_760_000_100_000_000_000) // fixed event time on a 5-minute boundary
	searchName = "alice failed logins (sdk example)"
)

func authEvent(pk string, offMin int64, user string) map[string]any {
	return map[string]any{"_pk": pk, "ts": t0 + offMin*minute, "kind": "login_failed", "user": user}
}

var (
	authEvents = []map[string]any{
		authEvent("a1", 1, "alice"), authEvent("a2", 2, "alice"),
		authEvent("a3", 3, "alice"), authEvent("b1", 2, "bob"),
	}
	sessions = []map[string]any{
		{"_pk": "s1", "ts": t0 + 4*minute, "user": "alice", "host": "srv-9"},
		{"_pk": "s2", "ts": t0 + 4*minute, "user": "bob", "host": "srv-1"},
	}
	rule = map[string]any{
		"name": "brute force (sdk example)", "target_type": "AuthEvent", "severity": "high",
		"window": map[string]any{
			"kind": "threshold", "event_type": "login_failed", "min_count": 3,
			"window_ns": 5 * minute, "allowed_lateness_ns": 0, "version": 1,
			"time_field": "ts", "group_field": "user", "event_type_field": "kind",
		},
	}
)

// at walks nested JSON objects: at(doc, "evidence", "reproduce", "sql").
func at(doc map[string]any, path ...string) any {
	var cur any = doc
	for _, k := range path {
		m, _ := cur.(map[string]any)
		cur = m[k]
	}
	return cur
}

func list(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func must[T any](v T, err error) T {
	if err != nil {
		log.Fatal(err)
	}
	return v
}

func waitForAlert(ctx context.Context, gov *relata.GovernanceClient, ruleID string) string {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		alerts := must(gov.ListAlerts(ctx, &relata.ListAlertsOptions{NewestFirst: true, Limit: 1000}))
		for _, a := range alerts {
			if a["rule_id"] == ruleID {
				return fmt.Sprint(a["alert_id"])
			}
		}
		time.Sleep(2 * time.Second)
	}
	log.Fatalf("rule %s raised no alert within 90s", ruleID)
	return ""
}

func main() {
	url := os.Getenv("RELATA_URL")
	if url == "" {
		url = "http://localhost:9090"
	}
	client := relata.New(url, &relata.ClientOptions{
		BearerToken:    os.Getenv("RELATA_TOKEN"),
		DefaultPurpose: purpose,
		Timeout:        60 * time.Second,
	})
	ctx := context.Background()
	ingest := relata.NewIngestClient(client)
	gov := relata.NewGovernanceClient(client)
	inv := relata.NewInvestigationClient(client)
	files := relata.NewFilesClient(client)

	fmt.Println("1. Ingest fixed events (idempotent on _pk)")
	opts := &relata.BulkOptions{OnConflict: "upsert", Durability: "sync"}
	must(ingest.Bulk(ctx, "AuthEvent", authEvents, opts))
	must(ingest.Bulk(ctx, "Session", sessions, opts))
	fmt.Printf("   %d AuthEvent, %d Session\n", len(authEvents), len(sessions))

	fmt.Println("2. Window rule and its alert")
	ruleID := fmt.Sprint(must(gov.CreateRule(ctx, rule, nil))["rule_id"])
	alertID := waitForAlert(ctx, gov, ruleID)
	fmt.Printf("   rule %s -> alert %s\n", ruleID, alertID)
	must(gov.UpdateAlert(ctx, alertID, &relata.UpdateAlertOptions{Status: "acknowledged", Note: "sdk example triage"}))

	fmt.Println("3. Evidence, and the alert reproduced from its query")
	evidence := must(inv.AlertEvidence(ctx, alertID, relata.EvidenceBounds{MaxEvents: 1000, DeadlineMs: 5000}))
	sql := fmt.Sprint(at(evidence, "evidence", "reproduce", "sql"))
	ids := []any{}
	for _, e := range list(evidence["events"]) {
		ids = append(ids, e["id"])
	}
	fmt.Printf("   completeness=%v events=%v\n", at(evidence, "completeness", "status"), ids)
	reproduced := must(client.Query(ctx, sql))
	fmt.Printf("   %s\n   -> %d rows\n", sql, len(reproduced.Rows))

	fmt.Println("4. Timeline")
	timeline := must(inv.AlertTimeline(ctx, alertID, relata.EvidenceBounds{}))
	for _, e := range list(timeline["entries"]) {
		label := e["id"]
		if label == nil {
			label = e["to"]
		}
		fmt.Printf("   %v  %-13v %v\n", e["at_ns"], e["kind"], label)
	}
	fmt.Printf("   pivot entities: %v\n", timeline["entities"])

	fmt.Println("5. Authorized pivot on the alert's user")
	pivot := must(inv.Pivot(ctx, alertID, relata.PivotRequest{TargetTypes: []string{"Session", "AuthEvent"}, Limit: 50}))
	for _, r := range list(pivot["results"]) {
		rowIDs := []any{}
		for _, row := range list(r["rows"]) {
			rowIDs = append(rowIDs, row["id"])
		}
		fmt.Printf("   %v: %v (truncated=%v)\n", r["object_type"], rowIDs, r["truncated"])
	}
	fmt.Printf("   completeness=%v\n", at(pivot, "completeness", "status"))

	fmt.Println("6. Saved search")
	def := relata.SavedSearch{Name: searchName, SQL: sql, Purpose: purpose}
	var saved map[string]any
	for _, s := range list(must(inv.ListSavedSearches(ctx, searchName, 0))["items"]) {
		if s["name"] == searchName {
			saved = must(inv.UpdateSavedSearch(ctx, fmt.Sprint(s["id"]), def, 0))
		}
	}
	if saved == nil {
		saved = must(inv.CreateSavedSearch(ctx, def))
	}
	run := must(inv.RunSavedSearch(ctx, fmt.Sprint(saved["id"]), nil))
	fmt.Printf("   %v v%v -> %d rows\n", saved["id"], saved["version"], len(list(run["data"])))

	fmt.Println("7. Archive the evidence export and check its SHA-256")
	record := must(json.Marshal(evidence))
	sum := sha256.Sum256(record)
	digest := hex.EncodeToString(sum[:])
	key := "cases/" + alertID + "/evidence.json"
	must(files.Put(ctx, key, record, &relata.FilesOptions{ContentType: "application/json"}))
	stored := must(files.Get(ctx, key, nil))
	if got := sha256.Sum256(stored.Bytes); hex.EncodeToString(got[:]) != digest {
		log.Fatalf("archived %s does not match its SHA-256", key)
	}
	fmt.Printf("   %s sha256=%s (verified on read-back)\n", key, digest)
	fmt.Printf("   events_sha256=%v\n", at(evidence, "export", "events_sha256"))
}
