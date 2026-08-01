// Package relata: client-side canonical validation — Tier-1 set (#2248).
//
// Pure, dependency-free validators + normalisers for the eight Tier-1 canonical
// identifier kinds, ported from crates/relata-canonical/src/ (email, phone,
// iban, imei, vin, msisdn, ipv4, ipv6).
//
// These run client-side with no server round-trip — deterministic checksum /
// format checks that let callers reject obviously bad input before it hits the
// wire. The server's relata-canonical crate remains the authority; this file
// mirrors its logic so both sides agree.
//
// Two functions per kind:
//   - Validate<Kind>(value) bool
//   - Normalize<Kind>(value) (string, error)
//
// Scope: Tier-1 only (8 of ~76 canonical kinds). The remaining 65 kinds stay
// server-side; typing them client-side is the remaining scope of #2248.

package relata

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// CanonicalError is returned by every Normalize* function. Kind is a
// machine-friendly tag ("empty", "structure", "checksum", …).
type CanonicalError struct {
	Kind    string
	Message string
}

func (e *CanonicalError) Error() string { return e.Kind + ": " + e.Message }

func canonErr(kind, message string) *CanonicalError {
	return &CanonicalError{Kind: kind, Message: message}
}

// AsCanonicalError unwraps err into a *CanonicalError, returning nil if err is
// not a canonical-validation failure.
func AsCanonicalError(err error) *CanonicalError {
	var ce *CanonicalError
	if errors.As(err, &ce) {
		return ce
	}
	return nil
}

func isAsciiAlnum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// ── Email ────────────────────────────────────────────────────────────────────

const (
	maxLocalEmail  = 64
	maxDomainEmail = 255
	maxLabelEmail  = 63
)

// ValidateEmail reports whether value is a valid email address.
func ValidateEmail(value string) bool { _, err := NormalizeEmail(value); return err == nil }

// NormalizeEmail returns the canonical lowercase-domain form of value.
func NormalizeEmail(value string) (string, error) {
	if value == "" {
		return "", canonErr("empty", "empty input")
	}
	if strings.Count(value, "@") != 1 {
		return "", canonErr("structure", "must contain exactly one '@'")
	}
	at := strings.IndexByte(value, '@')
	local := value[:at]
	domain := value[at+1:]

	if local == "" {
		return "", canonErr("structure", "empty local part")
	}
	if len(local) > maxLocalEmail {
		return "", canonErr("length", fmt.Sprintf("local part > %d chars", maxLocalEmail))
	}
	if local[0] == '.' || local[len(local)-1] == '.' {
		return "", canonErr("structure", "local part cannot start or end with '.'")
	}
	prevDot := false
	for i := 0; i < len(local); i++ {
		ch := local[i]
		ok := isAsciiAlnum(ch) || ch == '.' || ch == '_' || ch == '-' || ch == '+'
		if !ok {
			return "", canonErr("invalid_char", fmt.Sprintf("bad char %q in local part", ch))
		}
		if ch == '.' {
			if prevDot {
				return "", canonErr("structure", "consecutive '.' in local part")
			}
			prevDot = true
		} else {
			prevDot = false
		}
	}

	if domain == "" {
		return "", canonErr("structure", "empty domain")
	}
	if len(domain) > maxDomainEmail {
		return "", canonErr("length", fmt.Sprintf("domain > %d chars", maxDomainEmail))
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return "", canonErr("structure", "domain must have at least two labels")
	}
	for _, label := range labels {
		if label == "" {
			return "", canonErr("structure", "empty label")
		}
		if len(label) > maxLabelEmail {
			return "", canonErr("structure", "label > 63 chars")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", canonErr("structure", "label cannot start or end with '-'")
		}
		for i := 0; i < len(label); i++ {
			if !(isAsciiAlnum(label[i]) || label[i] == '-') {
				return "", canonErr("invalid_char", "bad char in domain label")
			}
		}
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 {
		return "", canonErr("structure", "TLD < 2 chars")
	}
	allDigit := true
	for i := 0; i < len(tld); i++ {
		if tld[i] < '0' || tld[i] > '9' {
			allDigit = false
			break
		}
	}
	if allDigit {
		return "", canonErr("structure", "all-numeric TLD")
	}
	return local + "@" + strings.ToLower(domain), nil
}

// ── Phone (E.164) ────────────────────────────────────────────────────────────

// ValidatePhone reports whether value is a valid E.164 phone number.
func ValidatePhone(value string) bool { _, err := NormalizePhone(value); return err == nil }

// NormalizePhone returns the canonical "+<digits>" E.164 form of value.
func NormalizePhone(value string) (string, error) {
	if value == "" {
		return "", canonErr("empty", "empty input")
	}
	if value[0] != '+' {
		return "", canonErr("structure", "missing '+' prefix")
	}
	rest := value[1:]
	if rest == "" {
		return "", canonErr("structure", "no digits after '+'")
	}
	if rest[0] == '0' {
		return "", canonErr("structure", "leading zero (country code starts at 1)")
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return "", canonErr("invalid_char", fmt.Sprintf("non-digit %q", rest[i]))
		}
	}
	if len(rest) > 15 {
		return "", canonErr("structure", "more than 15 digits")
	}
	return "+" + rest, nil
}

