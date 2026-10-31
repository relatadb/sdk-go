package relata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestChangesSendsQueryAndDecodesPage(t *testing.T) {
	oc, seen := conditionalServer(t, 200, `{"feedId":"f","events":[{"eventId":"f:3","seq":3,"kind":"delete","type":"Doc","id":"a","version":9,"commitId":"c","opIndex":0,"committedAtNs":1}],"nextCursor":"f:3","hasMore":false,"head":3,"withheld":0}`)
	page, err := oc.Changes(context.Background(), ChangesOptions{Cursor: "f:2", Limit: 50, Type: "Doc"})
	if err != nil {
		t.Fatal(err)
	}
	if seen.method != "GET" || seen.path != "/object-changes" || seen.query != "cursor=f%3A2&limit=50&type=Doc" {
		t.Fatalf("request = %s %s?%s", seen.method, seen.path, seen.query)
	}
	if page.NextCursor != "f:3" || len(page.Events) != 1 || page.Events[0].Kind != "delete" || page.Events[0].Version != 9 {
		t.Fatalf("page = %+v", page)
	}
}

func TestChangesWithoutOptionsHasNoQuery(t *testing.T) {
	oc, seen := conditionalServer(t, 200, `{"feedId":"f","events":[],"nextCursor":"f:0","hasMore":false,"head":0,"withheld":0}`)
	if _, err := oc.Changes(context.Background(), ChangesOptions{}); err != nil {
		t.Fatal(err)
	}
	if seen.query != "" {
		t.Fatalf("query = %q", seen.query)
	}
}

func TestChangePagesFollowsHasMoreAndReturnsResumeCursor(t *testing.T) {
	oc, seen := conditionalServer(t, 200, `{"feedId":"f","events":[{"eventId":"f:1","seq":1,"kind":"create","entity":"link","type":"KNOWS","id":"a->b","sourceId":"a","targetId":"b","version":0,"commitId":"c","opIndex":0,"committedAtNs":1}],"nextCursor":"f:1","hasMore":false,"head":1,"withheld":0}`)
	var got []ChangeEvent
	cursor, err := oc.ChangePages(context.Background(), ChangesOptions{Cursor: "f:0"}, func(p *ChangesPage) error {
		got = append(got, p.Events...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "f:1" || len(got) != 1 || got[0].Entity != "link" || got[0].SourceID != "a" || got[0].TargetID != "b" {
		t.Fatalf("cursor=%q events=%+v", cursor, got)
	}
	if seen.query != "cursor=f%3A0" {
		t.Fatalf("query = %q", seen.query)
	}
}

func followServer(t *testing.T, script []int) (*ObjectClient, *[]string) {
	t.Helper()
	var mu sync.Mutex
	cursors := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		cur := r.URL.Query().Get("cursor")
		cursors = append(cursors, cur)
		w.Header().Set("Content-Type", "application/json")
		if cur == "f:1" && len(script) > 0 {
			status := script[0]
			script = script[1:]
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"detail":"x"}`))
			return
		}
		if cur == "" {
			_, _ = w.Write([]byte(`{"feedId":"f","events":[{"eventId":"f:1"}],"nextCursor":"f:1","hasMore":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"feedId":"f","events":[{"eventId":"f:2"}],"nextCursor":"f:2","hasMore":false}`))
	}))
	t.Cleanup(srv.Close)
	return NewObjectClient(New(srv.URL, &ClientOptions{BearerToken: "t"})), &cursors
}

func TestFollowChangesReconnectsAndResumesFromTheLastCursor(t *testing.T) {
	oc, cursors := followServer(t, []int{503, 502})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ids []string
	last, err := oc.FollowChanges(ctx, ChangesOptions{}, FollowOptions{PollInterval: time.Millisecond, ReconnectBackoff: time.Millisecond}, func(p *ChangesPage) error {
		ids = append(ids, p.Events[0].EventID)
		if len(ids) == 2 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(ids) != 2 || ids[0] != "f:1" || ids[1] != "f:2" || last != "f:2" {
		t.Fatalf("ids = %v last = %q", ids, last)
	}
	want := []string{"", "f:1", "f:1", "f:1"}
	for i, w := range want {
		if i >= len(*cursors) || (*cursors)[i] != w {
			t.Fatalf("cursors = %v want prefix %v", *cursors, want)
		}
	}
}

func TestFollowChangesGivesUpAndDoesNotRetryAResyncError(t *testing.T) {
	oc, cursors := followServer(t, []int{503, 503, 503})
	_, err := oc.FollowChanges(context.Background(), ChangesOptions{}, FollowOptions{MaxReconnects: 2, ReconnectBackoff: time.Millisecond, PollInterval: time.Millisecond}, func(*ChangesPage) error { return nil })
	if err == nil || len(*cursors) != 4 {
		t.Fatalf("err = %v cursors = %v", err, *cursors)
	}
	oc2, cursors2 := followServer(t, []int{410})
	_, err = oc2.FollowChanges(context.Background(), ChangesOptions{}, FollowOptions{ReconnectBackoff: time.Millisecond, PollInterval: time.Millisecond}, func(*ChangesPage) error { return nil })
	var re *RelataError
	if !errors.As(err, &re) || re.StatusCode != 410 || len(*cursors2) != 2 {
		t.Fatalf("err = %v cursors = %v", err, *cursors2)
	}
}
