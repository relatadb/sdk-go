package relata

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

var dnsTunnelTypes = []string{"DnsLog", "DnsQuery", "DnsEvent"}

const (
	defaultDNSTunnelPageSize = 1000
	defaultDNSTunnelMaxRows  = 100000
)

// DnsTunnelOptions configures the local DNS tunnel triage recipe.
type DnsTunnelOptions struct {
	MinEntropy *float64
	MinQueries *uint64
	PageSize   int
	MaxRows    int
}

// DnsTunnelFinding preserves the fields returned by the retired SQL operator.
type DnsTunnelFinding struct {
	Domain      string  `json:"domain"`
	QueryCount  uint64  `json:"query_count"`
	AvgEntropy  float64 `json:"avg_entropy"`
	MaxLabelLen int     `json:"max_label_len"`
	Confidence  float64 `json:"confidence"`
}

// DnsTunnelResult contains application-computed findings and reports whether
// all authorized pages were read. Continuation maps a type to a server-issued
// cursor; remaining_types lists types that were not started after a budget stop.
type DnsTunnelResult struct {
	Columns          []string           `json:"columns"`
	Rows             []DnsTunnelFinding `json:"rows"`
	TypesScanned     []string           `json:"types_scanned"`
	RowsScanned      int                `json:"rows_scanned"`
	Complete         bool               `json:"complete"`
	Continuation     map[string]string  `json:"continuation"`
	IncompleteReason string             `json:"incomplete_reason,omitempty"`
}

