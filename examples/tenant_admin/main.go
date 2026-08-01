// Command tenant_admin demonstrates the multi-tenant lifecycle (#967):
// whoami + usage, CRUD on tenants, tier management, suspend / reactivate,
// per-tenant quota, sharing agreements, platform-wide usage / license.
//
// Requires an admin-scoped bearer token. Run against a server started with
// RELATA_TENANCY_MODE=multi and an admin token.
//
// Usage:
//
//	go run ./examples/tenant_admin -url http://localhost:9090 -token $ADMIN_TOKEN
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "tenant-" + hex.EncodeToString(b)
}

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token (admin)")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "admin",
		Timeout:        30 * time.Second,
	})
	admin := relata.NewTenantAdminClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// ── 1. me() + usage() ─────────────────────────────────────────────────
	fmt.Println("=== 1. me() + usage() ===")
	me, err := admin.Me(ctx)
	if err != nil {
		fmt.Printf("  admin endpoints require admin token — %v\n", err)
		return
	}
	fmt.Printf("  tenant_id=%v  tier=%v\n", me["tenant_id"], me["tier"])
	usage, err := admin.Usage(ctx)
	if err != nil {
		fmt.Printf("  usage error: %v\n", err)
	} else {
		fmt.Printf("  usage: %v\n", usage)
	}

	// ── 2. Create a new tenant ────────────────────────────────────────────
	newID := randomID()
	fmt.Printf("\n=== 2. Create('%s') ===\n", newID)
	created, err := admin.Create(ctx, newID, &relata.CreateTenantOptions{Tier: "free"})
	if err != nil {
		fmt.Printf("  create failed — %v\n", err)
		return
	}
	fmt.Printf("  created: %v\n", created)

	// ── 3. SetTier + SetQuota ─────────────────────────────────────────────
	fmt.Printf("\n=== 3. SetTier('%s', 'server') + SetQuota(1_000_000) ===\n", newID)
	if r, err := admin.SetTier(ctx, newID, "server"); err != nil {
		fmt.Printf("  setTier failed — %v\n", err)
	} else {
		fmt.Printf("  setTier: %v\n", r)
	}
	if r, err := admin.SetQuota(ctx, newID, 1_000_000); err != nil {
		fmt.Printf("  setQuota failed — %v\n", err)
	} else {
		fmt.Printf("  setQuota: %v\n", r)
	}

	// ── 4. Suspend + Reactivate ───────────────────────────────────────────
	fmt.Printf("\n=== 4. Suspend('%s') → Reactivate('%s') ===\n", newID, newID)
	if r, err := admin.Suspend(ctx, newID); err != nil {
		fmt.Printf("  suspend failed — %v\n", err)
	} else {
		fmt.Printf("  suspend: %v\n", r)
	}
	if r, err := admin.Reactivate(ctx, newID); err != nil {
		fmt.Printf("  reactivate failed — %v\n", err)
	} else {
		fmt.Printf("  reactivate: %v\n", r)
	}

	// ── 5. Per-tenant usage + sharing agreements ──────────────────────────
	fmt.Printf("\n=== 5. TenantUsage('%s') + ListSharing ===\n", newID)
	if r, err := admin.TenantUsage(ctx, newID); err != nil {
		fmt.Printf("  tenantUsage failed — %v\n", err)
	} else {
		fmt.Printf("  tenantUsage: %v\n", r)
	}
	if r, err := admin.ListSharing(ctx, newID); err != nil {
		fmt.Printf("  listSharing failed — %v\n", err)
	} else {
		fmt.Printf("  sharing: %d agreement(s)\n", len(r))
	}

	// ── 6. Platform-wide usage ────────────────────────────────────────────
	fmt.Println("\n=== 6. PlatformUsage() ===")
	if r, err := admin.PlatformUsage(ctx); err != nil {
		fmt.Printf("  failed — %v\n", err)
	} else {
		fmt.Printf("  platform usage: %v\n", r)
	}
}
