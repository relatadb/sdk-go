package relata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListCheckpointsKeepsStringIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"thread_id":"t","checkpoints":["a","b"]}`))
	}))
	defer srv.Close()
	got, err := NewA2AClient(New(srv.URL, &ClientOptions{BearerToken: "x"})).ListCheckpoints(context.Background(), "t")
	if err != nil || len(got) != 2 || got[0]["checkpoint_id"] != "a" || got[1]["checkpoint_id"] != "b" {
		t.Fatalf("got %v err %v", got, err)
	}
}
