package relata

import (
	"reflect"
	"testing"
)

func TestRetiredReportHumintMethodsAbsent(t *testing.T) {
	for ty, names := range map[reflect.Type][]string{
		reflect.TypeOf((*AuditClient)(nil)):      {"SignReceipt", "ExportPDF"},
		reflect.TypeOf((*GovernanceClient)(nil)): {"RequestBreakglass", "ApproveBreakglass", "BreakglassStatus"},
	} {
		for _, name := range names {
			if _, ok := ty.MethodByName(name); ok {
				t.Fatalf("retired method %s remains", name)
			}
		}
	}
	if _, ok := reflect.TypeOf((*AuditClient)(nil)).MethodByName("Entries"); !ok {
		t.Fatal("generic audit entries removed")
	}
}
