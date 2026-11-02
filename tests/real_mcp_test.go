package tests

import (
	"strings"
	"testing"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// MCP journey: a tool-using agent connects, discovers the tools, queries and
// searches the governed knowledge, reads the graph, writes case notes, stores
// memories and a RAG answer, and runs graph analytics, all via McpClient.
func TestRealJourneyMCP(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	m := relata.NewMcpClient(c)
	const P = "investigation"

	init, err := m.Initialize(ctx, &relata.McpInitializeOptions{ClientID: "sdk-real-go", Version: "1.0"})
	must(t, err, "initialize")
	if len(init) == 0 {
		t.Fatalf("empty initialize result")
	}
	tools, err := m.ListTools(ctx)
	must(t, err, "list tools")
	names := map[string]bool{}
	for _, tl := range tools {
		names[str(tl["name"])] = true
	}
	for _, want := range []string{"query_knowledge", "remember", "recall"} {
		if !names[want] {
			t.Errorf("tool %q missing from the %d advertised tools", want, len(tools))
		}
	}

	ty := uniq("RmHost")
	var rows []map[string]any
	for _, e := range [][2]string{{"h1", "h2"}, {"h2", "h3"}, {"h3", "h1"}, {"h3", "h4"}} {
		rows = append(rows, map[string]any{"_pk": e[0] + e[1], "id": e[0] + e[1], "subject": e[0], "subject_type": ty, "object": e[1], "object_type": ty, "link_type": "talks_to", "weight": 1.0, "note": "beacon to evil.example"})
	}
	ingestRowsWait(t, c, ty, rows)

	q, err := m.QueryKnowledge(ctx, "SELECT _pk FROM "+ty+" ORDER BY _pk", P)
	must(t, err, "query knowledge")
	if !strings.Contains(str(q), "h1h2") || !strings.Contains(str(q), "h3h4") {
		t.Fatalf("query_knowledge lost the ingested rows: %v", q)
	}
	if s, err := m.SearchKnowledge(ctx, "evil.example beacon", P, 5); err != nil {
		t.Errorf("search knowledge: %v", err)
	} else {
		t.Logf("search knowledge: %v", s)
	}
	if ep, err := m.ExplainPolicy(ctx, "SELECT * FROM "+ty, P); err != nil || len(ep) == 0 {
		t.Errorf("explain policy: %v %v", ep, err)
	}
	et, err := m.ListEntityTypes(ctx)
	must(t, err, "list entity types")
	if !strings.Contains(str(et), ty) {
		t.Errorf("list entity types lacks %s: %v", ty, et)
	}
	ge, err := m.GetEntities(ctx, ty, &relata.GetEntitiesOptions{Limit: 10})
	must(t, err, "get entities")
	if !strings.Contains(str(ge), "h1") {
		t.Errorf("get entities: %v", ge)
	}
	if se, err := m.SearchEntities(ctx, "evil.example", &relata.SearchEntitiesOptions{ObjectType: ty}); err != nil {
		t.Errorf("search entities: %v", err)
	} else {
		t.Logf("search entities: %v", se)
	}
	if r, err := m.FindInSocialCorpus(ctx, ty, "beacon", &relata.FindInSocialCorpusOptions{TopK: 3}); err != nil {
		t.Logf("social corpus: %v", err)
	} else {
		t.Logf("social corpus: %v", r)
	}
	email := strings.ToLower(ty) + "@example.com"
	if r, err := m.LookupIdentity(ctx, email, &relata.LookupIdentityOptions{Purpose: P}); err != nil {
		t.Errorf("lookup identity: %v", err)
	} else {
		t.Logf("lookup identity: %v", r)
	}
	if r, err := m.ResolveEntityIdentity(ctx, email, P); err != nil {
		t.Errorf("resolve entity identity: %v", err)
	} else {
		t.Logf("resolve entity identity: %v", r)
	}
	for name, call := range map[string]func() (map[string]any, error){
		"profile": func() (map[string]any, error) { return m.GetEntityProfile(ctx, "h1", P) },
		"timeline": func() (map[string]any, error) {
			return m.GetTimeline(ctx, "h1", P, &relata.GetTimelineOptions{Limit: 5})
		},
		"connections": func() (map[string]any, error) {
			return m.FindConnections(ctx, "h1", P, &relata.FindConnectionsOptions{Limit: 5})
		},
		"relationships": func() (map[string]any, error) {
			return m.GetRelationships(ctx, "h1", P, &relata.GetRelationshipsOptions{Limit: 5})
		},
		"link types": func() (map[string]any, error) { return m.ListLinkTypes(ctx) },
		"paths": func() (map[string]any, error) {
			return m.PathsBetween(ctx, "h1", "h4", &relata.PathsBetweenOptions{MaxHops: 4, Purpose: P})
		},
		"communities": func() (map[string]any, error) {
			return m.DetectCommunities(ctx, ty, &relata.DetectCommunitiesOptions{Purpose: P})
		},
		"rank": func() (map[string]any, error) {
			return m.RankKeyNodes(ctx, ty, &relata.RankKeyNodesOptions{Purpose: P})
		},
		"hub authority": func() (map[string]any, error) {
			return m.HubAuthority(ctx, ty, &relata.HubAuthorityOptions{Purpose: P})
		},
		"predict links": func() (map[string]any, error) {
			return m.PredictLinks(ctx, ty, &relata.PredictLinksOptions{FromID: "h1", ToID: "h4", Purpose: P})
		},
		"scc": func() (map[string]any, error) { return m.FindScc(ctx, ty, P) },
		"aggregate": func() (map[string]any, error) {
			return m.AggregateStats(ctx, ty, &relata.AggregateStatsOptions{Agg: "count", Purpose: P})
		},
		"hybrid search": func() (map[string]any, error) {
			return m.HybridSearch(ctx, ty, "beacon", &relata.McpHybridSearchOptions{TopK: 3, Purpose: P})
		},
		"similar mm": func() (map[string]any, error) {
			return m.SimilarMultimodal(ctx, ty, "h1h2", &relata.SimilarMultimodalOptions{TopK: 2, Purpose: P})
		},
		"geofence": func() (map[string]any, error) {
			return m.Geofence(ctx, 24.86, 67.0, &relata.GeofenceOptions{RadiusM: 500, TargetType: ty, Purpose: P})
		},
	} {
		r, err := call()
		if err != nil {
			t.Logf("%s: %v", name, err)
			continue
		}
		t.Logf("%s ok (%d keys)", name, len(r))
	}

	// Case work: notes, summary, audit.
	cid := uniq("case")
	if _, err := m.AddCaseNote(ctx, cid, "Beacon cluster around h1..h3", &relata.AddCaseNoteOptions{Author: "analyst", Category: "finding", EntitiesMentioned: []string{"h1", "h3"}}); err != nil {
		t.Errorf("add case note: %v", err)
	}
	cs, err := m.GetCaseSummary(ctx, P, nil)
	if err != nil {
		t.Errorf("case summary: %v", err)
	} else {
		t.Logf("case summary: %d keys", len(cs))
	}
	at, err := m.GetAuditTrail(ctx, &relata.GetAuditTrailOptions{Limit: 5, Purpose: P})
	if err != nil || len(at) == 0 {
		t.Errorf("audit trail: %v %v", at, err)
	}

	// RAG storage tools.
	ra, err := m.RagStoreAnswer(ctx, "what beacons?", "h1..h3 beacon to evil.example", P, &relata.RagStoreAnswerOptions{})
	if err != nil {
		t.Errorf("rag store answer: %v", err)
	} else {
		t.Logf("rag store answer: %v", ra)
	}
	if r, err := m.RagStoreElements(ctx, []map[string]any{{"type": "NarrativeText", "text": "The h1 host beacons."}}, "report-"+cid+".txt", P); err != nil {
		t.Errorf("rag store elements: %v", err)
	} else {
		t.Logf("rag store elements: %v", r)
	}
	if r, err := m.IngestDocument(ctx, "doc-"+cid, "Host h1 beacons to evil.example daily.", P, &relata.IngestDocumentOptions{Label: "report"}); err != nil {
		t.Errorf("mcp ingest document: %v", err)
	} else {
		t.Logf("mcp ingest document: %v", r)
	}

	// Agent memory over MCP.
	tag := uniq("mem")
	rm, err := m.Remember(ctx, "Operator prefers the "+tag+" dashboard", P, &relata.RememberOptions{Confidence: 0.8, MemoryClass: "semantic"})
	must(t, err, "remember")
	mid := str(rm["id"])
	if mid == "" || mid == "<nil>" {
		t.Fatalf("remember returned no id: %v", rm)
	}
	rb, err := m.RememberBatch(ctx, []map[string]any{{"content": "batch one " + tag}, {"content": "batch two " + tag}}, &relata.RememberBatchOptions{Purpose: P})
	if err != nil {
		t.Errorf("remember batch: %v", err)
	} else {
		t.Logf("remember batch: %v", rb)
	}
	rc, err := m.Recall(ctx, tag+" dashboard", P, &relata.RecallOptions{TopK: 5})
	must(t, err, "recall")
	if !strings.Contains(str(rc), "dashboard") {
		t.Errorf("recall: %v", rc)
	}
	for name, call := range map[string]func() (map[string]any, error){
		"recognize": func() (map[string]any, error) { return m.Recognize(ctx, mid, P) },
		"justify":   func() (map[string]any, error) { return m.Justify(ctx, mid, P) },
		"resolve":   func() (map[string]any, error) { return m.Resolve(ctx, mid, P) },
		"episodes": func() (map[string]any, error) {
			return m.EpisodesIn(ctx, "sess-"+tag, &relata.EpisodesInOptions{Limit: 5, Purpose: P})
		},
		"consolidate": func() (map[string]any, error) {
			return m.Consolidate(ctx, mid, "Operator prefers the "+tag+" dashboard (confirmed)", &relata.ConsolidateOptions{Confidence: 0.9, Purpose: P})
		},
		"associate": func() (map[string]any, error) {
			return m.Associate(ctx, mid, mid, &relata.AssociateOptions{Relation: "related_to", Purpose: P})
		},
		"summarise": func() (map[string]any, error) {
			return m.Summarise(ctx, &relata.SummariseOptions{IDs: []string{mid}, Purpose: P})
		},
		"remember procedure": func() (map[string]any, error) {
			return m.RememberProcedure(ctx, "agent-"+tag, "triage", "1. check 2. page", P)
		},
		"recall procedure": func() (map[string]any, error) {
			return m.RecallProcedure(ctx, "agent-"+tag, &relata.RecallProcedureOptions{Name: "triage", Purpose: P})
		},
		"forget": func() (map[string]any, error) { return m.Forget(ctx, mid, &relata.ForgetOptions{Purpose: P}) },
	} {
		r, err := call()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		t.Logf("%s ok (%d keys)", name, len(r))
	}

	// Rules, jobs, metrics, health through the tool surface.
	rule, err := m.CreateRule(ctx, uniq("mcprule"), "sev > 5", ty, &relata.McpCreateRuleOptions{Severity: "medium", Purpose: P})
	if err != nil {
		t.Errorf("create rule: %v", err)
	} else {
		t.Logf("create rule: %v", rule)
	}
	for name, call := range map[string]func() (map[string]any, error){
		"list rules": func() (map[string]any, error) { return m.ListRules(ctx) },
		"list jobs":  func() (map[string]any, error) { return m.ListJobs(ctx) },
		"job status": func() (map[string]any, error) { return m.JobStatus(ctx) },
		"health":     func() (map[string]any, error) { return m.ServerHealth(ctx) },
		"metrics":    func() (map[string]any, error) { return m.Metrics(ctx) },
	} {
		r, err := call()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		t.Logf("%s ok (%d keys)", name, len(r))
	}
	if _, err := m.ScheduleJob(ctx, "no-such-job-"+tag); err == nil {
		t.Errorf("scheduling an unknown job must fail")
	}
	// Erase through MCP: governed, certified.
	if r, err := m.EraseSubject(ctx, "nobody-"+tag+"@example.com", &relata.McpEraseSubjectOptions{Reason: "journey"}); err != nil {
		// Certified erasure needs the durable deletion-intent authority; without it the
		// refusal must be typed and must not claim any key destruction.
		wantProblem(t, err, 503, "")
	} else {
		t.Logf("erase subject: %v", r)
	}
	// Raw tool call and an unknown tool.
	if r, err := m.CallTool(ctx, "list_entity_types", map[string]any{}); err != nil || len(r) == 0 {
		t.Errorf("call tool: %v %v", r, err)
	}
	if _, err := m.CallTool(ctx, "no_such_tool_"+tag, map[string]any{}); err == nil {
		t.Errorf("unknown tool must fail")
	}
}