// ── IBAN (mod-97) ────────────────────────────────────────────────────────────

// ValidateIban reports whether value is a valid IBAN (mod-97 checksum).
func ValidateIban(value string) bool { _, err := NormalizeIban(value); return err == nil }

// NormalizeIban returns the canonical contiguous uppercased form of value.
func NormalizeIban(value string) (string, error) {
	if value == "" {
		return "", canonErr("empty", "empty input")
	}
	normalised := strings.ToUpper(strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, value))
	if len(normalised) < 15 || len(normalised) > 34 {
		return "", canonErr("length", "expected 15–34 chars")
	}
	if !isAsciiUpperByte(normalised[0]) || !isAsciiUpperByte(normalised[1]) {
		return "", canonErr("structure", "country code must be letters")
	}
	if !isAsciiDigitByte(normalised[2]) || !isAsciiDigitByte(normalised[3]) {
		return "", canonErr("structure", "check digits must be numeric")
	}
	for i := 4; i < len(normalised); i++ {
		if !isAsciiAlnum(normalised[i]) {
			return "", canonErr("invalid_char", fmt.Sprintf("non-alphanumeric byte %#x", normalised[i]))
		}
	}
	if !Mod97Valid(normalised) {
		return "", canonErr("checksum", "mod-97 check failed")
	}
	return normalised, nil
}

func isAsciiUpperByte(b byte) bool { return b >= 'A' && b <= 'Z' }
func isAsciiDigitByte(b byte) bool { return b >= '0' && b <= '9' }

// Mod97Valid is a pure mod-97 check on an uppercased, contiguous IBAN string.
func Mod97Valid(normalised string) bool {
	if len(normalised) < 5 {
		return false
	}
	rotated := normalised[4:] + normalised[:4]
	remainder := 0
	for i := 0; i < len(rotated); i++ {
		ch := rotated[i]
		var val int
		if isAsciiDigitByte(ch) {
			val = int(ch - '0')
		} else if isAsciiUpperByte(ch) {
			val = int(ch-'A') + 10
		} else {
			return false
		}
		if val >= 10 {
			remainder = (remainder*100 + val) % 97
		} else {
			remainder = (remainder*10 + val) % 97
		}
	}
	return remainder == 1
}

// ── IMEI (Luhn) ──────────────────────────────────────────────────────────────

// ValidateImei reports whether value is a valid IMEI (Luhn checksum).
func ValidateImei(value string) bool { _, err := NormalizeImei(value); return err == nil }

// NormalizeImei returns the canonical 15-digit form of value (whitespace trimmed).
func NormalizeImei(value string) (string, error) {
	s := strings.TrimSpace(value)
	if len(s) != 15 {
		return "", canonErr("length", fmt.Sprintf("expected 15 digits, got %d", len(s)))
	}
	digits := make([]int, 15)
	for i := 0; i < 15; i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return "", canonErr("structure", "non-digit in IMEI")
		}
		digits[i] = int(c - '0')
	}
	sum := 0
	for i, d := range digits {
		posFromRight := 14 - i
		if posFromRight%2 == 1 {
			doubled := d * 2
			if doubled > 9 {
				doubled -= 9
			}
			sum += doubled
		} else {
			sum += d
		}
	}
	if sum%10 != 0 {
		return "", canonErr("checksum", "Luhn check failed")
	}
	return s, nil
}

