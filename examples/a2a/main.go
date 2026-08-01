// Command a2a demonstrates the Agent-to-Agent task lifecycle + checkpoints
// (#967).
//
// The A2A door (.well-known/agent.json + /a2a/tasks/* + /a2a/checkpoints/*)
// lets one agent ask another agent to do work and track its progress. It is
// the production-grade replacement for ad-hoc LLM-to-LLM HTTP plumbing.
//
// Flow demonstrated:
//  1. Discover the agent card (GET /.well-known/agent.json)
//  2. Submit a task (POST /a2a/tasks) with a JSON-RPC-style payload
//  3. Poll task status (GET /a2a/tasks/:id)
//  4. List LangGraph-style checkpoints for a thread
//
// Usage:
//
//	go run ./examples/a2a -url http://localhost:9090 -token $RELATA_TOKEN
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
	flag.Parse()

	client := relata.New(*url, &relata.ClientOptions{
		BearerToken:    *token,
		DefaultPurpose: "agent",
		Timeout:        60 * time.Second,
	})
	a2a := relata.NewA2AClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// ── 1. Agent card (public discovery) ──────────────────────────────────
	fmt.Println("=== 1. GET /.well-known/agent.json ===")
	card, err := a2a.AgentCard(ctx)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  name=%v  version=%v  description=%v\n",
			card["name"], card["version"], card["description"])
		if skills, _ := card["skills"].([]any); len(skills) > 0 {
			fmt.Printf("  %d skill(s):\n", len(skills))
			for i, s := range skills {
				if i >= 5 {
					break
				}
				if skill, ok := s.(map[string]any); ok {
					fmt.Printf("    - %v : %v\n", skill["id"], skill["name"])
				}
			}
		}
	}

	// ── 2. Submit a task ──────────────────────────────────────────────────
	fmt.Println("\n=== 2. POST /a2a/tasks ===")
	taskID := fmt.Sprintf("task-%d", time.Now().UnixMilli())
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  "tasks/send",
		"params": map[string]any{
			"id": taskID,
			"message": map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"type": "text", "text": "Summarise the latest 5 audit entries."}},
			},
		},
	}
	created, err := a2a.SubmitTask(ctx, payload)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		if r, ok := created["result"].(map[string]any); ok {
			if id, ok := r["id"].(string); ok && id != "" {
				taskID = id
			}
		}
		fmt.Printf("  task_id=%s\n", taskID)
		fmt.Printf("  response: %v\n", created)
	}

	// ── 3. Poll status ────────────────────────────────────────────────────
	fmt.Printf("\n=== 3. GET /a2a/tasks/%s ===\n", taskID)
	status, err := a2a.GetTask(ctx, taskID)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		state := "unknown"
		if r, ok := status["result"].(map[string]any); ok {
			if s, ok := r["status"].(map[string]any); ok {
				if v, ok := s["state"].(string); ok {
					state = v
				}
			}
		}
		if s, ok := status["status"].(map[string]any); ok {
			if v, ok := s["state"].(string); ok {
				state = v
			}
		}
		if v, ok := status["state"].(string); ok {
			state = v
		}
		fmt.Printf("  state=%s\n", state)
	}

	// ── 4. Checkpoints (LangGraph thread state) ───────────────────────────
	threadID := taskID
	fmt.Printf("\n=== 4. GET /a2a/checkpoints/%s ===\n", threadID)
	checkpoints, err := a2a.ListCheckpoints(ctx, threadID)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Printf("  %d checkpoint(s) for thread %s\n", len(checkpoints), threadID)
	}
}
