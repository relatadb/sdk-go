package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureSQL(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var gotSQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		gotSQL, _ = body["sql"].(string)
		fmt.Fprint(w, `{"rows":[]}`)
	}))
	return srv, &gotSQL
}

func TestVesselTrack_BareMMSI(t *testing.T) {
	srv, gotSQL := captureSQL(t)
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	if _, err := c.VesselTrack(context.Background(), "analytics", 123456789, 0); err != nil {
		t.Fatal(err)
	}
	if *gotSQL != "VESSEL_TRACK(123456789)" {
		t.Errorf("sql = %q", *gotSQL)
	}
}

func TestVesselTrack_WithWindow(t *testing.T) {
	srv, gotSQL := captureSQL(t)
	defer srv.Close()
	c := newTestClient(srv, &ClientOptions{DefaultPurpose: "analytics"})
	if _, err := c.VesselTrack(context.Background(), "analytics", 123456789, 3600); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("VESSEL_TRACK(123456789, WINDOW => %d)", 3600*1_000_000_000)
	if *gotSQL != want {
		t.Errorf("sql = %q, want %q", *gotSQL, want)
	}
}

func TestDarkFleetDetect_Default(t *testing.T) {
	srv, gotSQL := captureSQL(t)
	defer srv.Close()
	c := newTestClient(srv, nil)
	if _, err := c.DarkFleetDetect(context.Background(), "analytics", 0); err != nil {
		t.Fatal(err)
	}
	if *gotSQL != "DARK_FLEET_DETECT()" {
		t.Errorf("sql = %q", *gotSQL)
	}
}

func TestDarkFleetDetect_WithGap(t *testing.T) {
	srv, gotSQL := captureSQL(t)
	defer srv.Close()
	c := newTestClient(srv, nil)
	if _, err := c.DarkFleetDetect(context.Background(), "analytics", 12.0); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(*gotSQL, "DARK_FLEET_DETECT(MAX_GAP_HOURS => ") {
		t.Errorf("sql = %q", *gotSQL)
	}
}

func TestVesselToVesselTransfer_Default(t *testing.T) {
	srv, gotSQL := captureSQL(t)
	defer srv.Close()
	c := newTestClient(srv, nil)
	if _, err := c.VesselToVesselTransfer(context.Background(), "analytics", 0, 0); err != nil {
		t.Fatal(err)
	}
	if *gotSQL != "VESSEL_TO_VESSEL_TRANSFER()" {
		t.Errorf("sql = %q", *gotSQL)
	}
}

func TestVesselToVesselTransfer_WithOpts(t *testing.T) {
	srv, gotSQL := captureSQL(t)
	defer srv.Close()
	c := newTestClient(srv, nil)
	if _, err := c.VesselToVesselTransfer(context.Background(), "analytics", 0.5, 30); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*gotSQL, "PROXIMITY_NM => 0.5") || !strings.Contains(*gotSQL, "TIME_WINDOW_MINUTES => 30") {
		t.Errorf("sql = %q", *gotSQL)
	}
}