// ── VIN (ISO 3779) ───────────────────────────────────────────────────────────

var vinTranslit = map[byte]int{
	'A': 1, 'J': 1, 'B': 2, 'K': 2, 'S': 2, 'C': 3, 'L': 3, 'T': 3,
	'D': 4, 'M': 4, 'U': 4, 'E': 5, 'N': 5, 'V': 5, 'F': 6, 'W': 6,
	'G': 7, 'P': 7, 'X': 7, 'H': 8, 'Y': 8, 'R': 9, 'Z': 9,
}

var vinWeights = [17]int{8, 7, 6, 5, 4, 3, 2, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2}

// ValidateVin reports whether value is a valid VIN (ISO 3779 check digit).
func ValidateVin(value string) bool { _, err := NormalizeVin(value); return err == nil }

// NormalizeVin returns the canonical 17-char uppercase form of value.
func NormalizeVin(value string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(value))
	if len(s) != 17 {
		return "", canonErr("length", fmt.Sprintf("expected 17 chars, got %d", len(s)))
	}
	values := make([]int, 17)
	for i := 0; i < 17; i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			values[i] = int(c - '0')
		} else if v, ok := vinTranslit[c]; ok {
			values[i] = v
		} else {
			return "", canonErr("structure", fmt.Sprintf("invalid VIN character %q", c))
		}
	}
	sum := 0
	for i, v := range values {
		sum += v * vinWeights[i]
	}
	check := sum % 11
	var expected byte
	if check == 10 {
		expected = 'X'
	} else {
		expected = byte('0' + check)
	}
	if s[8] != expected {
		return "", canonErr("checksum", "check-digit mismatch")
	}
	return s, nil
}

// ── MSISDN ───────────────────────────────────────────────────────────────────

// ValidateMsisdn reports whether value is a valid MSISDN (7–15 digits).
func ValidateMsisdn(value string) bool { _, err := NormalizeMsisdn(value); return err == nil }

// NormalizeMsisdn returns the canonical bare-digit form of value (+ stripped).
func NormalizeMsisdn(value string) (string, error) {
	stripped := strings.TrimLeft(strings.TrimSpace(value), "+")
	if stripped == "" {
		return "", canonErr("empty", "empty input")
	}
	for i := 0; i < len(stripped); i++ {
		if stripped[i] < '0' || stripped[i] > '9' {
			return "", canonErr("structure", "non-digit character in MSISDN")
		}
	}
	if len(stripped) < 7 || len(stripped) > 15 {
		return "", canonErr("length", fmt.Sprintf("expected 7–15 digits, got %d", len(stripped)))
	}
	if stripped[0] == '0' {
		return "", canonErr("structure", "E.164 MSISDN must not start with 0")
	}
	return stripped, nil
}

// ── IPv4 ─────────────────────────────────────────────────────────────────────

// ValidateIpv4 reports whether value is a valid dotted-quad IPv4 address.
func ValidateIpv4(value string) bool { _, err := NormalizeIpv4(value); return err == nil }

// NormalizeIpv4 returns the canonical "a.b.c.d" form of value (no leading zeros).
func NormalizeIpv4(value string) (string, error) {
	if value == "" {
		return "", canonErr("empty", "empty input")
	}
	octets := [4]int{}
	octetCount := 0
	digitCount := 0
	current := 0
	hadLeadingZero := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch == '.' {
			if digitCount == 0 {
				return "", canonErr("structure", "empty octet")
			}
			if octetCount >= 3 {
				return "", canonErr("structure", "too many octets")
			}
			octets[octetCount] = current
			octetCount++
			current = 0
			digitCount = 0
			hadLeadingZero = false
			continue
		}
		if ch < '0' || ch > '9' {
			return "", canonErr("invalid_char", fmt.Sprintf("non-digit %q", ch))
		}
		d := int(ch - '0')
		if digitCount == 0 && d == 0 {
			hadLeadingZero = true
		} else if hadLeadingZero {
			return "", canonErr("structure", "leading zero in octet")
		}
		current = current*10 + d
		digitCount++
		if current > 255 {
			return "", canonErr("structure", "octet > 255")
		}
		if digitCount > 3 {
			return "", canonErr("structure", "octet has too many digits")
		}
	}
	if digitCount == 0 {
		return "", canonErr("structure", "trailing dot")
	}
	if octetCount != 3 {
		return "", canonErr("structure", "wrong number of octets")
	}
	octets[3] = current
	return strconv.Itoa(octets[0]) + "." + strconv.Itoa(octets[1]) + "." +
		strconv.Itoa(octets[2]) + "." + strconv.Itoa(octets[3]), nil
}

