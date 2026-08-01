package relata

import (
	"testing"
)

func TestFlightTicketInjectsPurpose(t *testing.T) {
	if got := FlightTicket("SELECT 1", ""); got != "SELECT 1" {
		t.Fatalf("empty purpose should pass SQL through verbatim, got %q", got)
	}
	want := "/* PURPOSE 'analytics' */ SELECT 1"
	if got := FlightTicket("SELECT 1", "analytics"); got != want {
		t.Fatalf("purpose not injected: got %q want %q", got, want)
	}
}

func TestFlightTicketEscapesEmbeddedQuote(t *testing.T) {
	got := FlightTicket("SELECT 1", "o'reilly")
	want := "/* PURPOSE 'o''reilly' */ SELECT 1"
	if got != want {
		t.Fatalf("embedded quote not doubled: got %q want %q", got, want)
	}
}

func TestResolveFlightEndpointDerivesDefault(t *testing.T) {
	cases := map[string]string{
		"http://localhost:9090":    "grpc://localhost:8815",
		"https://relata.com:443/":  "grpc://relata.com:8815",
		"garbage":                  "grpc://localhost:8815",
	}
	for in, want := range cases {
		if got := ResolveFlightEndpoint(in, ""); got != want {
			t.Errorf("ResolveFlightEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveFlightEndpointOverrideWins(t *testing.T) {
	got := ResolveFlightEndpoint("http://localhost:9090", "grpc://h:9999")
	want := "grpc://h:9999"
	if got != want {
		t.Fatalf("explicit override did not win: got %q want %q", got, want)
	}
}
