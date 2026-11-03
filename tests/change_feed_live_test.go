package tests

/**
 * Live change-feed follower for scripts/sdk_change_feed_live.py (#5716). The
 * driver owns the server (it SIGKILLs and restarts it); this test only runs
 * ObjectClient.FollowChanges and appends every delivered event to
 * RELATA_FEED_OUT as one JSON line, until the file named by RELATA_FEED_STOP
 * exists. Skips unless RELATA_TOKEN, RELATA_FEED_OUT and RELATA_FEED_STOP are
 * all set, so `go test ./...` stays green without a server.
 */

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	relata "github.com/relatadb/sdk-go/v3/relata"
)

func TestChangeFeedLive(t *testing.T) {
	token, out, stop := os.Getenv("RELATA_TOKEN"), os.Getenv("RELATA_FEED_OUT"), os.Getenv("RELATA_FEED_STOP")
	if token == "" || out == "" || stop == "" {
		t.Skip("RELATA_TOKEN, RELATA_FEED_OUT and RELATA_FEED_STOP are required")
	}
	host := os.Getenv("RELATA_HOST")
	if host == "" {
		host = "http://localhost:9090"
	}
	c := relata.New(host, &relata.ClientOptions{BearerToken: token, Timeout: 5 * time.Second})
	objects := relata.NewObjectClient(c)

	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(stop); err == nil {
				cancel()
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	pages := 0
	_, err = objects.FollowChanges(ctx, relata.ChangesOptions{Limit: 50}, relata.FollowOptions{
		PollInterval:     200 * time.Millisecond,
		MaxReconnects:    40,
		ReconnectBackoff: 250 * time.Millisecond,
		MaxBackoff:       2 * time.Second,
	}, func(p *relata.ChangesPage) error {
		pages++
		for _, e := range p.Events {
			line, _ := json.Marshal(map[string]any{"eventId": e.EventID, "seq": e.Seq, "id": e.ID})
			if _, err := f.Write(append(line, '\n')); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("follow ended with %v", err)
	}
	if pages == 0 {
		t.Fatal("the follower delivered no page")
	}
}
