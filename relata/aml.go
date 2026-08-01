// Typed decoders for AML / financial-intelligence result shapes (#2255).
//
// The server's AML operators (SANCTIONS_SCREEN, BENEFICIAL_OWNERSHIP_CHAIN,
// CRYPTO_TRACE, WIRE_RECONSTRUCTION, HAWALA_TRACE, RESOLVE_IDENTITY, and the
// shortest-path / Dijkstra path operators) return generic query-result rows
// (map[string]any of column → JSON value). This file declares typed structs
// for each result shape and provides a decoder that maps the generic rows into
// those structs, so callers can work with strongly-typed objects instead of
// untyped maps.
//
// Field names mirror the exact column names emitted by the server operators
// (see crates/relata-query/src/finint_operators.rs and investigation_ops.rs).
//
// Scope: only the AML / financial-intelligence result shapes are typed here.
// All other packs (police, evidence, supply-chain, health, cyber, …) continue
// to return generic map[string]any results — typing them is the remaining
// scope of #2255.

package relata

import (
	"fmt"
	"strconv"
)

func parseFloatLoose(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
func parseIntLoose(s string) (int64, error)     { return strconv.ParseInt(s, 10, 64) }

// getStr extracts a string field; stringifies numbers, treats missing/nil as
// absent.
func getStr(row map[string]any, key string) (string, bool) {
	v, ok := row[key]
	if !ok || v == nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	default:
		return fmt.Sprint(x), true
	}
}

