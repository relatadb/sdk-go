package relata

import (
	"fmt"
)

type domainProfile struct {
	types   []string
	queries []string
}

var domainProfiles = map[string]domainProfile{
	"financial": {[]string{"BankAccount", "FinancialTransaction", "CryptoWallet", "AmlAlert", "Transaction", "WireTransferHop"}, []string{"SELECT * FROM FinancialTransaction ORDER BY amount DESC LIMIT 20", "SELECT * FROM AmlAlert WHERE risk_score > 70 LIMIT 10", "SELECT * FROM CryptoWallet LIMIT 20"}},
	"telco":     {[]string{"MsisdnRecord", "CallRecord", "CdrRecord", "ImsiRecord", "TowerDump"}, []string{"SELECT * FROM CallRecord LIMIT 20", "SELECT * FROM MsisdnRecord LIMIT 20", "SELECT * FROM TowerDump LIMIT 10"}},
	"cyber":     {[]string{"ThreatIndicator", "MalwareRecord", "DomainRecord", "NetworkEvent", "IpAddress"}, []string{"SELECT * FROM ThreatIndicator WHERE threat_score > 50 LIMIT 20", "SELECT * FROM MalwareRecord LIMIT 10", "SELECT * FROM NetworkEvent LIMIT 20"}},
	"humint":    {[]string{"SourceReport", "DocumentSource", "CaseFile"}, []string{"SELECT * FROM DocumentSource LIMIT 10", "SELECT * FROM CaseFile LIMIT 10", "SELECT * FROM SourceReport LIMIT 10"}},
	"narcotics": {[]string{"Person", "Organization", "CaseFile", "Transaction", "Vehicle"}, []string{"SELECT * FROM Person LIMIT 20"}},
	"fara":      {[]string{"Person", "Organization", "Transaction", "SanctionEntry", "PepRecord"}, []string{"SELECT * FROM Person LIMIT 20"}},
	"maritime":  {[]string{"VesselRecord", "FlightRecord", "Location"}, []string{"SELECT * FROM Person LIMIT 20"}},
	"border":    {[]string{"PassportRecord", "VisaRecord", "BorderCrossing", "Person"}, []string{"SELECT * FROM Person LIMIT 20"}},
	"sanctions": {[]string{"SanctionEntry", "WatchlistEntry", "PepRecord", "Person", "Organization"}, []string{"SELECT * FROM SanctionEntry LIMIT 20", "SELECT * FROM WatchlistEntry WHERE status = 'active' LIMIT 20", "SELECT * FROM PepRecord LIMIT 10"}},
}

// SummarizeDomain composes the former domain summary from the `types` array
// returned by an authenticated, tenant-scoped McpClient.ListEntityTypes call.
// It performs no request or store access and is not an authorization boundary.
func SummarizeDomain(domain string, inventory map[string]any) (map[string]any, error) {
	if domain != "all" {
		if _, ok := domainProfiles[domain]; !ok {
			return nil, fmt.Errorf("unknown domain: %s", domain)
		}
	}
	rawTypes, ok := inventory["types"].([]any)
	if !ok {
		return nil, fmt.Errorf("list_entity_types result must contain a types array")
	}
	counts := make(map[string]int, len(rawTypes))
	for _, raw := range rawTypes {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("list_entity_types entries must be objects")
		}
		name, ok := entry["type"].(string)
		if !ok {
			return nil, fmt.Errorf("list_entity_types entries must contain a type name")
		}
		rows, ok := toNonnegativeInt(entry["rows"])
		if !ok {
			return nil, fmt.Errorf("list_entity_types rows must be nonnegative integers")
		}
		counts[name] = rows
	}

	selected := make([]string, 0, len(domainProfiles))
	if domain == "all" {
		for _, name := range []string{"financial", "telco", "cyber", "humint", "narcotics", "fara", "maritime", "border", "sanctions"} {
			selected = append(selected, name)
		}
	} else {
		selected = append(selected, domain)
	}
	summaries := make([]map[string]any, 0, len(selected))
	for _, name := range selected {
		profile := domainProfiles[name]
		types := make([]map[string]any, 0, len(profile.types))
		total := 0
		for _, objectType := range profile.types {
			rows := counts[objectType]
			total += rows
			types = append(types, map[string]any{"type": objectType, "rows": rows})
		}
		summaries = append(summaries, map[string]any{
			"domain": name, "total_rows": total, "types": types, "suggested_queries": profile.queries,
		})
	}
	return map[string]any{"domain": domain, "summaries": summaries}, nil
}

func toNonnegativeInt(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, n >= 0
	case int64:
		return int(n), n >= 0 && int64(int(n)) == n
	case uint64:
		return int(n), n <= uint64(^uint(0)>>1)
	case float64:
		return int(n), n >= 0 && n <= float64(^uint(0)>>1) && float64(int(n)) == n
	default:
		return 0, false
	}
}
