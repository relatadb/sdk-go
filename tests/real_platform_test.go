package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// Operational surface: health/status/stats/version/ready, system jobs and
// config, token self-service lifecycle, tenant identity, freshness barrier,
// evidence listing, observe stream, notification rules CRUD.
func TestRealPlatformOps(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)

	h, err := c.Health(ctx)
	must(t, err, "health")
	if !h.IsHealthy() {
		t.Fatalf("server not healthy: %+v", h)
	}
	v, err := c.Version(ctx)
	must(t, err, "version")
	if v == nil || str(*v) == "" {
		t.Fatalf("version: %+v", v)
	}
	if _, err := c.Status(ctx); err != nil {
		t.Errorf("status: %v", err)
	}
	ty := uniq("RpOps")
	ingestRowsWait(t, c, ty, []map[string]any{{"_pk": "s1", "n": 1}})
	st, err := c.Stats(ctx)
	must(t, err, "stats")
	t.Logf("stats: %+v", *st)
	if rr, err := c.Ready(ctx); err != nil {
		t.Errorf("ready: %v", err)
	} else if !rr.IsReady {
		t.Errorf("authenticated /health/ready answered 200 but IsReady is false: %+v", *rr)
	}
	if relata.New(realHost(), &relata.ClientOptions{BearerToken: "x"}).Close() != nil {
		t.Errorf("close")
	}
	if s := (&relata.RelataError{Message: "boom", StatusCode: 418}).Error(); !strings.Contains(s, "boom") {
		t.Errorf("error string: %q", s)
	}
	if got := str(c); strings.Contains(got, os_token()) && os_token() != "" {
		t.Errorf("client formatting must not leak the bearer token: %q", got)
	}

	sys := relata.NewSystemClient(c)
	if _, err := sys.JobsStatus(ctx); err != nil {
		t.Errorf("jobs status: %v", err)
	}
	if _, err := sys.JobStatus(ctx, "no-such-job-"+uniq("j")); err == nil {
		t.Errorf("unknown job must be an error")
	} else {
		wantProblemAny(t, err)
	}
	if cfg, err := sys.SystemConfig(ctx); err != nil || len(cfg) == 0 {
		t.Errorf("system config: %v %v", cfg, err)
	}
	if sp, err := sys.SystemSpecs(ctx); err != nil || len(sp) == 0 {
		t.Errorf("system specs: %v %v", sp, err)
	}
	if _, err := sys.MCPSessions(ctx, &relata.MCPSessionsOptions{Limit: 5}); err != nil {
		t.Logf("mcp sessions: %v", err)
	}
	// Notification rules CRUD.
	nr, err := sys.CreateNotificationRule(ctx, map[string]any{"min_severity": "high", "alert_type": "detection", "webhook_url": "https://example.invalid/hook", "webhook_headers": map[string]string{"X-Route": "soc"}, "active": true})
	if err != nil {
		t.Errorf("create notification rule: %v", err)
	} else {
		id := str(nr["rule_id"])
		if id == "" {
			t.Fatalf("notification rule has no id: %v", nr)
		}
		if _, err := sys.UpdateNotificationRule(ctx, id, map[string]any{"active": false}); err != nil {
			t.Errorf("update notification rule: %v", err)
		}
		rules, err := sys.NotificationRules(ctx)
		must(t, err, "list notification rules")
		found := false
		for _, r := range rules {
			if str(r["rule_id"]) == id {
				found = true
			}
		}
		if !found {
			t.Errorf("created notification rule %s not listed", id)
		}
		if _, err := sys.DeleteNotificationRule(ctx, id); err != nil {
			t.Errorf("delete notification rule: %v", err)
		}
	}

	// Token self-service: list, audit, rotate (new secret authenticates, old does not), revoke.
	adm := adminClient(t)
	minted := mintToken(t, "tenant-admin", "default")
	mc := relata.New(realHost(), &relata.ClientOptions{BearerToken: minted, DefaultPurpose: "investigation", Timeout: 30 * time.Second})
	tk := relata.NewTokenClient(mc)
	lst, err := tk.ListSelf(ctx)
	must(t, err, "list self")
	t.Logf("tokens: %v", lst)
	if _, err := tk.SelfAudit(ctx); err != nil {
		t.Errorf("self audit: %v", err)
	}
	rot, err := tk.RotateSelf(ctx)
	if err != nil {
		t.Logf("rotate self: %v", err)
	} else if ns := str(rot["secret"]); ns != "" {
		nc := relata.New(realHost(), &relata.ClientOptions{BearerToken: ns, DefaultPurpose: "investigation"})
		if _, err := relata.NewTokenClient(nc).ListSelf(ctx); err != nil {
			t.Errorf("rotated token must authenticate: %v", err)
		}
		_, err = relata.NewTokenClient(mc).ListSelf(ctx)
		wantProblem(t, err, 401, "")
	}
	_ = adm

	if me, err := relata.NewTenantAdminClient(c).Me(ctx); err != nil {
		t.Logf("tenant me: %v", err)
	} else if len(me) == 0 {
		t.Errorf("tenant me empty")
	}

	fr := relata.NewFreshnessClient(c)
	if fs, err := fr.Status(ctx); err != nil {
		t.Errorf("freshness status: %v", err)
	} else {
		t.Logf("freshness: %v", fs)
	}
	rc, err := c.IngestDocument(ctx, []map[string]any{{"_type": ty, "_pk": "fb1", "n": 2}}, "ingest")
	must(t, err, "ingest for barrier")
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		ts, _ := c.IngestDocumentStatus(ctx, rc.TaskID)
		if ts != nil && ts.Status == "complete" {
			if tok := freshnessToken(ts); tok != "" {
				bar, err := fr.Barrier(ctx, tok, &relata.FreshnessOptions{WaitMS: 2000})
				if err != nil {
					t.Errorf("barrier: %v", err)
				} else {
					t.Logf("barrier: %v", bar)
				}
			}
			break
		}
	}
	ev := relata.NewEvidenceClient(c)
	if l, err := ev.List(ctx, 5); err != nil {
		t.Logf("evidence list: %v", err)
	} else {
		t.Logf("evidence: %v", l)
	}
	_, err = ev.Get(ctx, "no-such-evidence-"+uniq("e"))
	wantProblemAny(t, err)
	_, err = ev.Replay(ctx, "no-such-evidence-"+uniq("e"), false)
	wantProblemAny(t, err)

	octx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ch, err := c.ObserveStream(octx)
	if err != nil {
		t.Logf("observe stream: %v", err)
	} else {
		select {
		case e, ok := <-ch:
			t.Logf("observe event ok=%v %+v", ok, e)
		case <-octx.Done():
		}
	}
}

func freshnessToken(ts *relata.IngestDocumentTaskStatus) string {
	raw, ok := ts.Extensions["freshness"]
	if !ok {
		return ""
	}
	s := string(raw)
	i := strings.Index(s, `"token":"`)
	if i < 0 {
		return ""
	}
	s = s[i+len(`"token":"`):]
	return s[:strings.Index(s, `"`)]
}
