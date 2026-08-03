package relata

import (
	"context"
	"net"
	"testing"

	"github.com/apache/arrow/go/v15/arrow"
	"github.com/apache/arrow/go/v15/arrow/array"
	"github.com/apache/arrow/go/v15/arrow/flight"
	"github.com/apache/arrow/go/v15/arrow/ipc"
	"github.com/apache/arrow/go/v15/arrow/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// #2758: this is the real, in-process Arrow Flight `bufconn` test a prior
// pass investigated and deliberately deferred (it could not be verified via
// `go test` at the time). google.golang.org/grpc/test/bufconn ships as part
// of google.golang.org/grpc itself (already a direct dependency, see go.mod)
// — no new module was added. queryFlightDoGetWithDialOpts (flight.go) is the
// small, additive testable seam this test needed: it lets the test inject
// grpc.WithContextDialer(listener.DialContext) so the *real* dial → DoGet →
// IPC-stream-reconstruction code path in queryFlightDoGet runs against a fake
// FlightServiceServer, entirely in-process.

// bufconnFlightServer is a minimal fake Arrow Flight service: DoGet always
// returns one record batch (a 3-row {id int64, name utf8} table) regardless
// of the ticket, but records the ticket bytes and incoming bearer metadata so
// the test can assert both the request the SDK sent and the table it
// decoded.
type bufconnFlightServer struct {
	flight.BaseFlightServer

	gotTicket    string
	gotAuth      string
	gotTenant    string
	gotActingAs  string
	gotDelegated string
	gotRequestID string
}

func (s *bufconnFlightServer) DoGet(tkt *flight.Ticket, fs flight.FlightService_DoGetServer) error {
	s.gotTicket = string(tkt.GetTicket())
	if md, ok := metadata.FromIncomingContext(fs.Context()); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			s.gotAuth = vals[0]
		}
		if vals := md.Get("x-relata-tenant-id"); len(vals) > 0 {
			s.gotTenant = vals[0]
		}
		if vals := md.Get("x-acting-as"); len(vals) > 0 {
			s.gotActingAs = vals[0]
		}
		if vals := md.Get("x-delegated-by"); len(vals) > 0 {
			s.gotDelegated = vals[0]
		}
		if vals := md.Get("x-request-id"); len(vals) > 0 {
			s.gotRequestID = vals[0]
		}
	}

	mem := memory.NewGoAllocator()
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64},
		{Name: "name", Type: arrow.BinaryTypes.String},
	}, nil)

	bldr := array.NewRecordBuilder(mem, schema)
	defer bldr.Release()
	bldr.Field(0).(*array.Int64Builder).AppendValues([]int64{1, 2, 3}, nil)
	bldr.Field(1).(*array.StringBuilder).AppendValues([]string{"a", "b", "c"}, nil)
	rec := bldr.NewRecord()
	defer rec.Release()

	w := flight.NewRecordWriter(fs, ipc.WithSchema(schema))
	return w.Write(rec)
}

// dialer returns a grpc.WithContextDialer-compatible func backed by an
// in-memory bufconn.Listener serving srv.
func startBufconnFlightServer(t *testing.T, srv *bufconnFlightServer) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	const bufSize = 1 << 20
	lis := bufconn.Listen(bufSize)

	gs := grpc.NewServer()
	flight.RegisterFlightServiceServer(gs, srv)
	go func() {
		_ = gs.Serve(lis)
	}()
	t.Cleanup(gs.Stop)

	return func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
}

// TestQueryFlightDoGetOverBufconn exercises the real queryFlightDoGet path —
// ticket construction, gRPC DoGet, and Arrow IPC stream reconstruction into
// an arrow.Table — against an in-process fake Flight server, with no live
// server required (#2758).
func TestQueryFlightDoGetOverBufconn(t *testing.T) {
	srv := &bufconnFlightServer{}
	dialer := startBufconnFlightServer(t, srv)

	ticketSQL := FlightTicket("SELECT * FROM Person LIMIT 3", "investigation")

	table, err := queryFlightDoGetWithDialOpts(
		context.Background(),
		"passthrough:///bufnet",
		ticketSQL,
		"test-bearer-token",
		nil,
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryFlightDoGetWithDialOpts: %v", err)
	}
	defer table.Release()

	// The ticket the fake server received must be exactly what FlightTicket
	// built — the PURPOSE comment plus SQL.
	if srv.gotTicket != ticketSQL {
		t.Fatalf("server received ticket %q, want %q", srv.gotTicket, ticketSQL)
	}
	// Bearer auth must travel as gRPC "authorization" metadata (#2362).
	if want := "Bearer test-bearer-token"; srv.gotAuth != want {
		t.Fatalf("server received authorization %q, want %q", srv.gotAuth, want)
	}

	if got, want := table.NumRows(), int64(3); got != want {
		t.Fatalf("table.NumRows() = %d, want %d", got, want)
	}
	if got, want := table.NumCols(), int64(2); got != want {
		t.Fatalf("table.NumCols() = %d, want %d", got, want)
	}
	if got, want := table.Schema().Field(0).Name, "id"; got != want {
		t.Fatalf("column 0 name = %q, want %q", got, want)
	}
	if got, want := table.Schema().Field(1).Name, "name"; got != want {
		t.Fatalf("column 1 name = %q, want %q", got, want)
	}
}

