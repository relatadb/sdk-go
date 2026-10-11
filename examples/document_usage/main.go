package main

import (
	"context"
	"fmt"
	"os"

	"github.com/relatadb/sdk-go/v2/relata"
)

func main() {
	client := relata.New(env("RELATA_URL", "http://localhost:9090"), &relata.ClientOptions{
		BearerToken: os.Getenv("RELATA_BEARER_TOKEN"),
		Tenant:      requiredEnv("RELATA_TENANT"),
	})
	defer client.Close()

	retrieved := true
	receipt, err := relata.RecordUsage(context.Background(), client, relata.DocumentUsageObservation{
		// Keep stable across retries after a response is lost.
		IdempotencyKey: "answer-42:citation-1",
		Target:         relata.EventTarget{ObjectType: "DocumentSource", Key: "report_id", Value: "report-42"},
		Purpose:        "rag-usage",
		Cited:          &retrieved,
		Retrieved:      &retrieved,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("event %s (%s)\n", receipt.EventID, receipt.Status)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func requiredEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		fmt.Fprintf(os.Stderr, "Set %s to the destination tenant ID\n", key)
		os.Exit(1)
	}
	return value
}
