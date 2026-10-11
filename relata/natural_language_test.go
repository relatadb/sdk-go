package relata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type naturalLanguageRoundTripper func(*http.Request) (*http.Response, error)

func (f naturalLanguageRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func naturalLanguageJSONResponse(r *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}

func TestQueryInNaturalLanguageUsesVisibleSchemaAndGovernedQuery(t *testing.T) {
	var querySQL string
	httpClient := &http.Client{Transport: naturalLanguageRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/query/schema":
			if r.URL.Query().Get("purpose") != "analytics" {
				t.Errorf("schema purpose = %q", r.URL.Query().Get("purpose"))
			}
			return naturalLanguageJSONResponse(r, `{"purpose":"analytics","object_types":["Person"],"link_types":["Knows"]}`), nil
		case "/query":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			querySQL, _ = body["sql"].(string)
			return naturalLanguageJSONResponse(r, `{"query_id":"q1","rows":[{"name":"Ada","citation":{"chunk_id":"chunk-1","report_id":"doc-1"}}],"columns":["name","citation"]}`), nil
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
	})}
	client := New("http://relata.local", &ClientOptions{DefaultPurpose: "analytics", HTTPClient: httpClient})
	var prompt string
	got, err := client.QueryInNaturalLanguage(context.Background(), "find a person", NaturalLanguageQueryOptions{
		Purpose: "analytics",
		Complete: func(_ context.Context, value string, budget int) (Completion, error) {
			prompt = value
			if budget != 512 {
				t.Fatalf("budget = %d", budget)
			}
			return Completion{Text: "SELECT * FROM Person"}, nil
		},
		Answer: func(_ context.Context, question string, result *QueryResult, budget int) (Completion, error) {
			if question != "find a person" || budget != 512 {
				t.Fatalf("answer callback args question=%q budget=%d", question, budget)
			}
			citation, ok := result.Rows[0]["citation"].(map[string]any)
			if !ok || citation["chunk_id"] != "chunk-1" || citation["report_id"] != "doc-1" {
				t.Fatalf("citation row was not preserved for answer callback: %#v", result.Rows)
			}
			return Completion{Text: "Ada is supported by chunk-1", ModelID: "answer-model"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Visible object types: Person") {
		t.Fatalf("prompt = %s", prompt)
	}
	if querySQL != "SELECT * FROM Person LIMIT 100" || got.Result.RowCount != 1 || got.Answer != "Ada is supported by chunk-1" || got.AnswerModelID != "answer-model" {
		t.Fatalf("sql=%q result=%+v", querySQL, got.Result)
	}
}

func TestQueryInNaturalLanguageProviderFailureAndInvalidSQLDoNotQuery(t *testing.T) {
	queryCalls := 0
	httpClient := &http.Client{Transport: naturalLanguageRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/query/schema" {
			return naturalLanguageJSONResponse(r, `{"purpose":"analytics","object_types":["Person"],"link_types":[]}`), nil
		}
		if r.URL.Path == "/query" {
			queryCalls++
			return naturalLanguageJSONResponse(r, `{"query_id":"unexpected","rows":[]}`), nil
		}
		return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
	})}
	client := New("http://relata.local", &ClientOptions{DefaultPurpose: "analytics", HTTPClient: httpClient})

	_, err := client.QueryInNaturalLanguage(context.Background(), "find a person", NaturalLanguageQueryOptions{
		Purpose: "analytics",
		Complete: func(context.Context, string, int) (Completion, error) {
			return Completion{}, fmt.Errorf("provider unavailable")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("provider error = %v", err)
	}

	for _, sql := range []string{"DELETE FROM Person", "SELECT * FROM Person LIMIT 101", "SELECT * FROM Person; DELETE FROM Person"} {
		_, err := client.QueryInNaturalLanguage(context.Background(), "find a person", NaturalLanguageQueryOptions{
			Purpose: "analytics",
			Complete: func(context.Context, string, int) (Completion, error) {
				return Completion{Text: sql}, nil
			},
		})
		if err == nil {
			t.Errorf("SQL %q unexpectedly accepted", sql)
		}
	}
	if queryCalls != 0 {
		t.Fatalf("governed query called %d times after callback failures/invalid SQL", queryCalls)
	}
}

func TestQueryInNaturalLanguageHonorsPerQueryTimeout(t *testing.T) {
	httpClient := &http.Client{Transport: naturalLanguageRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/query/schema" {
			return naturalLanguageJSONResponse(r, `{"purpose":"analytics","object_types":["Person"],"link_types":[]}`), nil
		}
		if r.URL.Path == "/query" {
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-time.After(100 * time.Millisecond):
				return naturalLanguageJSONResponse(r, `{"query_id":"late","rows":[]}`), nil
			}
		}
		return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
	})}
	client := New("http://relata.local", &ClientOptions{DefaultPurpose: "analytics", Timeout: time.Second, HTTPClient: httpClient})
	_, err := client.QueryInNaturalLanguage(context.Background(), "find a person", NaturalLanguageQueryOptions{
		Purpose: "analytics",
		Timeout: 20 * time.Millisecond,
		Complete: func(context.Context, string, int) (Completion, error) {
			return Completion{Text: "SELECT * FROM Person"}, nil
		},
	})
	if err == nil {
		t.Fatal("expected query timeout")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "deadline") && !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("query error does not report timeout: %v", err)
	}
}
