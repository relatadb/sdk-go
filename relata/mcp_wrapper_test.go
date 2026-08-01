package relata

// #2322 — 42 (+4) previously Rust-only MCP tool wrappers, ported to Go.
//
// Each test asserts the wire tool `name` and the `arguments` sent match the
// server's real registration (source of truth: the `input_schema` blocks in
// `crates/relata-cli/src/serve.rs` and `crates/relata-cli/src/serve/mcp/procedure.rs`),
// not merely that the method exists.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mcpCapture spins up a test server that records the /mcp/tools/call body
// into gotBody and always replies with a minimal, valid MCP envelope.
func mcpCapture(t *testing.T) (*McpClient, *map[string]any) {
	t.Helper()
	gotBody := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		inner := `{"ok":true}`
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}]}`, strconvQuote(inner))
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(srv, nil)
	return NewMcpClient(c), &gotBody
}

func argsOf(t *testing.T, gotBody *map[string]any) map[string]any {
	t.Helper()
	args, ok := (*gotBody)["arguments"].(map[string]any)
	if !ok {
		t.Fatalf("arguments missing or wrong type: %v", *gotBody)
	}
	return args
}

func TestMcpClient_RememberBatch_SendsItems(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	_, err := mcp.RememberBatch(context.Background(), []map[string]any{{"content": "a"}}, &RememberBatchOptions{Purpose: "analytics"})
	if err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "remember_batch" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
	args := argsOf(t, gotBody)
	if args["purpose"] != "analytics" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_Recognize_SendsID(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.Recognize(context.Background(), "mem-1", ""); err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "recognize" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
	if argsOf(t, gotBody)["id"] != "mem-1" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_EpisodesIn_SendsSessionIDAndLimit(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.EpisodesIn(context.Background(), "sess-1", &EpisodesInOptions{Limit: 5}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["session_id"] != "sess-1" || args["limit"] != float64(5) {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_Justify_SendsID(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.Justify(context.Background(), "mem-1", ""); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["id"] != "mem-1" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_Consolidate_SendsIDAndContent(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.Consolidate(context.Background(), "mem-1", "updated", &ConsolidateOptions{Confidence: 0.5}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["id"] != "mem-1" || args["content"] != "updated" || args["confidence"] != 0.5 {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_Forget_SendsIDAndRetainDays(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.Forget(context.Background(), "mem-1", &ForgetOptions{RetainDays: 30}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["id"] != "mem-1" || args["retain_days"] != float64(30) {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_RememberProcedure_SendsSchemaFields(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.RememberProcedure(context.Background(), "agent-1", "onboarding", "do the thing", ""); err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "remember_procedure" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
	args := argsOf(t, gotBody)
	if args["agent_id"] != "agent-1" || args["name"] != "onboarding" || args["instruction_text"] != "do the thing" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_RecallProcedure_SendsAgentID(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.RecallProcedure(context.Background(), "agent-1", &RecallProcedureOptions{Name: "onboarding", AllVersions: true}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["agent_id"] != "agent-1" || args["name"] != "onboarding" || args["all_versions"] != true {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_Associate_SendsFromToRelation(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.Associate(context.Background(), "mem-1", "mem-2", &AssociateOptions{Relation: "contradicts"}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["from_id"] != "mem-1" || args["to_id"] != "mem-2" || args["relation"] != "contradicts" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_Resolve_SendsID(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.Resolve(context.Background(), "mem-1", ""); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["id"] != "mem-1" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_Summarise_SendsIDs(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.Summarise(context.Background(), &SummariseOptions{IDs: []string{"mem-1", "mem-2"}, Scope: "topic:fraud"}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	ids, _ := args["ids"].([]any)
	if len(ids) != 2 || args["scope"] != "topic:fraud" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_NlQuery_SendsQuery(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.NlQuery(context.Background(), "find all persons in Dublin", &NlQueryOptions{Interpret: true}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["query"] != "find all persons in Dublin" || args["interpret"] != true {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_EraseSubject_SendsSubjectAndReason(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.EraseSubject(context.Background(), "alice@example.com", &McpEraseSubjectOptions{Reason: "user-request"}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["subject"] != "alice@example.com" || args["reason"] != "user-request" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_IngestMedia_SendsObjectType(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.IngestMedia(context.Background(), "MediaContent", &IngestMediaOptions{BytesB64: "Zm9v"}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["object_type"] != "MediaContent" || args["bytes_b64"] != "Zm9v" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_SimilarMultimodal_SendsEntityTypeAndID(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.SimilarMultimodal(context.Background(), "Document", "doc-1", &SimilarMultimodalOptions{TopK: 5}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["entity_type"] != "Document" || args["id"] != "doc-1" {
		t.Fatalf("args = %v", args)
	}
	if _, present := args["entity_id"]; present {
		t.Fatalf("entity_id must not be sent: %v", args)
	}
}

func TestMcpClient_HybridSearch_SendsEntityTypeAndQuery(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.HybridSearch(context.Background(), "Document", "fraud ring", nil); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["entity_type"] != "Document" || args["query"] != "fraud ring" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_PathsBetween_SendsFromAndTo(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.PathsBetween(context.Background(), "a", "b", &PathsBetweenOptions{MaxHops: 6}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["from"] != "a" || args["to"] != "b" || args["max_hops"] != float64(6) {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_ListLinkTypes_SendsNoArgs(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ListLinkTypes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "list_link_types" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
}

func TestMcpClient_ServerHealth_SendsNoArgs(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ServerHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "server_health" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
}

func TestMcpClient_JobStatus_SendsNoArgs(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.JobStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "job_status" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
}

func TestMcpClient_Metrics_SendsNoArgs(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.Metrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "metrics" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
}

func TestMcpClient_ListRules_SendsNoArgs(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ListRules(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "list_rules" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
}

func TestMcpClient_CreateRule_SendsNameAndConditionSQL(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.CreateRule(context.Background(), "high-value-txn", "amount > 10000", &McpCreateRuleOptions{Severity: "high"}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["name"] != "high-value-txn" || args["condition_sql"] != "amount > 10000" || args["severity"] != "high" || args["purpose"] != "security" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_ImportSigma_SendsYamlKey(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ImportSigma(context.Background(), "title: test\n", ""); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["yaml"] != "title: test\n" {
		t.Fatalf("args = %v", args)
	}
	if _, present := args["sigma_yaml"]; present {
		t.Fatalf("sigma_yaml must not be sent: %v", args)
	}
}

func TestMcpClient_ListJobs_SendsNoArgs(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ListJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "list_jobs" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
}

func TestMcpClient_ScheduleJob_SendsName(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ScheduleJob(context.Background(), "transaction_ring"); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["name"] != "transaction_ring" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_ListWorkflows_SendsNoArgs(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ListWorkflows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*gotBody)["name"] != "list_workflows" {
		t.Fatalf("name = %v", (*gotBody)["name"])
	}
}

func TestMcpClient_RunWorkflow_SendsName(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.RunWorkflow(context.Background(), "aml-review"); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["name"] != "aml-review" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_WorkflowStatus_SendsRunID(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.WorkflowStatus(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["run_id"] != "run-1" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_TraceCrypto_SendsAddress(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.TraceCrypto(context.Background(), "bc1qxyz", &TraceCryptoOptions{MaxHops: 3}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["address"] != "bc1qxyz" || args["max_hops"] != float64(3) || args["purpose"] != "analytics" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_BeneficialOwnership_SendsParty(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.BeneficialOwnership(context.Background(), "Acme Holdings", nil); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["party"] != "Acme Holdings" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_ReconstructWire_SendsAccount(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ReconstructWire(context.Background(), "IE12BOFI90000112345678", nil); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["account"] != "IE12BOFI90000112345678" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_TraceHawala_SendsSeed(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.TraceHawala(context.Background(), "actor-1", nil); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["seed"] != "actor-1" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_Geofence_SendsLatLon(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.Geofence(context.Background(), 53.35, -6.26, &GeofenceOptions{RadiusM: 500}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["lat"] != 53.35 || args["lon"] != -6.26 || args["radius_m"] != float64(500) {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_ResolveEntityIdentity_SendsIdentity(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ResolveEntityIdentity(context.Background(), "alice@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["identity"] != "alice@example.com" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_DetectCommunities_SendsEntityType(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.DetectCommunities(context.Background(), "Person", &DetectCommunitiesOptions{Algo: "leiden"}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["entity_type"] != "Person" || args["algo"] != "leiden" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_RankKeyNodes_SendsEntityTypeAndMetric(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.RankKeyNodes(context.Background(), "Person", &RankKeyNodesOptions{Metric: "betweenness"}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["entity_type"] != "Person" || args["metric"] != "betweenness" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_HubAuthority_SendsEntityType(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.HubAuthority(context.Background(), "Person", nil); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["entity_type"] != "Person" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_PredictLinks_SendsEntityType(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.PredictLinks(context.Background(), "Person", &PredictLinksOptions{FromID: "p1", ToID: "p2", Method: "jaccard"}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["entity_type"] != "Person" || args["from_id"] != "p1" || args["to_id"] != "p2" || args["method"] != "jaccard" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_FindScc_SendsEntityType(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.FindScc(context.Background(), "Person", ""); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["entity_type"] != "Person" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_ScreenSanctions_SendsName(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.ScreenSanctions(context.Background(), "John Doe", &ScreenSanctionsOptions{Threshold: 0.9, HasThreshold: true}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["name"] != "John Doe" || args["threshold"] != 0.9 || args["purpose"] != "compliance_review" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_AggregateStats_SendsEntityType(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.AggregateStats(context.Background(), "Transaction", &AggregateStatsOptions{Agg: "SUM", Column: "amount"}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["entity_type"] != "Transaction" || args["agg"] != "SUM" || args["column"] != "amount" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_InvestigateEntity_SendsEntityTypeAndID(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.InvestigateEntity(context.Background(), "Person", "p1", ""); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["entity_type"] != "Person" || args["id"] != "p1" {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_FindThreats_SendsEntityType(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.FindThreats(context.Background(), "Person", ""); err != nil {
		t.Fatal(err)
	}
	if argsOf(t, gotBody)["entity_type"] != "Person" {
		t.Fatalf("args = %v", (*gotBody)["arguments"])
	}
}

func TestMcpClient_SearchVideoFrames_SendsQueryID(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.SearchVideoFrames(context.Background(), "frame-1", &SearchVideoFramesOptions{TopK: 10}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["query_id"] != "frame-1" || args["top_k"] != float64(10) {
		t.Fatalf("args = %v", args)
	}
}

func TestMcpClient_FaceMatch_SendsProbeID(t *testing.T) {
	mcp, gotBody := mcpCapture(t)
	if _, err := mcp.FaceMatch(context.Background(), "probe-1", &FaceMatchOptions{Threshold: 0.9}); err != nil {
		t.Fatal(err)
	}
	args := argsOf(t, gotBody)
	if args["probe_id"] != "probe-1" || args["threshold"] != 0.9 {
		t.Fatalf("args = %v", args)
	}
}
