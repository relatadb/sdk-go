package tests

import (
	"strings"
	"testing"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// Retention journey: policy, legal hold on a case, WORM retention, then
// subject access (DSAR) and certified erasure with a verifiable receipt.
func TestRealJourneyRetentionAndErasure(t *testing.T) {
	admin := roleClient(t, "platform-admin")
	c := realClient(t)
	ctx := ctxT(t)
	g := relata.NewGovernanceClient(admin)
	// Role separation: erasure and legal holds are refused to admin roles and
	// done by the ordinary data token.
	gd := relata.NewGovernanceClient(c)
	ty := uniq("RgRec")
	email := strings.ToLower(ty) + "@example.com"
	var err0 error
	_, err0 = g.PlaceLegalHold(ctx, uniq("sep"), ty, nil)
	wantProblem(t, err0, 403, "role-separation")
	ingestRowsWait(t, c, ty, []map[string]any{{"_pk": "g1", "id": "g1", "email": email, "note": "subject data"}})

	if _, err := g.SetRetentionPolicy(ctx, ty, 365, "delete"); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	pols, err := g.ListRetentionPolicies(ctx)
	must(t, err, "list retention")
	if !strings.Contains(str(pols), ty) {
		t.Fatalf("retention policy for %s not listed: %v", ty, pols)
	}
	cid := uniq("case")
	if _, err := gd.PlaceLegalHold(ctx, cid, ty, &relata.PlaceLegalHoldOptions{Field: "email", Value: email}); err != nil {
		t.Fatalf("place hold: %v", err)
	}
	holds, err := g.ListLegalHolds(ctx)
	must(t, err, "list holds")
	if !strings.Contains(str(holds), cid) {
		t.Fatalf("hold %s not listed: %v", cid, holds)
	}
	if _, err := gd.LiftLegalHold(ctx, cid); err != nil {
		t.Errorf("lift hold: %v", err)
	}
	wty := uniq("RgWorm")
	ingestRowsWait(t, c, wty, []map[string]any{{"_pk": "w1", "v": 1}})
	if _, err := relata.NewGovernanceClient(roleClient(t, "schema-admin")).SetWormPolicy(ctx, wty, 3600); err != nil {
		t.Fatalf("set worm: %v", err)
	}
	worm, err := g.ListWormPolicies(ctx)
	must(t, err, "list worm")
	if !strings.Contains(str(worm), wty) {
		t.Fatalf("worm policy for %s not listed: %v", wty, worm)
	}

	dsar, err := gd.SubmitDSAR(ctx, email, "subject access request", nil)
	must(t, err, "dsar")
	if !strings.Contains(str(dsar), ty) && !strings.Contains(str(dsar), "g1") {
		t.Logf("dsar did not name the record type: %v", dsar)
	}

	er, err := gd.Erase(ctx, email, &relata.EraseOptions{Reason: "gdpr art 17"})
	if err != nil {
		// Without the durable deletion-intent authority the server must refuse
		// typed and claim no destruction.
		t.Fatalf("erase: %v", err)
	}
	t.Logf("erase: %v", er)
	op := str(er["operation_id"])
	if op != "" && op != "<nil>" {
		if st, err := gd.DeletionStatus(ctx, op); err != nil {
			t.Errorf("deletion status: %v", err)
		} else {
			t.Logf("deletion status: %v", st)
		}
	}
	// The open edition issues no signed Art. 17 receipt (no per-subject key is
	// destroyed), so the response must say so, and a forged receipt must not verify.
	if er["global_physical_erasure_verified"] != false || er["failed_count"] != float64(0) {
		t.Errorf("erase must not over-claim: %v", er)
	}
	forged, err := gd.VerifyReceipt(ctx, map[string]any{"receipt_id": "rcpt-forged", "subject": email, "erased_at": 1, "rows_shredded": 1, "receipt_signature": "00"})
	must(t, err, "verify forged receipt")
	if forged["verified"] != false {
		t.Errorf("a forged receipt must not verify: %v", forged)
	}
	_, err = gd.VerifyReceipt(ctx, map[string]any{"receipt_id": "x"})
	wantProblem(t, err, 400, "")

	// SQL-door erasure by the ordinary client states plainly it is not certified.
	sty := uniq("RgSql")
	semail := strings.ToLower(sty) + "@example.com"
	ingestRowsWait(t, c, sty, []map[string]any{{"_pk": "g1", "id": "g1", "email": semail}})
	sq, err := c.EraseSubject(ctx, "investigation", semail, "")
	must(t, err, "sql erase")
	if sq["logical_erasure_complete"] != true || sq["certified"] != false {
		t.Errorf("sql erase result: %v", sq)
	}
	if rows := sel(t, c, "SELECT _pk FROM "+sty); len(rows) != 0 {
		t.Errorf("erased subject's rows must be suppressed from reads: %v", rows)
	}
}
