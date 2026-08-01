// Command investigation demonstrates a full end-to-end investigation workflow
// using the Relata Go SDK.
//
// Scenario: A financial intelligence unit (FIU) analyst is investigating a
// suspected money-laundering network. The workflow:
//  1. Look up a phone number in the IdentityIndex to resolve the subject.
//  2. Fetch all financial transactions linked to the resolved entity.
//  3. Retrieve them with full PROV-O provenance metadata.
//  4. Run a bi-temporal AS OF query to see the state at the date of the
//     original suspicious activity report (SAR).
//  5. Pull call-detail records (CDR) for the same period.
//
// Every query declares purpose="investigation" as required by SPECS §5.22.4.
//
// Usage:
//
//	go run ./examples/investigation \
//	    -url  http://localhost:9090  \
//	    -token $RELATA_BEARER_TOKEN
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token")
	phone := flag.String("phone", "+919876543210", "Subject phone number (MSISDN)")
	sarDate := flag.String("sar-date", "2024-11-01", "Date of suspicious activity (AS OF)")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "investigation",
		Timeout:        60 * time.Second,
	})

	ctx := context.Background()

	// ── Step 1: Resolve identity ──────────────────────────────────────────────
	// LOOKUP_IDENTITY resolves any canonical identifier through the global
	// IdentityIndex and returns the linked entity (Person, Organisation, …).
	fmt.Printf("=== Step 1: Resolve %s via IdentityIndex ===\n", *phone)

	idResult, err := relata.LookupIdentity(*phone).
		Purpose("investigation").
		Execute(ctx, client)
	if err != nil {
		log.Fatalf("identity lookup failed: %v", err)
	}

	if idResult.RowCount == 0 {
		log.Fatalf("no entity found for %s", *phone)
	}

	subjectRow := idResult.Rows[0]
	entityID, _ := subjectRow["entity_id"].(string)
	entityName, _ := subjectRow["display_name"].(string)
	fmt.Printf("Resolved → entity_id=%s  name=%q\n\n", entityID, entityName)

	// ── Step 2: Financial transactions ───────────────────────────────────────
	// Use the QueryBuilder for type-safe, composable SQL construction.
	fmt.Println("=== Step 2: Financial transactions (current view) ===")

	txResult, err := relata.NewQuery("SELECT * FROM FinancialTransaction").
		Purpose("investigation").
		Where(fmt.Sprintf("subject_entity_id = '%s'", entityID)).
		Where("amount_usd > 10000").
		Limit(50).
		Execute(ctx, client)
	if err != nil {
		log.Fatalf("transaction query failed: %v", err)
	}
	fmt.Printf("Transactions ≥ $10k: %d rows in %d ms\n\n",
		txResult.RowCount, txResult.ElapsedMs)

	txResult.Each(func(row map[string]any) {
		fmt.Printf("  txn_id=%-20v  amount_usd=%v  counterparty=%v\n",
			row["txn_id"], row["amount_usd"], row["counterparty_entity_id"])
	})

	// ── Step 3: Same query with provenance metadata ───────────────────────────
	// WithProvenance() appends WITH PROVENANCE to the SQL, causing the engine
	// to return _source, _collector, _method, _confidence, _observed_at, and
	// _recorded_at columns alongside every data column.
	fmt.Printf("\n=== Step 3: Same query WITH PROVENANCE ===\n")

	provResult, err := relata.NewQuery("SELECT * FROM FinancialTransaction").
		Purpose("investigation").
		Where(fmt.Sprintf("subject_entity_id = '%s'", entityID)).
		Where("amount_usd > 10000").
		WithProvenance().
		Limit(10).
		Execute(ctx, client)
	if err != nil {
		log.Fatalf("provenance query failed: %v", err)
	}

	provResult.Each(func(row map[string]any) {
		fmt.Printf("  txn_id=%-20v  _source=%v  _confidence=%v\n",
			row["txn_id"], row["_source"], row["_confidence"])
	})

	// ── Step 4: Bi-temporal point-in-time view at SAR date ───────────────────
	// AS OF requests the bi-temporal snapshot at a specific valid_from/valid_to
	// boundary. This is how analysts recreate "what did we know on date X" —
	// essential for court-admissible reconstructions (SPECS §5.5).
	fmt.Printf("\n=== Step 4: AS OF '%s' (SAR date snapshot) ===\n", *sarDate)

	asOfResult, err := relata.NewQuery("SELECT * FROM FinancialTransaction").
		Purpose("investigation").
		Where(fmt.Sprintf("subject_entity_id = '%s'", entityID)).
		AsOf(*sarDate).
		Limit(50).
		Execute(ctx, client)
	if err != nil {
		log.Fatalf("as-of query failed: %v", err)
	}
	fmt.Printf("Transactions visible at %s: %d rows in %d ms\n\n",
		*sarDate, asOfResult.RowCount, asOfResult.ElapsedMs)

	// ── Step 5: Call-detail records for the same subject ─────────────────────
	fmt.Println("=== Step 5: Call-detail records ===")

	cdrResult, err := relata.NewQuery("SELECT * FROM PhoneCall").
		Purpose("investigation").
		Where(fmt.Sprintf("(caller_msisdn = '%s' OR callee_msisdn = '%s')", *phone, *phone)).
		Where("duration_s > 0").
		AsOf(*sarDate).
		Limit(100).
		Execute(ctx, client)
	if err != nil {
		log.Fatalf("CDR query failed: %v", err)
	}
	fmt.Printf("CDR rows at %s: %d in %d ms\n", *sarDate, cdrResult.RowCount, cdrResult.ElapsedMs)

	cdrResult.Each(func(row map[string]any) {
		fmt.Printf("  call_id=%-20v  caller=%v  callee=%v  duration_s=%v\n",
			row["call_id"], row["caller_msisdn"], row["callee_msisdn"], row["duration_s"])
	})

	// ── Summary ───────────────────────────────────────────────────────────────
	fmt.Printf("\n=== Investigation summary ===\n")
	fmt.Printf("Subject  : %s (%s)\n", entityName, entityID)
	fmt.Printf("Txns≥10k : %d (current)  /  %d (at SAR date)\n",
		txResult.RowCount, asOfResult.RowCount)
	fmt.Printf("CDR rows : %d\n", cdrResult.RowCount)

	// Demonstrate error handling: purposeless query is rejected client-side.
	_, err = client.Query(ctx, "SELECT 1") // no DefaultPurpose in this call
	if errors.Is(err, relata.ErrPurposeRequired) {
		fmt.Println("\n[expected] purposeless query correctly rejected:", err)
	}
}
