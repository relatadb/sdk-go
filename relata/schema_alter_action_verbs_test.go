package relata

// Regression test for #4704: SchemaAlter's documented short action verbs
// ("add" | "drop" | "rename" | "retype") must be translated to the server's
// long-form verbs (add_column | remove_column | rename_column |
// change_type, crates/relata-cli/src/serve/types_routes.rs
// schema_alter_handler) on the wire, since the server only ever accepted
// the long form and every call following SchemaAlter's own doc comment
// previously 400'd.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSchemaAlter_TranslatesDocumentedShortVerbs(t *testing.T) {
	cases := []struct {
		action   string
		expected string
	}{
		{"add", "add_column"},
		{"drop", "remove_column"},
		{"rename", "rename_column"},
		{"retype", "change_type"},
		// Long-form verbs pass through unchanged.
		{"add_column", "add_column"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.action, func(t *testing.T) {
			var gotPath, gotMethod string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"schema_generation":2}`)
			}))
			defer srv.Close()

			c := newTestClient(srv, nil)
			_, err := c.SchemaAlter(context.Background(), "Company", tc.action, "lei", &SchemaAlterOptions{ColType: "text"})
			if err != nil {
				t.Fatalf("SchemaAlter: %v", err)
			}
			if gotMethod != "PATCH" || gotPath != "/types/Company/schema" {
				t.Fatalf("method/path = %s %s", gotMethod, gotPath)
			}
			if gotBody["action"] != tc.expected {
				t.Fatalf("action = %v, want %v", gotBody["action"], tc.expected)
			}
		})
	}
}
