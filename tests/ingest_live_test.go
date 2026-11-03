package tests

/**
 * Ingestion-contract run through the Go SDK against a live server (#5689,
 * #5742, #5743). Mirrors scripts/sdk_ingest_e2e.py: idempotent typed bulk with a
 * durable source checkpoint, retry/conflict behaviour, upsert, CSV text, a
 * contract with quarantine, and prepared document rows from the shared
 * two-provider fixture read back in order.
 *
 * Skips unless RELATA_TOKEN (a registry data token) is set, so `go test ./...`
 * stays server-free. Run: scripts/sdk_ingest_live.sh (needs a running server).
 */

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v3/relata"
)

func ingestRows(t *testing.T, c *relata.Client, sql string) []map[string]any {
	t.Helper()
	res, err := c.Query(context.Background(), sql, relata.WithPurpose("investigation"))
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return res.Rows
}

func waitIngestRows(t *testing.T, c *relata.Client, sql string, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := ingestRows(t, c, sql)
		if len(got) >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestIngestContractLive(t *testing.T) {
	token := os.Getenv("RELATA_TOKEN")
	if token == "" {
		t.Skip("RELATA_TOKEN not set; live ingestion run skipped")
	}
	host := getEnv("RELATA_HOST", "http://localhost:9090")
	ctx := context.Background()
	client := relata.New(host, &relata.ClientOptions{BearerToken: token, DefaultPurpose: "investigation", Timeout: 30 * time.Second})
	ingest := relata.NewIngestClient(client)
	control := relata.NewIngestControlClient(client)
	run := fmt.Sprint(time.Now().UnixNano())
	fixtures := filepath.Join("..", "..", "contract-tests", "fixtures")

	ty, key, source := "GoIng"+run, "go-batch-"+run, "go/source-"+run
	batch := []map[string]any{
		{"_pk": "a", "qty": 3, "note": nil, "wide": "9007199254740993"},
		{"_pk": "b", "qty": 4, "note": "x", "wide": "12345678901234567890.123"},
	}
	cp := func(pos, expected string) *relata.SourceCheckpoint {
		return &relata.SourceCheckpoint{Source: source, Position: pos, ExpectedPosition: &expected}
	}

	// 1. idempotent, synchronous, checkpointed bulk
	r1, err := ingest.Bulk(ctx, ty, batch, &relata.BulkOptions{IdempotencyKey: key, Durability: "sync", SourceCheckpoint: cp("10", "")})
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if fmt.Sprint(r1["inserted"]) != "2" {
		t.Fatalf("first batch must insert 2 rows: %v", r1)
	}
	if sc, _ := r1["source_checkpoint"].(map[string]any); sc["advanced"] != true {
		t.Fatalf("checkpoint must advance at the durable commit: %v", r1)
	}
	found := false
	for _, r := range ingestRows(t, client, "SELECT _pk, note, wide FROM "+ty) {
		if r["_pk"] == "a" && r["note"] == nil && r["wide"] == "9007199254740993" {
			found = true
		}
	}
	if !found {
		t.Fatal("null and exact text must survive")
	}

	// 2. retry replays the recorded response, writes nothing
	r2, err := ingest.Bulk(ctx, ty, batch, &relata.BulkOptions{IdempotencyKey: key, Durability: "sync", SourceCheckpoint: cp("10", "")})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if r2["idempotent_replay"] != true || fmt.Sprint(r2["inserted"]) != "2" {
		t.Fatalf("retry must replay the recorded response: %v", r2)
	}
	if n := len(ingestRows(t, client, "SELECT _pk FROM "+ty)); n != 2 {
		t.Fatalf("retry added rows: %d", n)
	}

	// 3. same key, different body
	_, err = ingest.Bulk(ctx, ty, []map[string]any{{"_pk": "a", "qty": 99}}, &relata.BulkOptions{IdempotencyKey: key, Durability: "sync"})
	if statusOf(err) != 409 {
		t.Fatalf("same key with a different body must be 409, got %v", err)
	}

	// 4. stale compare-and-set checkpoint
	_, err = ingest.Bulk(ctx, ty, []map[string]any{{"_pk": "c", "qty": 1}}, &relata.BulkOptions{Durability: "sync", OnConflict: "upsert", SourceCheckpoint: cp("11", "5")})
	if statusOf(err) != 409 {
		t.Fatalf("stale expected_position must be 409, got %v", err)
	}
	if n := len(ingestRows(t, client, "SELECT _pk FROM "+ty)); n != 2 {
		t.Fatalf("refused batch wrote rows: %d", n)
	}
	if c, err := control.Checkpoint(ctx, source); err != nil || c["position"] != "10" {
		t.Fatalf("checkpoint must be readable and unchanged: %v %v", c, err)
	}

	// 5. upsert supersedes in place
	r5, err := ingest.Bulk(ctx, ty, []map[string]any{{"_pk": "b", "qty": 40, "note": "y"}}, &relata.BulkOptions{OnConflict: "upsert", Durability: "sync"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	live := ingestRows(t, client, "SELECT _pk, qty FROM "+ty+" WHERE _pk = 'b'")
	if fmt.Sprint(r5["updated"]) != "1" || len(live) != 1 || fmt.Sprint(live[0]["qty"]) != "40" {
		t.Fatalf("upsert must update in place: %v %v", r5, live)
	}

	// 6. CSV text is stored as the exact source text
	csvTy := "GoCsv" + run
	csv, err := os.ReadFile(filepath.Join(fixtures, "structured-ingest-q03.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.BulkCSV(ctx, csvTy, string(csv), nil); err != nil {
		t.Fatalf("csv: %v", err)
	}
	got := waitIngestRows(t, client, "SELECT * FROM "+csvTy, 1)
	if len(got) != 1 || fmt.Sprint(got[0]["exact_int64"]) != "9007199254740993" ||
		fmt.Sprint(got[0]["decimal_text"]) != "12345678901234567890.12345678901234567890" {
		t.Fatalf("CSV cells must be kept as exact text: %v", got)
	}

	// 7. contract + quarantine = explicit partial acceptance
	dataset, cty := "goreadings"+run, "GoReading"+run
	if _, err := control.PutContract(ctx, dataset, map[string]any{
		"version": 1, "object_type": cty,
		"fields": []map[string]any{
			{"name": "id", "type": "string", "required": true},
			{"name": "temp", "type": "number", "min": -50, "max": 150, "required": true},
		},
	}); err != nil {
		t.Fatalf("put contract: %v", err)
	}
	rc, err := ingest.Bulk(ctx, cty, []map[string]any{
		{"_pk": "r1", "id": "r1", "temp": 20.5, "_source_offset": "seg:0"},
		{"_pk": "r2", "id": "r2", "temp": 999, "_source_offset": "seg:1"},
	}, &relata.BulkOptions{Contract: &relata.ContractRef{Dataset: dataset}})
	if err != nil {
		t.Fatalf("contract batch: %v", err)
	}
	c, _ := rc["contract"].(map[string]any)
	if fmt.Sprint(c["accepted"]) != "1" || fmt.Sprint(c["quarantined"]) != "1" {
		t.Fatalf("valid record accepted, poison quarantined: %v", rc)
	}
	q, err := control.ListQuarantine(ctx, &relata.QuarantineListOptions{Dataset: dataset})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := q["items"].([]any)
	first, _ := items[0].(map[string]any)
	if len(items) != 1 || first["source_offset"] != "seg:1" || first["reason"] != "above_max:temp" {
		t.Fatalf("quarantine must name the offset and reason: %v", q)
	}

	// 8. prepared documents, two independent providers, read back in order
	raw, err := os.ReadFile(filepath.Join(fixtures, "document-ingest-preservation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Providers []struct {
			Name    string           `json:"name"`
			Objects []map[string]any `json:"objects"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, p := range fixture.Providers {
		remap := map[string]string{}
		for _, o := range p.Objects {
			for _, f := range []string{"_pk", "chunk_id", "report_id", "node_id"} {
				if v, ok := o[f].(string); ok {
					if _, seen := remap[v]; !seen {
						remap[v] = v + "@" + run
					}
				}
			}
		}
		var fix func(v any) any
		fix = func(v any) any {
			switch x := v.(type) {
			case string:
				for old, next := range remap {
					if x == old || strings.HasPrefix(x, old+":") {
						return next + x[len(old):]
					}
				}
				return x
			case []any:
				out := make([]any, len(x))
				for i, e := range x {
					out[i] = fix(e)
				}
				return out
			}
			return v
		}
		objs := make([]map[string]any, 0, len(p.Objects))
		for _, o := range p.Objects {
			c := map[string]any{}
			for k, v := range o {
				c[k] = fix(v)
			}
			objs = append(objs, c)
		}
		resp, err := client.IngestDocument(ctx, objs, "investigation")
		if err != nil {
			t.Fatalf("%s ingest: %v", p.Name, err)
		}
		var task *relata.IngestDocumentTaskStatus
		for i := 0; i < 200; i++ {
			if task, err = client.IngestDocumentStatus(ctx, resp.TaskID); err != nil {
				t.Fatal(err)
			}
			if task.Status != "pending" && task.Status != "queued" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if task.TaskID != resp.TaskID || task.Status != "complete" || task.RowsWritten != len(objs) || task.RowsDropped != 0 {
			t.Fatalf("%s: task must complete with task_id preserved and every row written: %+v", p.Name, task)
		}
		var want []map[string]any
		for _, o := range objs {
			if o["_type"] == "DocumentChunk" {
				want = append(want, o)
			}
		}
		// order by sequence_index (insertion sort; the fixtures hold a handful of chunks)
		for i := 1; i < len(want); i++ {
			for j := i; j > 0 && fmt.Sprint(want[j]["sequence_index"]) < fmt.Sprint(want[j-1]["sequence_index"]); j-- {
				want[j], want[j-1] = want[j-1], want[j]
			}
		}
		report := want[0]["report_id"]
		chunks := waitIngestRows(t, client, fmt.Sprintf("SELECT chunk_id, sequence_index, prev_chunk_id, next_chunk_id, page_start FROM DocumentChunk WHERE report_id = '%v' ORDER BY sequence_index", report), len(want))
		var gotIDs, wantIDs []string
		var gotLinks, wantLinks [][2]string
		for i := range want {
			wantIDs = append(wantIDs, fmt.Sprint(want[i]["chunk_id"]))
			wantLinks = append(wantLinks, [2]string{fmt.Sprint(want[i]["prev_chunk_id"]), fmt.Sprint(want[i]["next_chunk_id"])})
		}
		for _, g := range chunks {
			gotIDs = append(gotIDs, fmt.Sprint(g["chunk_id"]))
			gotLinks = append(gotLinks, [2]string{fmt.Sprint(g["prev_chunk_id"]), fmt.Sprint(g["next_chunk_id"])})
		}
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Fatalf("%s: chunk IDs and order: got %v want %v", p.Name, gotIDs, wantIDs)
		}
		if !reflect.DeepEqual(gotLinks, wantLinks) {
			t.Fatalf("%s: neighbor links: got %v want %v", p.Name, gotLinks, wantLinks)
		}
	}
}
