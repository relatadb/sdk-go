package relata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const deletionReceiptWire = `{"version":1,"operation_id":"operation","scope":"observed_local_source_versions","global_copy_inventory":"incomplete","global_physical_erasure_verified":false,"revision":18446744073709551615,"sources":[{"system_from":9223372036854775806,"closed_at_ns":9223372036854775807}],"peers":[{"receipt":{"revision":18446744073709551615,"updated_ns":1791710000000000001}}],"known_backup_copies":[{"removal_receipt":{"attempted_at_ns":1791710000000000001,"removed_at_ns":1791710000000000003}}],"text":"9223372036854775806"}`

func TestDeletionReceiptExactNumbersRoundTrip(t *testing.T) {
	receipt, err := DecodeDeletionProgress(strings.NewReader(deletionReceiptWire), 0)
	if err != nil {
		t.Fatal(err)
	}
	if receipt["revision"] != json.Number("18446744073709551615") {
		t.Fatalf("rounded revision: %v", receipt["revision"])
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{`"system_from":9223372036854775806`, `"closed_at_ns":9223372036854775807`, `"revision":18446744073709551615`, `"updated_ns":1791710000000000001`, `"removed_at_ns":1791710000000000003`} {
		if !strings.Contains(string(encoded), token) {
			t.Fatalf("lost exact token %s: %s", token, encoded)
		}
	}
	if receipt["text"] != "9223372036854775806" {
		t.Fatal("changed quoted text")
	}
}

func TestDeletionReceiptRefusesMalformedAndOversizedInput(t *testing.T) {
	for _, wire := range []string{"{}", deletionReceiptWire + "{}", strings.Replace(deletionReceiptWire, `"version":1`, `"version":1.0`, 1), strings.Replace(deletionReceiptWire, "18446744073709551615", "18446744073709551616", 1)} {
		if _, err := DecodeDeletionProgress(strings.NewReader(wire), 0); err == nil {
			t.Fatal("accepted malformed receipt")
		}
	}
	if _, err := DecodeDeletionProgress(strings.NewReader(deletionReceiptWire), 8); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("cap error: %v", err)
	}
}

func TestDeletionReceiptDoesNotChangeGenericClientDecoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, deletionReceiptWire)
	}))
	defer server.Close()
	generic, err := New(server.URL, nil).EraseSubject(context.Background(), "audit", "fixture", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := generic["revision"].(float64); !ok {
		t.Fatalf("generic numbers changed: %T", generic["revision"])
	}
}
