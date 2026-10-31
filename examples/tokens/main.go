// Command tokens demonstrates the tenant self-service token surface under
// /v1/auth/tokens/self.
//
// Flow:
//  1. ListSelf()      → the caller's own tokens
//  2. SelfAudit()     → audit trail of those tokens
//  3. RotateSelf()    → replace the calling token (pass -rotate to run it)
//  4. RevokeSelf(id)  → revoke one of the caller's tokens (pass -revoke <id>)
//
// Usage:
//
//	go run ./examples/tokens -url http://localhost:9090 -token $RELATA_TOKEN
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

func main() {
	url := flag.String("url", "http://localhost:9090", "Relata server base URL")
	token := flag.String("token", "", "Bearer token (optional)")
	rotate := flag.Bool("rotate", false, "rotate the calling token (invalidates -token)")
	revoke := flag.String("revoke", "", "token id to revoke")
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "admin",
		Timeout:        30 * time.Second,
	})
	tokens := relata.NewTokenClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Println("=== 1. ListSelf() ===")
	list, err := tokens.ListSelf(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  tokens: %v\n", list)
	}

	fmt.Println("\n=== 2. SelfAudit() ===")
	audit, err := tokens.SelfAudit(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  audit: %v\n", audit)
	}

	if *rotate {
		fmt.Println("\n=== 3. RotateSelf() ===")
		rotated, err := tokens.RotateSelf(ctx)
		if err != nil {
			fmt.Printf("  error: %v\n", err)
		} else {
			fmt.Printf("  rotated: %v\n", rotated)
		}
	}

	if *revoke != "" {
		fmt.Printf("\n=== 4. RevokeSelf('%s') ===\n", *revoke)
		revoked, err := tokens.RevokeSelf(ctx, *revoke)
		if err != nil {
			fmt.Printf("  error: %v\n", err)
		} else {
			fmt.Printf("  revoked: %v\n", revoked)
		}
	}
}