// ── IPv6 ─────────────────────────────────────────────────────────────────────

// ValidateIpv6 reports whether value is a valid IPv6 address (RFC 4291).
func ValidateIpv6(value string) bool { _, err := NormalizeIpv6(value); return err == nil }

// ParseIpv6 parses an IPv6 address into 16 canonical big-endian bytes.
func ParseIpv6(value string) ([16]byte, error) {
	var out [16]byte
	if value == "" {
		return out, canonErr("empty", "empty input")
	}
	if strings.Count(value, "::") > 1 {
		return out, canonErr("structure", "multiple '::' compressions")
	}

	parseGroups := func(s string) ([]int, error) {
		parts := strings.Split(s, ":")
		groups := make([]int, 0, len(parts))
		for _, p := range parts {
			if p == "" {
				return nil, canonErr("structure", "empty group")
			}
			if len(p) > 4 {
				return nil, canonErr("structure", "group has > 4 hex digits")
			}
			g, err := strconv.ParseUint(p, 16, 16)
			if err != nil {
				return nil, canonErr("invalid_char", fmt.Sprintf("non-hex in group %q", p))
			}
			groups = append(groups, int(g))
		}
		return groups, nil
	}

	var groups []int
	if strings.Contains(value, "::") {
		halves := strings.SplitN(value, "::", 2)
		left, right := halves[0], halves[1]
		var lg, rg []int
		var err error
		if left != "" {
			if lg, err = parseGroups(left); err != nil {
				return out, err
			}
		}
		if right != "" {
			if rg, err = parseGroups(right); err != nil {
				return out, err
			}
		}
		zeros := 8 - len(lg) - len(rg)
		if zeros < 0 {
			return out, canonErr("structure", "too many groups around '::'")
		}
		if zeros == 0 {
			return out, canonErr("structure", "'::' must compress at least one group")
		}
		groups = make([]int, 0, 8)
		groups = append(groups, lg...)
		for i := 0; i < zeros; i++ {
			groups = append(groups, 0)
		}
		groups = append(groups, rg...)
	} else {
		var err error
		groups, err = parseGroups(value)
		if err != nil {
			return out, err
		}
		if len(groups) != 8 {
			return out, canonErr("structure", "expected 8 groups")
		}
	}

	for i, g := range groups {
		out[i*2] = byte(g >> 8)
		out[i*2+1] = byte(g)
	}
	return out, nil
}

// DisplayIpv6 formats 16 canonical bytes as RFC 5952 lowercase-compressed text.
func DisplayIpv6(addr [16]byte) string {
	groups := [8]int{}
	for i := 0; i < 8; i++ {
		groups[i] = (int(addr[i*2]) << 8) | int(addr[i*2+1])
	}
	bestStart, bestLen := -1, 0
	curStart, curLen := -1, 0
	for i, g := range groups {
		if g == 0 {
			if curLen == 0 {
				curStart = i
			}
			curLen++
			if curLen > bestLen {
				bestLen = curLen
				bestStart = curStart
			}
		} else {
			curLen = 0
		}
	}
	compress := bestLen >= 2

	var sb strings.Builder
	sb.Grow(39)
	i := 0
	for i < 8 {
		if compress && i == bestStart {
			sb.WriteString("::")
			i += bestLen
			continue
		}
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), ":") {
			sb.WriteByte(':')
		}
		sb.WriteString(strconv.FormatInt(int64(groups[i]), 16))
		i++
	}
	return sb.String()
}

// NormalizeIpv6 returns the canonical RFC 5952 lowercase-compressed form.
func NormalizeIpv6(value string) (string, error) {
	addr, err := ParseIpv6(value)
	if err != nil {
		return "", err
	}
	return DisplayIpv6(addr), nil
}
