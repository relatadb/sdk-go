package relata

import (
	"encoding/json"
	"testing"
)

// An authenticated /health/ready answers status "ready"; both that and the
// unauthenticated "ok" mean the node is serving.
func TestReadyReportAcceptsReadyStatus(t *testing.T) {
	for _, status := range []string{"ok", "ready"} {
		var r ReadyReport
		if err := json.Unmarshal([]byte(`{"status":"`+status+`"}`), &r); err != nil {
			t.Fatal(err)
		}
		if !r.IsReady {
			t.Errorf("status %q must derive IsReady=true", status)
		}
	}
	var shedding ReadyReport
	_ = json.Unmarshal([]byte(`{"status":"shedding","reason":"queue"}`), &shedding)
	if shedding.IsReady {
		t.Errorf("shedding must not be ready")
	}
}
