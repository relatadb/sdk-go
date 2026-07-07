// Command audit demonstrates audit-chain verification and compliance reporting
// via the Relata Go SDK.
//
// Scenario: A compliance officer runs daily checks on a Relata deployment to
// confirm the hash-chained audit log remains intact (tamper-evident) and to
// produce a summary for the agency's data-governance board.
//
// Workflow:
//  1. Verify the hash chain is intact via GET /audit/count.
//  2. Query the audit log for recent query activity (SELECT from AuditEntry).
//  3. Flag any queries that lacked a valid purpose or came from unrecognised
//     principals.
//  4. Check cluster node health for a multi-node deployment.
//  5. Exit with a non-zero status if any anomaly is detected — designed to
//     run as a cron-scheduled health gate.
//
// Usage:
//
//	go run ./examples/audit \
//	    -url   http://localhost:8080  \
//	    -token $RELATA_BEARER_TOKEN   \
//	    -since 2025-01-01
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/OpenWorkBench-Co/RelataDB/sdks/go/relata"
)

func main() {
	url := flag.String("url", "http://localhost:8080", "Relata server base URL")
	token := flag.String("token", "", "Bearer token")
	since := flag.String("since", "2025-01-01", "Start date for audit window (YYYY-MM-DD)")
	flag.Parse()

	// Use "audit" as the purpose — this is a registered token for compliance
	// queries and is distinct from "investigation" or "analysis".
	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "audit",
		Timeout:        60 * time.Second,
	})

	ctx := context.Background()
	exitCode := 0 // accumulate; non-zero on any anomaly

	// ── Step 1: Hash-chain integrity check ────────────────────────────────────
	fmt.Println("=== Step 1: Audit chain integrity ===")

	auditResp, err := client.AuditCount(ctx)
	if err != nil {
		if errors.Is(err, relata.ErrChainCorrupted) {
			// AuditCount still returns the struct alongside the error so we can
			// report the entry count before raising the alarm.
			fmt.Printf("CRITICAL: audit chain integrity FAILED — entries=%d\n",
				auditResp.Entries)
			fmt.Println("  This indicates tampering or storage corruption.")
			fmt.Println("  Escalate immediately per incident response playbook.")
			exitCode = 2
		} else {
			log.Fatalf("audit count failed: %v", err)
		}
	} else {
		fmt.Printf("Chain intact: entries=%d  chain_valid=%v\n",
			auditResp.Entries, auditResp.ChainValid)
	}

	// ── Step 2: Recent query activity ─────────────────────────────────────────
	// The AuditEntry type in the ontology captures every query: principal,
	// purpose, query_id, cost_units, masked_columns, and access_scope_ref.
	fmt.Printf("\n=== Step 2: Audit entries since %s ===\n", *since)

	activityResult, err := relata.NewQuery("SELECT * FROM AuditEntry").
		Purpose("audit").
		Where(fmt.Sprintf("recorded_at >= '%s'", *since)).
		Limit(200).
		Execute(ctx, client)
	if err != nil {
		log.Fatalf("audit activity query failed: %v", err)
	}
	fmt.Printf("Audit entries: %d  elapsed: %d ms\n\n",
		activityResult.RowCount, activityResult.ElapsedMs)

	// ── Step 3: Flag anomalous queries ────────────────────────────────────────
	// A purposeless query should never reach the engine (rejected at the wire),
	// but a missing purpose field in historical entries signals a misconfigured
	// client or a period before purpose enforcement was enabled.
	fmt.Println("=== Step 3: Anomaly scan ===")

	var (
		purposelessCount int
		highCostCount    int
		highCostThresh   = 5000.0 // cost_units above this warrant review
	)

	activityResult.Each(func(row map[string]any) {
		purpose, _ := row["purpose"].(string)
		costUnits, _ := row["cost_units"].(float64)

		if purpose == "" {
			purposelessCount++
			fmt.Printf("  [WARN] purposeless entry  query_id=%v  principal=%v\n",
				row["query_id"], row["principal"])
		}
		if costUnits > highCostThresh {
			highCostCount++
			fmt.Printf("  [WARN] high-cost query  query_id=%v  cost_units=%.0f  purpose=%v\n",
				row["query_id"], costUnits, purpose)
		}
	})

	if purposelessCount == 0 && highCostCount == 0 {
		fmt.Println("  No anomalies detected.")
	} else {
		fmt.Printf("  Purposeless: %d  High-cost: %d\n", purposelessCount, highCostCount)
		if purposelessCount > 0 {
			exitCode = 1
		}
	}

	// ── Step 4: Cluster node health sweep ────────────────────────────────────
	fmt.Printf("\n=== Step 4: Cluster node health ===\n")

	nodes, err := client.ClusterNodes(ctx)
	if err != nil {
		fmt.Printf("  [WARN] cluster nodes unavailable: %v\n", err)
		// Non-fatal for lite/server profiles that don't expose cluster info.
	} else {
		fmt.Printf("  Nodes: %d\n", len(nodes))
		for _, node := range nodes {
			fmt.Printf("    node_id=%-20s  role=%-15s  url=%s\n",
				node.NodeID, node.Role, node.URL)
		}
	}

	// Also verify health + status of the local node.
	health, err := client.Health(ctx)
	if err != nil {
		fmt.Printf("  [WARN] health check failed: %v\n", err)
		exitCode = 1
	} else {
		fmt.Printf("  Local node: %s  profile=%s  status=%s\n",
			health.NodeID, health.Profile, health.Status)
		if health.Status != "ok" {
			fmt.Printf("  [WARN] node status is not 'ok': %s\n", health.Status)
			exitCode = 1
		}
	}

	status, err := client.Status(ctx)
	if err != nil {
		fmt.Printf("  [WARN] status failed: %v\n", err)
	} else {
		fmt.Printf("  Role=%s  quota=%d\n", status.Role, status.QueryQuota)
	}

	// ── Step 5: Compliance summary ────────────────────────────────────────────
	fmt.Printf("\n=== Compliance summary ===\n")
	fmt.Printf("Window       : since %s\n", *since)
	fmt.Printf("Audit entries: %d\n", activityResult.RowCount)
	fmt.Printf("Chain valid  : %v\n", auditResp != nil && auditResp.ChainValid)
	fmt.Printf("Anomalies    : purposeless=%d  high-cost=%d\n",
		purposelessCount, highCostCount)

	if exitCode == 0 {
		fmt.Println("Result       : PASS — no compliance violations detected")
	} else {
		fmt.Printf("Result       : FAIL — exit code %d\n", exitCode)
	}

	os.Exit(exitCode)
}
