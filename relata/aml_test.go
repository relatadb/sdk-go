package relata

import "testing"

func TestDecodeSanctionsHits(t *testing.T) {
	rows := []map[string]any{
		{"list_name": "OFAC", "entry_id": "e1", "matched_field": "name", "match_score": 0.95},
		{"list_name": "EU", "entry_id": "e2", "matched_field": "aliases", "match_score": "0.8"},
	}
	hits := DecodeSanctionsHits(rows)
	if len(hits) != 2 {
		t.Fatalf("len = %d, want 2", len(hits))
	}
	if hits[0].ListName != "OFAC" {
		t.Errorf("ListName = %q", hits[0].ListName)
	}
}

func TestDecodeSanctionsSkipsMissing(t *testing.T) {
	if got := DecodeSanctionsHits([]map[string]any{{"list_name": "OFAC"}}); len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}

func TestDecodeBeneficialOwners(t *testing.T) {
	rows := []map[string]any{
		{"depth": 1, "parent_id": "p", "child_id": "c", "link_type": "owns", "ownership_pct": 51.0},
	}
	bos := DecodeBeneficialOwners(rows)
	if len(bos) != 1 || bos[0].OwnershipPct == nil || *bos[0].OwnershipPct != 51.0 {
		t.Fatalf("unexpected: %+v", bos)
	}
}

func TestDecodeBeneficialOwnersNullPct(t *testing.T) {
	rows := []map[string]any{
		{"depth": 1, "parent_id": "p", "child_id": "c", "link_type": "owns", "ownership_pct": nil},
	}
	bos := DecodeBeneficialOwners(rows)
	if bos[0].OwnershipPct != nil {
		t.Errorf("expected nil OwnershipPct")
	}
}

func TestDecodeCryptoTrace(t *testing.T) {
	rows := []map[string]any{
		{"hop": 1, "from_wallet": "w1", "to_wallet": "w2", "amount": 0.5, "asset": "BTC", "chain": "bitcoin"},
	}
	if got := DecodeCryptoTrace(rows); got[0].ToWallet != "w2" {
		t.Errorf("ToWallet = %q", got[0].ToWallet)
	}
}

func TestDecodeWireHops(t *testing.T) {
	rows := []map[string]any{
		{"step": 0, "from_bank": "b1", "to_bank": "b2", "amount": 100, "currency": "USD", "reference": "r1"},
	}
	if got := DecodeWireHops(rows); got[0].Currency != "USD" {
		t.Errorf("Currency = %q", got[0].Currency)
	}
}

func TestDecodeHawalaPairs(t *testing.T) {
	rows := []map[string]any{
		{"sender": "s", "receiver": "r", "amount": 200, "sender_country": "AE", "receiver_country": "IN", "ts": "2024-01-01"},
	}
	if got := DecodeHawalaPairs(rows); got[0].ReceiverCountry != "IN" {
		t.Errorf("ReceiverCountry = %q", got[0].ReceiverCountry)
	}
}

func TestDecodeResolvedIdentities(t *testing.T) {
	rows := []map[string]any{
		{"_type": "Canonical", "_id": "x", "identity_value": "x", "source": "relata-detect", "observed_at": 123, "confidence": 1.0},
	}
	ids := DecodeResolvedIdentities(rows)
	if ids[0].ID != "x" || ids[0].Kind != "Canonical" {
		t.Errorf("unexpected: %+v", ids[0])
	}
}

func TestDecodePathHops(t *testing.T) {
	rows := []map[string]any{
		{"position": 1, "node": "a", "cost": 0.0},
		{"position": 2, "node": "b"},
	}
	hops := DecodePathHops(rows)
	if len(hops) != 2 {
		t.Fatalf("len = %d", len(hops))
	}
	if hops[0].Cost == nil || *hops[0].Cost != 0.0 {
		t.Errorf("hops[0].Cost unexpected")
	}
	if hops[1].Cost != nil {
		t.Errorf("hops[1].Cost should be nil")
	}
}
