// Command jobs_workflows demonstrates the jobs + workflows client methods (#723).
//
// Usage:
//
//	go run ./examples/jobs_workflows \
//	    -url http://localhost:8080 \
//	    -token $RELATA_BEARER_TOKEN
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/OpenWorkBench-Co/RelataDB/sdks/go/relata"
)

func main() {
	rawURL := flag.String("url", "http://localhost:8080", "Relata server URL")
	token := flag.String("token", "demo", "Bearer token")
	flag.Parse()

	c := relata.New(*rawURL, &relata.ClientOptions{BearerToken: *token})
	sys := relata.NewSystemClient(c)
	ctx := context.Background()

	jobs, err := sys.JobsStatus(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("jobs:", jobs)

	c2, err := sys.JobStatus(ctx, "c2_beacon_detect")
	if err != nil {
		log.Println("job_status:", err)
	} else {
		fmt.Println("job/c2:", c2)
	}

	wfs, err := sys.ListWorkflows(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("workflows:", wfs)

	_, err = sys.RegisterWorkflow(ctx, "demo_flow", []map[string]any{
		{"name": "s1", "kind": "query", "sql": "SELECT 1"},
	})
	if err != nil {
		log.Println("register_workflow:", err)
	}

	def, err := sys.GetWorkflow(ctx, "demo_flow")
	if err != nil {
		log.Println("get_workflow:", err)
	} else {
		fmt.Println("def:", def)
	}

	run, err := sys.RunWorkflow(ctx, "demo_flow")
	if err != nil {
		log.Println("run_workflow:", err)
	} else {
		fmt.Println("run:", run)
	}

	status, err := sys.WorkflowStatus(ctx, "demo_flow")
	if err != nil {
		log.Println("workflow_status:", err)
	} else {
		fmt.Println("status:", status)
	}

	if run != nil {
		runID, _ := run["run_id"].(string)
		if runID != "" {
			detail, err := sys.WorkflowRun(ctx, runID)
			if err != nil {
				log.Println("workflow_run:", err)
			} else {
				fmt.Printf("run_detail: attempts=%d last_error=%v\n", detail.AttemptCount, detail.LastError)
			}
		}
	}
}
