package relata

import "fmt"

// ExtensionSuggestion describes one application-defined extension profile.
type ExtensionSuggestion struct {
	Pack        string   `json:"pack"`
	Relevance   string   `json:"relevance"`
	ActiveTypes []string `json:"active_types"`
	AllTypes    []string `json:"all_types"`
}

type extensionProfile struct {
	pack  string
	types []string
}

var extensionProfiles = []extensionProfile{
	{"telco", []string{"MsisdnRecord", "CallRecord", "CdrRecord", "ImsiRecord", "TowerDump"}},
	{"finint", []string{"BankAccount", "FinancialTransaction", "CryptoWallet", "AmlAlert", "WireTransferHop"}},
	{"fara", []string{"Organization", "Transaction", "SanctionEntry", "PepRecord"}},
	{"cyber", []string{"ThreatIndicator", "MalwareRecord", "DomainRecord", "NetworkEvent"}},
	{"maritime", []string{"VesselRecord", "FlightRecord"}},
	{"border", []string{"PassportRecord", "VisaRecord", "BorderCrossing"}},
}

// SuggestExtensions composes the former MCP result from a supplied
// McpClient.ListEntityTypes result. It makes no request and is not an
// authorization boundary. "no_data" means no matching name appears in the
// supplied inventory, not that a package is globally absent.
func SuggestExtensions(inventory map[string]any) (map[string]any, error) {
	rawTypes, ok := inventory["types"].([]any)
	if !ok {
		return nil, fmt.Errorf("list_entity_types result must contain a types array")
	}
	storeTypes := make([]string, 0, len(rawTypes))
	for _, raw := range rawTypes {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("list_entity_types entries must be objects")
		}
		name, ok := entry["type"].(string)
		if !ok {
			return nil, fmt.Errorf("list_entity_types entries must contain a type name")
		}
		if _, ok := toNonnegativeInt(entry["rows"]); !ok {
			return nil, fmt.Errorf("list_entity_types rows must be nonnegative integers")
		}
		storeTypes = append(storeTypes, name)
	}
	present := make(map[string]struct{}, len(storeTypes))
	for _, name := range storeTypes {
		present[name] = struct{}{}
	}
	extensions := make([]map[string]any, 0, len(extensionProfiles))
	for _, profile := range extensionProfiles {
		active := make([]string, 0, len(profile.types))
		for _, name := range profile.types {
			if _, ok := present[name]; ok {
				active = append(active, name)
			}
		}
		relevance := "partial"
		if len(active) == 0 {
			relevance = "no_data"
		} else if len(active) == len(profile.types) {
			relevance = "full"
		}
		extensions = append(extensions, map[string]any{
			"pack": profile.pack, "relevance": relevance,
			"active_types": active, "all_types": append([]string(nil), profile.types...),
		})
	}
	return map[string]any{"extensions": extensions, "store_types": storeTypes}, nil
}
