package relata

import "testing"

func TestSuggestExtensionsComposesSuppliedInventory(t *testing.T) {
	result, err := SuggestExtensions(map[string]any{"types": []any{
		map[string]any{"type": "CallRecord", "rows": float64(2)},
		map[string]any{"type": "MsisdnRecord", "rows": float64(0)},
		map[string]any{"type": "Person", "rows": float64(100)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result["store_types"].([]string); len(got) != 3 || got[0] != "CallRecord" {
		t.Fatalf("store types = %v", got)
	}
	extensions := result["extensions"].([]map[string]any)
	telco := extensions[0]
	if telco["relevance"] != "partial" {
		t.Fatalf("telco relevance = %v", telco["relevance"])
	}
	if got := telco["active_types"].([]string); len(got) != 2 || got[0] != "MsisdnRecord" || got[1] != "CallRecord" {
		t.Fatalf("active types = %v", got)
	}
	if extensions[1]["relevance"] != "no_data" {
		t.Fatalf("finint relevance = %v", extensions[1]["relevance"])
	}
}

func TestSuggestExtensionsValidatesListEntityTypesShape(t *testing.T) {
	if _, err := SuggestExtensions(map[string]any{}); err == nil {
		t.Fatal("expected missing types error")
	}
	if _, err := SuggestExtensions(map[string]any{"types": []any{map[string]any{"type": "CallRecord", "rows": -1}}}); err == nil {
		t.Fatal("expected invalid row count error")
	}
}