// TestQueryFlightDoGetOverBufconnDialOptsAdditive confirms the transport
// credentials selected via flightTransportCredentials still take effect
// before extraDialOpts are appended — i.e. queryFlightDoGetWithDialOpts is
// purely additive over queryFlightDoGet, not a replacement of its transport
// selection. A plain grpc:// endpoint still selects insecure credentials
// (matching TestFlightTransportCredentialsPlaintextForGrpcScheme), and the
// bufconn dialer is what actually lets the in-memory connection succeed.
func TestQueryFlightDoGetOverBufconnDialOptsAdditive(t *testing.T) {
	srv := &bufconnFlightServer{}
	dialer := startBufconnFlightServer(t, srv)

	// "grpc://" is stripped down to the bare passthrough target by
	// stripFlightScheme, exercising flightTransportCredentials' insecure
	// branch exactly as queryFlightDoGet's production path does.
	table, err := queryFlightDoGetWithDialOpts(
		context.Background(),
		"grpc://passthrough:///bufnet",
		FlightTicket("SELECT 1", ""),
		"",
		nil,
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryFlightDoGetWithDialOpts: %v", err)
	}
	defer table.Release()

	if srv.gotAuth != "" {
		t.Fatalf("expected no authorization metadata when bearer is empty, got %q", srv.gotAuth)
	}
	if got, want := table.NumRows(), int64(3); got != want {
		t.Fatalf("table.NumRows() = %d, want %d", got, want)
	}
}

// TestQueryFlightDoGetForwardsTenantMetadata verifies the tenant / acting-as /
// delegated-by / request-id headers travel as gRPC metadata on the Flight door
// (#3213) — without them the server falls back to default-tenant resolution.
func TestQueryFlightDoGetForwardsTenantMetadata(t *testing.T) {
	srv := &bufconnFlightServer{}
	dialer := startBufconnFlightServer(t, srv)

	meta := map[string]string{
		"x-relata-tenant-id": "org-acme",
		"x-acting-as":        "alice",
		"x-delegated-by":     "root-admin",
		"x-request-id":       "req-123",
	}
	table, err := queryFlightDoGetWithDialOpts(
		context.Background(),
		"passthrough:///bufnet",
		FlightTicket("SELECT 1", ""),
		"",
		meta,
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryFlightDoGetWithDialOpts: %v", err)
	}
	defer table.Release()

	if srv.gotTenant != "org-acme" {
		t.Fatalf("x-relata-tenant-id = %q, want org-acme", srv.gotTenant)
	}
	if srv.gotActingAs != "alice" {
		t.Fatalf("x-acting-as = %q, want alice", srv.gotActingAs)
	}
	if srv.gotDelegated != "root-admin" {
		t.Fatalf("x-delegated-by = %q, want root-admin", srv.gotDelegated)
	}
	if srv.gotRequestID != "req-123" {
		t.Fatalf("x-request-id = %q, want req-123", srv.gotRequestID)
	}
}

// TestFlightMetadataBuilder checks Client.flightMetadata emits the tenant /
// delegation headers plus a per-request X-Request-ID (#3213).
func TestFlightMetadataBuilder(t *testing.T) {
	c := New("http://localhost:9090", &ClientOptions{
		Tenant:      "org-acme",
		ActingAs:    "alice",
		DelegatedBy: "root-admin",
	})
	meta := c.flightMetadata()
	if meta["x-relata-tenant-id"] != "org-acme" {
		t.Fatalf("tenant = %q", meta["x-relata-tenant-id"])
	}
	if meta["x-acting-as"] != "alice" {
		t.Fatalf("acting-as = %q", meta["x-acting-as"])
	}
	if meta["x-delegated-by"] != "root-admin" {
		t.Fatalf("delegated-by = %q", meta["x-delegated-by"])
	}
	if meta["x-request-id"] == "" {
		t.Fatal("expected a per-request X-Request-ID")
	}
}
