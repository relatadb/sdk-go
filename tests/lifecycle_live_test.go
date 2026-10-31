package tests

/**
 * Dataset-lifecycle run through the Go SDK against a live server (#5724).
 *
 * Mirrors scripts/sdk_lifecycle_e2e.py: create -> inspect -> compare-and-set
 * alter -> stale-version refusal -> lifecycle progress -> data-token refusal ->
 * drop. Skips unless RELATA_TOKEN (a registry data token) and
 * RELATA_ADMIN_TOKEN are both set, so `go test ./...` stays server-free.
 *
 * Run: scripts/sdk_lifecycle_live.sh (starts nothing; needs a running server).
 */

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

func statusOf(err error) int {
	var re *relata.RelataError
	if errors.As(err, &re) {
		return re.StatusCode
	}
	return 0
}

func TestLifecycleLive(t *testing.T) {
	data, admin := os.Getenv("RELATA_TOKEN"), os.Getenv("RELATA_ADMIN_TOKEN")
	if data == "" || admin == "" {
		t.Skip("RELATA_TOKEN and RELATA_ADMIN_TOKEN not set; live lifecycle run skipped")
	}
	host := getEnv("RELATA_HOST", "http://localhost:9090")
	ctx := context.Background()
	dc := relata.New(host, &relata.ClientOptions{BearerToken: data, Timeout: 15 * time.Second})
	ac := relata.New(host, &relata.ClientOptions{BearerToken: admin, Timeout: 15 * time.Second})
	name := fmt.Sprintf("GoLife%d", time.Now().UnixNano())

	if _, err := ac.RegisterType(ctx, name, map[string]any{"description": "go lifecycle run"}); err != nil {
		t.Fatalf("admin create: %v", err)
	}
	detail, err := dc.TypeDetail(ctx, name)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	v0, _ := detail["schema_version"].(string)
	if len(v0) != 16 {
		t.Fatalf("schema_version token = %q", v0)
	}

	if _, err := dc.RegisterType(ctx, name+"Denied", nil); err == nil {
		t.Fatal("data token must not create a type")
	} else if s := statusOf(err); s != 401 && s != 403 {
		t.Fatalf("data-token create: status %d (%v)", s, err)
	}
	if _, err := dc.TypeDetail(ctx, name+"Denied"); statusOf(err) != 404 {
		t.Fatalf("a refused create must register nothing, got %v", err)
	}

	optional := true
	altered, err := ac.SchemaAlter(ctx, name, "add", "bin",
		&relata.SchemaAlterOptions{ColType: "string", Optional: &optional, ExpectedSchemaVersion: v0})
	if err != nil {
		t.Fatalf("compare-and-set alter: %v", err)
	}
	v1, _ := altered["schema_version"].(string)
	if v1 == v0 || altered["schema_version_before"] != v0 {
		t.Fatalf("alter did not move the token: %v", altered)
	}
	_, err = ac.SchemaAlter(ctx, name, "add", "other",
		&relata.SchemaAlterOptions{ColType: "string", Optional: &optional, ExpectedSchemaVersion: v0})
	if statusOf(err) != 412 {
		t.Fatalf("stale expected_schema_version must be 412, got %v", err)
	}
	after, _ := dc.TypeDetail(ctx, name)
	if after["schema_version"] != v1 {
		t.Fatalf("refused alter changed the schema: %v", after)
	}

	life, err := dc.TypeLifecycle(ctx, name)
	if err != nil {
		t.Fatalf("lifecycle: %v", err)
	}
	var ops [][2]string
	for _, raw := range life["operations"].([]any) {
		o := raw.(map[string]any)
		ops = append(ops, [2]string{o["operation"].(string), o["outcome"].(string)})
	}
	want := [][2]string{{"alter", "refused"}, {"alter", "applied"}, {"register", "applied"}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("lifecycle journal = %v, want %v", ops, want)
	}
	if life["schema_version"] != v1 {
		t.Fatalf("lifecycle schema_version %v != %v", life["schema_version"], v1)
	}

	if _, err := ac.DeregisterType(ctx, name); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := dc.TypeLifecycle(ctx, name); statusOf(err) != 404 {
		t.Fatalf("dropped type must be gone, got %v", err)
	}
}
