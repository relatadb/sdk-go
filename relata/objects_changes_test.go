package relata

import (
	"context"
	"testing"
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
