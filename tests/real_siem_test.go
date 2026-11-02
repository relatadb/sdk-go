package tests

/**
 * Journey: SIEM / investigation. Ingest events, a detection rule fires on a
 * burst, the analyst triages the alert (evidence, timeline, pivot, saved search),
 * tunes the rule, and the whole thing is visible in the audit chain. Driven only
 * through the Go SDK.
 */

import (
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

func findAlert(t *testing.T, g *relata.GovernanceClient, ruleID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		al, err := g.ListAlerts(ctxT(t), &relata.ListAlertsOptions{Limit: 200})
		must(t, err, "list alerts")
		for _, a := range al {
			if a["rule_id"] == ruleID {
				return a
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no alert fired for rule %s", ruleID)
	return nil
}

func TestRealJourneySIEM(t *testing.T) {
	c := realClient(t)
	pa := roleClient(t, "platform-admin")
	g := relata.NewGovernanceClient(c)
	gAdmin := relata.NewGovernanceClient(pa)
	inv := relata.NewInvestigationClient(c)
	st := relata.NewStreamingClient(c)
	ctx := ctxT(t)
	ty := uniq("SiemAuth")

	ingestRowsWait(t, c, ty, []map[string]any{{"id": "ok-1", "action": "login_ok", "sev": 1, "src_ip": "10.0.0.1", "user": "bob"}})

	// Detection rule + live alert stream opened BEFORE the attack so the push path is proven.
	rule, err := g.CreateRule(ctx, map[string]any{"name": "brute-" + ty, "condition_sql": "sev >= 8", "target_type": ty, "severity": "high", "mitre_technique": "T1110"}, nil)
	must(t, err, "create rule")
	rid, _ := rule["rule_id"].(string)
	if rid == "" || rule["enabled"] != true {
		t.Fatalf("rule: %v", rule)
	}
	rules, err := g.ListRules(ctx, ty)
	must(t, err, "list rules")
	if len(rules) != 1 || rules[0]["condition_sql"] != "sev >= 8" {
		t.Fatalf("list rules: %v", rules)
	}
	one, err := g.GetRule(ctx, rid)
	must(t, err, "get rule")
	if one["target_type"] != ty || one["mitre_technique"] != "T1110" {
		t.Fatalf("get rule: %v", one)
	}
	_, err = g.GetRule(ctx, "no-such-rule")
	wantProblem(t, err, 404, "")

	// The attack: three failed logins above threshold from one source.
	ingestRowsWait(t, c, ty, []map[string]any{
		{"id": "bad-1", "action": "login_failed", "sev": 9, "src_ip": "10.0.0.9", "user": "admin"},
		{"id": "bad-2", "action": "login_failed", "sev": 9, "src_ip": "10.0.0.9", "user": "admin"},
		{"id": "bad-3", "action": "login_failed", "sev": 8, "src_ip": "10.0.0.9", "user": "root"},
	})
	alert := findAlert(t, g, rid)
	aid, _ := alert["alert_id"].(string)
	if alert["severity"] != "high" || alert["status"] != "open" || alert["matched_type"] != ty {
		t.Fatalf("alert: %v", alert)
	}
	// The alert stream is a per-connection drain of the tenant's alert queue (the
	// server closes each poll); the SDK's default reconnect loop turns it into a
	// feed. Our alert must arrive on it.
	sse, err := st.Alerts(ctx, nil)
	must(t, err, "alerts stream")
	defer sse.Close()
	pushed := make(chan map[string]any, 64)
	go func() {
		for sse.Next() {
			if d := sse.Event().Data; d != nil && d["type"] != "heartbeat" {
				pushed <- d
			}
		}
	}()
	timeout := time.After(20 * time.Second)
wait:
	for {
		select {
		case ev := <-pushed:
			if ev["rule_id"] == rid || strings.Contains(str(ev), rid) {
				break wait
			}
		case <-timeout:
			t.Error("the rule's alert was not delivered on the alert stream")
			break wait
		}
	}
	// Severity filter works.
	crit, err := g.ListAlerts(ctx, &relata.ListAlertsOptions{Severity: "critical", Limit: 200})
	must(t, err, "alerts critical")
	for _, a := range crit {
		if a["rule_id"] == rid {
			t.Fatalf("a high alert must not appear in the critical filter")
		}
	}

	// Triage.
	if _, err := g.UpdateAlert(ctx, aid, &relata.UpdateAlertOptions{Status: "bogus"}); err == nil {
		t.Fatal("unknown alert status must be refused")
	}
	upd, err := g.UpdateAlert(ctx, aid, &relata.UpdateAlertOptions{Status: "acknowledged", Assignee: "analyst-1", Note: "looking at 10.0.0.9"})
	must(t, err, "ack alert")
	if upd["status"] != "acknowledged" {
		t.Fatalf("update: %v", upd)
	}
	evid, err := inv.AlertEvidence(ctx, aid, relata.EvidenceBounds{MaxEvents: 10, DeadlineMs: 5000})
	must(t, err, "evidence")
	events, _ := evid["events"].([]any)
	if len(events) == 0 {
		t.Fatalf("evidence must return the matched event: %v", evid["completeness"])
	}
	tl, err := inv.AlertTimeline(ctx, aid, relata.EvidenceBounds{MaxEvents: 10})
	must(t, err, "timeline")
	if entries, _ := tl["entries"].([]any); len(entries) == 0 || tl["alert_id"] != aid {
		t.Fatalf("timeline: %v", tl)
	}
	pv, err := inv.Pivot(ctx, aid, relata.PivotRequest{Field: "src_ip", Value: "10.0.0.9", TargetTypes: []string{ty}, Limit: 10})
	must(t, err, "pivot")
	t.Logf("pivot keys: %v", keysOf(pv))

	// Saved search lifecycle with optimistic concurrency.
	name := "hunt-" + ty
	ss, err := inv.CreateSavedSearch(ctx, relata.SavedSearch{Name: name, SQL: "SELECT user, sev FROM " + ty + " WHERE src_ip = $1 AND sev >= $2", Purpose: "investigation", Params: []any{"10.0.0.9", 8}})
	must(t, err, "create saved search")
	sid, _ := ss["id"].(string)
	lst, err := inv.ListSavedSearches(ctx, name, 10)
	must(t, err, "list saved")
	if str(lst["total"]) != "1" {
		t.Fatalf("saved list: %v", lst)
	}
	got, err := inv.GetSavedSearch(ctx, sid)
	must(t, err, "get saved")
	if got["name"] != name || str(got["version"]) != "1" {
		t.Fatalf("saved get: %v", got)
	}
	run, err := inv.RunSavedSearch(ctx, sid, []any{"10.0.0.9", 8})
	must(t, err, "run saved")
	if str(run["rows"]) != "3" {
		t.Fatalf("saved search must return the 3 attack events: %v", run)
	}
	upv, err := inv.UpdateSavedSearch(ctx, sid, relata.SavedSearch{Name: name, SQL: "SELECT user FROM " + ty + " WHERE src_ip = $1", Purpose: "investigation", Description: "narrowed"}, 1)
	must(t, err, "update saved")
	if str(upv["version"]) != "2" {
		t.Fatalf("saved update: %v", upv)
	}
	_, err = inv.UpdateSavedSearch(ctx, sid, relata.SavedSearch{Name: name, SQL: "SELECT 1", Purpose: "investigation"}, 1)
	wantProblemAny(t, err) // stale expected_version
	if _, err := inv.DeleteSavedSearch(ctx, sid); err != nil {
		t.Fatalf("delete saved: %v", err)
	}
	_, err = inv.GetSavedSearch(ctx, sid)
	wantProblem(t, err, 404, "")

	// Tuning: snooze (analyst), suppress + exception (admin-level), then disable.
	tune0, err := g.GetRuleTuning(ctx, rid)
	must(t, err, "tuning")
	if tune0["snoozed"] != false {
		t.Fatalf("tuning before: %v", tune0)
	}
	if _, err := g.SnoozeRule(ctx, rid, 60); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	if tune1, _ := g.GetRuleTuning(ctx, rid); tune1["snoozed"] != true {
		t.Fatalf("rule must be snoozed: %v", tune1)
	}
	_, err = g.SuppressRule(ctx, rid, "host-9", &relata.SuppressRuleOptions{DurationSecs: 300})
	wantProblem(t, err, 403, "") // analysts cannot mute detection
	if _, err := gAdmin.SuppressRule(ctx, rid, "host-9", &relata.SuppressRuleOptions{Condition: "known scanner", DurationSecs: 300}); err != nil {
		t.Fatalf("admin suppress: %v", err)
	}
	if _, err := gAdmin.AddRuleException(ctx, rid, map[string]any{"entity_id": "host-1", "reason": "pentest"}); err != nil {
		t.Fatalf("add exception: %v", err)
	}
	tune2, _ := g.GetRuleTuning(ctx, rid)
	if !strings.Contains(str(tune2), "host-9") {
		t.Fatalf("suppression not visible in tuning: %v", tune2)
	}
	if _, err := gAdmin.RemoveRuleException(ctx, rid, "host-1"); err != nil {
		t.Fatalf("remove exception: %v", err)
	}
	dis, err := g.DisableRule(ctx, rid)
	must(t, err, "disable")
	if dis["enabled"] != false {
		t.Fatalf("disable: %v", dis)
	}

	// External alert from another tool (EDR) through the SDK.
	sa, err := c.SubmitAlert(ctx, relata.AlertSubmitRequest{IdempotencyKey: uniq("edr"), Purpose: "investigation", AlertType: "external", Severity: "high", MatchedType: ty, MatchedKey: "id", MatchedRowID: "bad-1", Context: map[string]any{"source": "edr"}})
	must(t, err, "submit alert")
	if sa.AlertID == "" {
		t.Fatalf("submit alert: %+v", sa)
	}
	// Append-only event append needs an installed schema package: typed refusal.
	_, err = c.AppendEvent(ctx, relata.EventAppendRequest{ObjectType: ty, IdempotencyKey: uniq("ev"), Target: relata.EventTarget{ObjectType: ty, Key: "_pk", Value: "bad-1"}, Attributes: map[string]any{"a": 1}, Purpose: "investigation"})
	wantProblem(t, err, 400, "")

	// Audit: the chain verifies and recorded the activity.
	ac := relata.NewAuditClient(c)
	cnt, err := ac.Count(ctx)
	must(t, err, "audit count")
	if cnt["chain_valid"] != true || str(cnt["count"]) == "0" {
		t.Fatalf("audit count: %v", cnt)
	}
	lg, err := ac.Log(ctx, &relata.AuditLogOptions{Limit: 5})
	must(t, err, "audit log")
	if es, _ := lg["entries"].([]any); len(es) == 0 {
		t.Fatalf("audit log empty: %v", lg)
	}
	top, err := c.AuditCount(ctx)
	must(t, err, "typed audit count")
	if top.IsTampered() || top.Entries == 0 {
		t.Fatalf("typed audit: %+v", top)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