// getFloat extracts a float64 field from a JSON number or numeric string.
func getFloat(row map[string]any, key string) (float64, bool) {
	v, ok := row[key]
	if !ok || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := parseFloatLoose(x)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

func getInt(row map[string]any, key string) (int64, bool) {
	v, ok := row[key]
	if !ok || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case string:
		n, err := parseIntLoose(x)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// ── SanctionsHit ─────────────────────────────────────────────────────────────

// SanctionsHit is a single sanctions-list match from SANCTIONS_SCREEN.
type SanctionsHit struct {
	ListName     string  // sanctions list (e.g. "OFAC", "EU")
	EntryID      string  // server-side entry identifier
	MatchedField string  // field on the entry that matched
	MatchScore   float64 // fuzzy match score in [0.0, 1.0]
}

// DecodeSanctionsHits decodes generic query rows into typed SanctionsHit
// values. Rows missing required fields are skipped.
func DecodeSanctionsHits(rows []map[string]any) []SanctionsHit {
	out := make([]SanctionsHit, 0, len(rows))
	for _, r := range rows {
		listName, ok1 := getStr(r, "list_name")
		entryID, ok2 := getStr(r, "entry_id")
		matched, ok3 := getStr(r, "matched_field")
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		score, _ := getFloat(r, "match_score")
		out = append(out, SanctionsHit{
			ListName:     listName,
			EntryID:      entryID,
			MatchedField: matched,
			MatchScore:   score,
		})
	}
	return out
}

// ── BeneficialOwner ──────────────────────────────────────────────────────────

// BeneficialOwner is one hop in a beneficial-ownership chain.
type BeneficialOwner struct {
	Depth        int      // BFS depth from the seed entity (1-based)
	ParentID     string   // owning entity id
	ChildID      string   // owned entity id
	LinkType     string   // edge type ("owns", "subsidiary_of", …)
	OwnershipPct *float64 // 0–100 when known
}

// DecodeBeneficialOwners decodes generic query rows into typed BeneficialOwner hops.
func DecodeBeneficialOwners(rows []map[string]any) []BeneficialOwner {
	out := make([]BeneficialOwner, 0, len(rows))
	for _, r := range rows {
		depth, ok1 := getInt(r, "depth")
		parentID, ok2 := getStr(r, "parent_id")
		childID, ok3 := getStr(r, "child_id")
		linkType, ok4 := getStr(r, "link_type")
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue
		}
		bo := BeneficialOwner{
			Depth:    int(depth),
			ParentID: parentID,
			ChildID:  childID,
			LinkType: linkType,
		}
		if pct, ok := getFloat(r, "ownership_pct"); ok {
			bo.OwnershipPct = &pct
		}
		out = append(out, bo)
	}
	return out
}

// ── CryptoTraceHop ───────────────────────────────────────────────────────────

// CryptoTraceHop is one hop in a cryptocurrency fund-flow trace.
type CryptoTraceHop struct {
	Hop        int     // 1-based hop index (-1 = terminal summary row)
	FromWallet string  // source wallet address
	ToWallet   string  // destination wallet address
	Amount     float64 // transfer amount in the asset's native units
	Asset      string  // asset ticker ("BTC", "ETH", …)
	Chain      string  // blockchain ("bitcoin", "ethereum", …)
}

// DecodeCryptoTrace decodes generic query rows into typed CryptoTraceHop values.
func DecodeCryptoTrace(rows []map[string]any) []CryptoTraceHop {
	out := make([]CryptoTraceHop, 0, len(rows))
	for _, r := range rows {
		hop, ok := getInt(r, "hop")
		if !ok {
			continue
		}
		fromWallet, _ := getStr(r, "from_wallet")
		toWallet, _ := getStr(r, "to_wallet")
		amount, _ := getFloat(r, "amount")
		asset, _ := getStr(r, "asset")
		chain, _ := getStr(r, "chain")
		out = append(out, CryptoTraceHop{
			Hop: int(hop), FromWallet: fromWallet, ToWallet: toWallet,
			Amount: amount, Asset: asset, Chain: chain,
		})
	}
	return out
}

// ── WireHop ──────────────────────────────────────────────────────────────────

// WireHop is one leg of a reconstructed wire-transfer chain.
type WireHop struct {
	Step      int     // 0-based step index (-1 = chain summary row)
	FromBank  string  // sending bank / account
	ToBank    string  // receiving bank / account
	Amount    float64 // transfer amount
	Currency  string  // ISO 4217 currency code ("USD", …)
	Reference string  // wire reference / reference text
}

// DecodeWireHops decodes generic query rows into typed WireHop values.
func DecodeWireHops(rows []map[string]any) []WireHop {
	out := make([]WireHop, 0, len(rows))
	for _, r := range rows {
		step, ok := getInt(r, "step")
		if !ok {
			continue
		}
		fromBank, _ := getStr(r, "from_bank")
		toBank, _ := getStr(r, "to_bank")
		amount, _ := getFloat(r, "amount")
		currency, _ := getStr(r, "currency")
		reference, _ := getStr(r, "reference")
		out = append(out, WireHop{
			Step: int(step), FromBank: fromBank, ToBank: toBank,
			Amount: amount, Currency: currency, Reference: reference,
		})
	}
	return out
}

// ── HawalaPair ───────────────────────────────────────────────────────────────

// HawalaPair is a matched informal-value-transfer pair from HAWALA_TRACE.
type HawalaPair struct {
	Sender          string  // sender identifier
	Receiver        string  // receiver identifier
	Amount          float64 // transferred amount
	SenderCountry   string  // ISO 3166 sender country code
	ReceiverCountry string  // ISO 3166 receiver country code
	Ts              string  // transaction timestamp (textual)
}

// DecodeHawalaPairs decodes generic query rows into typed HawalaPair values.
func DecodeHawalaPairs(rows []map[string]any) []HawalaPair {
	out := make([]HawalaPair, 0, len(rows))
	for _, r := range rows {
		sender, ok := getStr(r, "sender")
		if !ok {
			continue
		}
		receiver, _ := getStr(r, "receiver")
		amount, _ := getFloat(r, "amount")
		sCountry, _ := getStr(r, "sender_country")
		rCountry, _ := getStr(r, "receiver_country")
		ts, _ := getStr(r, "ts")
		out = append(out, HawalaPair{
			Sender: sender, Receiver: receiver, Amount: amount,
			SenderCountry: sCountry, ReceiverCountry: rCountry, Ts: ts,
		})
	}
	return out
}

// ── ResolvedIdentity ─────────────────────────────────────────────────────────

// ResolvedIdentity is a resolved identity member from RESOLVE_IDENTITY.
type ResolvedIdentity struct {
	ID            string  // canonical identifier
	Kind          string  // row kind ("Canonical", "Cluster", …)
	IdentityValue string  // raw identity value
	Source        string  // detection source ("relata-detect", …)
	ObservedAt    int64   // observation timestamp (i64 ns UTC)
	Confidence    float64 // link confidence in [0.0, 1.0]
}

// DecodeResolvedIdentities decodes generic query rows into typed ResolvedIdentity values.
func DecodeResolvedIdentities(rows []map[string]any) []ResolvedIdentity {
	out := make([]ResolvedIdentity, 0, len(rows))
	for _, r := range rows {
		id, ok := getStr(r, "_id")
		if !ok {
			continue
		}
		kind, _ := getStr(r, "_type")
		identityValue, _ := getStr(r, "identity_value")
		source, _ := getStr(r, "source")
		observedAt, _ := getInt(r, "observed_at")
		confidence, _ := getFloat(r, "confidence")
		out = append(out, ResolvedIdentity{
			ID: id, Kind: kind, IdentityValue: identityValue,
			Source: source, ObservedAt: observedAt, Confidence: confidence,
		})
	}
	return out
}

// ── PathHop ──────────────────────────────────────────────────────────────────

// PathHop is one node on a graph path from SHORTEST_PATH / DIJKSTRA.
type PathHop struct {
	Position int      // 1-based position along the path
	Node     string   // node identifier
	Cost     *float64 // accumulated cost/distance when emitted, else nil
}

// DecodePathHops decodes generic query rows into typed PathHop values.
func DecodePathHops(rows []map[string]any) []PathHop {
	out := make([]PathHop, 0, len(rows))
	for _, r := range rows {
		position, ok1 := getInt(r, "position")
		node, ok2 := getStr(r, "node")
		if !ok1 || !ok2 {
			continue
		}
		hop := PathHop{Position: int(position), Node: node}
		if cost, ok := getFloat(r, "cost"); ok {
			hop.Cost = &cost
		}
		out = append(out, hop)
	}
	return out
}