// DnsTunnelDetect runs the former core DNS heuristic as local application
// composition over ACL-filtered schema names and governed keyset-paged SELECTs.
// Every data page is still authorized by Relata for the requested purpose and
// tenant. It returns Complete=false rather than claiming a budgeted/truncated
// result is a full scan.
func (c *Client) DnsTunnelDetect(ctx context.Context, purpose string, opts *DnsTunnelOptions) (*DnsTunnelResult, error) {
	if purpose == "" {
		purpose = c.defaultPurpose
	}
	if purpose == "" {
		purpose = "security"
	}
	minEntropy, minQueries := 3.0, uint64(5)
	options := DnsTunnelOptions{PageSize: defaultDNSTunnelPageSize, MaxRows: defaultDNSTunnelMaxRows}
	if opts != nil {
		if opts.MinEntropy != nil {
			minEntropy = *opts.MinEntropy
		}
		if opts.MinQueries != nil {
			minQueries = *opts.MinQueries
		}
		if opts.PageSize != 0 {
			options.PageSize = opts.PageSize
		}
		if opts.MaxRows != 0 {
			options.MaxRows = opts.MaxRows
		}
	}
	if options.PageSize < 1 || options.MaxRows < 1 {
		return nil, fmt.Errorf("page_size and max_rows must be positive")
	}
	if options.PageSize > 10000 {
		options.PageSize = 10000
	}
	schema, err := c.QuerySchema(ctx, purpose)
	if err != nil {
		return nil, err
	}
	visible, ok := schema["object_types"].([]any)
	if !ok {
		return nil, fmt.Errorf("query_schema result must contain an object_types array")
	}
	visibleSet := make(map[string]struct{}, len(visible))
	for _, value := range visible {
		name, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("query_schema object_types entries must be strings")
		}
		visibleSet[name] = struct{}{}
	}
	types := make([]string, 0, len(dnsTunnelTypes))
	for _, name := range dnsTunnelTypes {
		if _, exists := visibleSet[name]; exists {
			types = append(types, name)
		}
	}
	rows := make([]map[string]any, 0)
	continuation := map[string]string{}
	rowsScanned := 0
	complete := true
	incompleteReason := ""
	for typeIndex, objectType := range types {
		var cursor *string
		seen := map[string]struct{}{}
		for {
			remaining := options.MaxRows - rowsScanned
			if remaining <= 0 {
				complete = false
				incompleteReason = "max_rows_reached"
				break
			}
			limit := min(options.PageSize, remaining)
			sql := fmt.Sprintf("SELECT * FROM %s LIMIT %d", objectType, limit)
			if cursor != nil {
				sql += " AFTER " + sqlLiteral(*cursor)
			}
			sql += " WITH CACHE BYPASS"
			page, queryErr := c.Query(ctx, sql, WithPurpose(purpose))
			if queryErr != nil {
				return nil, queryErr
			}
			rows = append(rows, page.Rows...)
			rowsScanned += len(page.Rows)
			if page.Partial {
				complete = false
				incompleteReason = "server_returned_partial_shard_result"
				break
			}
			if page.Truncated {
				complete = false
				incompleteReason = "server_truncated_page_without_safe_resume_cursor"
				break
			}
			if page.NextCursor != nil {
				if *page.NextCursor == "" || *page.NextCursor == derefString(cursor) {
					complete = false
					incompleteReason = "pagination_cursor_did_not_advance"
					continuation[objectType] = *page.NextCursor
					break
				}
				if _, exists := seen[*page.NextCursor]; exists {
					complete = false
					incompleteReason = "pagination_cursor_did_not_advance"
					continuation[objectType] = *page.NextCursor
					break
				}
				seen[*page.NextCursor] = struct{}{}
			} else if len(page.Rows) == limit && !page.HasNextCursor {
				complete = false
				incompleteReason = "server_did_not_return_pagination_metadata"
				break
			}
			if page.NextCursor == nil {
				break
			}
			if rowsScanned >= options.MaxRows {
				complete = false
				incompleteReason = "max_rows_reached"
				continuation[objectType] = *page.NextCursor
				break
			}
			cursor = page.NextCursor
		}
		if !complete {
			continuation["remaining_types"] = strings.Join(types[typeIndex:], ",")
			break
		}
	}
	return formatDNSTunnelResult(rows, types, minEntropy, minQueries, rowsScanned, complete, continuation, incompleteReason), nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func formatDNSTunnelResult(rows []map[string]any, types []string, minEntropy float64, minQueries uint64, scanned int, complete bool, continuation map[string]string, reason string) *DnsTunnelResult {
	type accumulator struct {
		entropySum float64
		labelCount uint64
		count      uint64
		maxLen     int
	}
	byDomain := make(map[string]*accumulator)
	for _, row := range rows {
		var value any
		found := false
		for _, field := range []string{"query_name", "qname", "domain", "name"} {
			if candidate, ok := row[field]; ok {
				value, found = candidate, true
				break
			}
		}
		name, ok := value.(string)
		if !found || !ok {
			continue
		}
		name = strings.ToLower(strings.TrimRight(name, "."))
		if name == "" {
			continue
		}
		labels := make([]string, 0)
		for _, label := range strings.Split(name, ".") {
			if label != "" {
				labels = append(labels, label)
			}
		}
		if len(labels) == 0 {
			continue
		}
		domain := labels[len(labels)-1]
		var sublabels []string
		if len(labels) >= 2 {
			domain = labels[len(labels)-2] + "." + labels[len(labels)-1]
		}
		if len(labels) > 2 {
			sublabels = labels[:len(labels)-2]
		}
		item := byDomain[domain]
		if item == nil {
			item = &accumulator{}
			byDomain[domain] = item
		}
		item.count++
		for _, label := range sublabels {
			item.entropySum += dnsShannonEntropy(label)
			item.labelCount++
			if len(label) > item.maxLen {
				item.maxLen = len(label) // Rust str::len counts UTF-8 bytes.
			}
		}
	}
	result := &DnsTunnelResult{
		Columns: []string{"domain", "query_count", "avg_entropy", "max_label_len", "confidence"},
		Rows:    []DnsTunnelFinding{}, TypesScanned: append([]string(nil), types...), RowsScanned: scanned, Complete: complete,
		Continuation: continuation, IncompleteReason: reason,
	}
	for domain, item := range byDomain {
		if item.count < minQueries {
			continue
		}
		avg := 0.0
		if item.labelCount > 0 {
			avg = item.entropySum / float64(item.labelCount)
		}
		if avg < minEntropy {
			continue
		}
		entropyScore := math.Max(0, math.Min(0.6, (avg-minEntropy)*0.25))
		lenScore := math.Max(0, math.Min(0.4, (float64(item.maxLen)-20)/43))
		confidence := math.Max(0, math.Min(1, 0.4+entropyScore+lenScore))
		result.Rows = append(result.Rows, DnsTunnelFinding{
			Domain: domain, QueryCount: item.count, AvgEntropy: avg,
			MaxLabelLen: item.maxLen, Confidence: confidence,
		})
	}
	sort.Slice(result.Rows, func(i, j int) bool {
		if result.Rows[i].Confidence == result.Rows[j].Confidence {
			return result.Rows[i].Domain < result.Rows[j].Domain
		}
		return result.Rows[i].Confidence > result.Rows[j].Confidence
	})
	return result
}

func dnsShannonEntropy(value string) float64 {
	if value == "" {
		return 0
	}
	counts := make(map[rune]int)
	length := 0
	for _, character := range value {
		counts[character]++
		length++
	}
	entropy := 0.0
	for _, count := range counts {
		probability := float64(count) / float64(length)
		entropy -= probability * math.Log2(probability)
	}
	return entropy
}
