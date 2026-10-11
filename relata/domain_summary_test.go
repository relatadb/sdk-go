package relata

import "testing"

func TestSummarizeDomainUsesVisibleInventory(t *testing.T) {
	result, err := SummarizeDomain("financial", map[string]any{"types": []any{
		map[string]any{"type": "FinancialTransaction", "rows": float64(7)},
		map[string]any{"type": "BankAccount", "rows": float64(2)},
		map[string]any{"type": "Person", "rows": float64(1000)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	summaries := result["summaries"].([]map[string]any)
	summary := summaries[0]
	if summary["total_rows"] != 9 {
		t.Fatalf("total_rows = %v, want 9", summary["total_rows"])
	}
	types := summary["types"].([]map[string]any)
	if types[0]["type"] != "BankAccount" || types[0]["rows"] != 2 {
		t.Fatalf("first type = %v", types[0])
	}
	if len(summary["suggested_queries"].([]string)) != 3 {
		t.Fatalf("suggested queries = %v", summary["suggested_queries"])
	}
}

func TestSummarizeDomainAllAndValidation(t *testing.T) {
	all, err := SummarizeDomain("all", map[string]any{"types": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(all["summaries"].([]map[string]any)); got != 9 {
		t.Fatalf("all summaries = %d, want 9", got)
	}
	if _, err := SummarizeDomain("other", map[string]any{"types": []any{}}); err == nil {
		t.Fatal("expected unknown domain error")
	}
	if _, err := SummarizeDomain("telco", map[string]any{"types": []any{map[string]any{"type": "CallRecord", "rows": -1}}}); err == nil {
		t.Fatal("expected invalid count error")
	}
}
