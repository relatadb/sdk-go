package relata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Vectors mirror sdks/shared/canonical-vectors.json (shared across all 4 SDKs).

type vecCase struct {
	Input    string `json:"input"`
	Expected string `json:"expected"`
}
type vecKind struct {
	Valid  []vecCase `json:"valid"`
	Reject []string  `json:"reject"`
}

func loadVectors(t *testing.T) map[string]vecKind {
	t.Helper()
	// The test runs with cwd = the package dir (sdks/go/relata).
	path := filepath.Join("..", "shared", "canonical-vectors.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared vectors: %v", err)
	}
	// Decode as raw messages first so the shared file's top-level metadata
	// keys (`_comment`, `_note` — string values) don't break the typed decode.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	out := make(map[string]vecKind, 8)
	for _, kind := range []string{
		"email", "phone", "iban", "imei", "vin", "msisdn", "ipv4", "ipv6",
	} {
		b, ok := raw[kind]
		if !ok {
			continue
		}
		var k vecKind
		if err := json.Unmarshal(b, &k); err != nil {
			t.Fatalf("parse vectors[%s]: %v", kind, err)
		}
		out[kind] = k
	}
	return out
}

func TestCanonicalEmailVectors(t *testing.T) {
	testKindVectors(t, "email", NormalizeEmail)
}
func TestCanonicalPhoneVectors(t *testing.T) {
	testKindVectors(t, "phone", NormalizePhone)
}
func TestCanonicalIbanVectors(t *testing.T) {
	testKindVectors(t, "iban", NormalizeIban)
}
func TestCanonicalImeiVectors(t *testing.T) {
	testKindVectors(t, "imei", NormalizeImei)
}
func TestCanonicalVinVectors(t *testing.T) {
	testKindVectors(t, "vin", NormalizeVin)
}
func TestCanonicalMsisdnVectors(t *testing.T) {
	testKindVectors(t, "msisdn", NormalizeMsisdn)
}
func TestCanonicalIpv4Vectors(t *testing.T) {
	testKindVectors(t, "ipv4", NormalizeIpv4)
}
func TestCanonicalIpv6Vectors(t *testing.T) {
	testKindVectors(t, "ipv6", NormalizeIpv6)
}

func testKindVectors(t *testing.T, kind string, norm func(string) (string, error)) {
	t.Helper()
	vec := loadVectors(t)[kind]
	for _, c := range vec.Valid {
		got, err := norm(c.Input)
		if err != nil {
			t.Errorf("%s: normalize(%q) unexpected err: %v", kind, c.Input, err)
			continue
		}
		if got != c.Expected {
			t.Errorf("%s: normalize(%q) = %q, want %q", kind, c.Input, got, c.Expected)
		}
	}
	for _, bad := range vec.Reject {
		if _, err := norm(bad); err == nil {
			t.Errorf("%s: expected rejection of %q", kind, bad)
		}
	}
}

func TestCanonicalValidateBooleans(t *testing.T) {
	if !ValidateEmail("a@b.co") {
		t.Error("ValidateEmail should accept a@b.co")
	}
	if ValidateEmail("bad") {
		t.Error("ValidateEmail should reject bad")
	}
	if !ValidateIban("DE89370400440532013000") {
		t.Error("ValidateIban should accept DE89370400440532013000")
	}
	if ValidateIban("GB82WEST12345698765433") {
		t.Error("ValidateIban should reject bad checksum")
	}
	if !ValidateIpv6("::1") {
		t.Error("ValidateIpv6 should accept ::1")
	}
}

func TestCanonicalErrorType(t *testing.T) {
	_, err := NormalizeEmail("bad")
	ce := AsCanonicalError(err)
	if ce == nil {
		t.Fatalf("expected *CanonicalError, got %T", err)
	}
	if ce.Kind == "" {
		t.Error("CanonicalError.Kind should be non-empty")
	}
}
