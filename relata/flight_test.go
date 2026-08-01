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

// #2362: grpcs:// and tls:// endpoints must select a TLS-protected Flight
// connection; grpc:// and http:// remain plaintext.
func TestIsSecureFlightEndpoint(t *testing.T) {
	cases := map[string]bool{
		"grpc://localhost:8815":  false,
		"grpcs://localhost:8815": true,
		"tls://localhost:8815":   true,
		"http://localhost:8815":  false,
		"localhost:8815":         false,
	}
	for in, want := range cases {
		if got := isSecureFlightEndpoint(in); got != want {
			t.Errorf("isSecureFlightEndpoint(%q) = %v, want %v", in, got, want)
		}
	}
}

// #2362: the scheme prefix must be stripped regardless of which one is used,
// leaving grpc.NewClient a bare host:port target.
func TestStripFlightScheme(t *testing.T) {
	cases := map[string]string{
		"grpc://localhost:8815":  "localhost:8815",
		"grpcs://localhost:8815": "localhost:8815",
		"tls://localhost:8815":   "localhost:8815",
		"http://localhost:8815":  "localhost:8815",
		"localhost:8815":         "localhost:8815",
	}
	for in, want := range cases {
		if got := stripFlightScheme(in); got != want {
			t.Errorf("stripFlightScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// #2362: the core regression this issue reports — queryFlightDoGet must no
// longer hardcode insecure.NewCredentials() unconditionally. A grpcs://
// endpoint must select TLS transport credentials; a plain grpc:// endpoint
// must keep the existing plaintext behavior (dev/loopback default).
func TestFlightTransportCredentialsSelectsTLSForSecureScheme(t *testing.T) {
	creds := flightTransportCredentials("grpcs://relata.example:8815")
	if got := creds.Info().SecurityProtocol; got != "tls" {
		t.Fatalf("expected tls security protocol for grpcs:// endpoint, got %q", got)
	}
}

func TestFlightTransportCredentialsPlaintextForGrpcScheme(t *testing.T) {
	creds := flightTransportCredentials("grpc://relata.example:8815")
	if got := creds.Info().SecurityProtocol; got != "insecure" {
		t.Fatalf("expected insecure security protocol for grpc:// endpoint, got %q", got)
	}
}
