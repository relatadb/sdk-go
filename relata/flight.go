package relata

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/apache/arrow/go/v15/arrow"
	"github.com/apache/arrow/go/v15/arrow/array"
	"github.com/apache/arrow/go/v15/arrow/flight"
	"github.com/apache/arrow/go/v15/arrow/ipc"
	"github.com/apache/arrow/go/v15/arrow/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// DefaultFlightPort is the server's default Arrow Flight gRPC port
// (RELATA_FLIGHT_PORT), matching crates/relata-cli/src/flight_server.rs.
const DefaultFlightPort = "8815"

// FlightTicket builds the raw UTF-8 ticket bytes for an Arrow Flight do_get
// call (#2253). The server reads the ticket as SQL and extracts the purpose
// from a leading /* PURPOSE '<id>' */ comment, defaulting to "flight_query".
//
// Pass an empty purpose to omit the comment.
func FlightTicket(sql, purpose string) string {
	if purpose == "" {
		return sql
	}
	escaped := strings.ReplaceAll(purpose, "'", "''")
	return fmt.Sprintf("/* PURPOSE '%s' */ %s", escaped, sql)
}

// ResolveFlightEndpoint derives the Arrow Flight gRPC endpoint from baseURL
// unless flightEndpoint overrides it. The scheme tracks the HTTP base: an
// https:// base defaults to a TLS-protected grpcs://<host>:8815 endpoint, and
// anything else defaults to grpc://<host>:8815 (#3217 — the old unconditional
// grpc:// default shipped the bearer in cleartext even for TLS deployments).
// Pass an explicit grpcs://host:port (or tls://host:port) endpoint to request
// a TLS-protected Flight connection regardless — see isSecureFlightEndpoint
// (#2362).
func ResolveFlightEndpoint(baseURL, flightEndpoint string) string {
	if flightEndpoint != "" {
		return flightEndpoint
	}
	host := "localhost"
	scheme := ""
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Hostname() != "" {
		host = parsed.Hostname()
		scheme = parsed.Scheme
	}
	if scheme == "https" {
		return "grpcs://" + host + ":" + DefaultFlightPort
	}
	return "grpc://" + host + ":" + DefaultFlightPort
}

// isSecureFlightEndpoint reports whether endpoint requests a TLS-protected
// Arrow Flight connection (#2362). The `grpcs://` / `tls://` schemes select
// TLS; `grpc://` (and the legacy bare `http://` form `ResolveFlightEndpoint`
// used to be the only option) remain plaintext — mirroring the scheme-driven
// convention the endpoint string already follows.
func isSecureFlightEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "grpcs://") || strings.HasPrefix(endpoint, "tls://")
}

// stripFlightScheme removes endpoint's scheme prefix (grpc://, grpcs://,
// tls://, http://), leaving the bare host:port grpc.NewClient's target wants.
func stripFlightScheme(endpoint string) string {
	for _, prefix := range []string{"grpcs://", "grpc://", "tls://", "http://"} {
		if strings.HasPrefix(endpoint, prefix) {
			return strings.TrimPrefix(endpoint, prefix)
		}
	}
	return endpoint
}

// flightTransportCredentials selects the gRPC transport credentials for a
// Flight endpoint (#2362): TLS, verified against the system root CA pool,
// for `grpcs://`/`tls://` endpoints; plaintext otherwise. Prior to this fix
// queryFlightDoGet hardcoded insecure.NewCredentials() unconditionally, so
// the Flight bearer token traveled in cleartext on every connection —
// including ones deliberately widened off loopback via RELATA_FLIGHT_BIND.
func flightTransportCredentials(endpoint string) credentials.TransportCredentials {
	if isSecureFlightEndpoint(endpoint) {
		//nolint:gosec // MinVersion is set explicitly below; server cert is
		// verified against the system root CA pool (no InsecureSkipVerify).
		return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	return insecure.NewCredentials()
}

// QueryFlight executes sql against the server's Arrow Flight door (do_get) and
// returns the result as a zero-copy arrow.Table — no JSON intermediate (#2253,
// #958). This mirrors relata-sdk-rust's query_flight.
//
// The Flight door is enabled server-side with RELATA_FLIGHT_ENABLE=true.
// flightEndpoint defaults to grpc://<Client.baseURL host>:8815 (grpcs:// when
// the base URL is https, #3217); pass an explicit grpc://host:port to
// override. The SQL ticket may include a leading /* PURPOSE '<id>' */ comment;
// pass a non-empty purpose to inject one. Bearer auth is sent as gRPC
// "authorization" metadata, using the Client's token unless bearer overrides
// it.
//
// #3217: the bearer is never attached on a plaintext (grpc:// / http://)
// endpoint unless ClientOptions.AllowCleartextBearer is set — otherwise the
// call fails closed with ErrCleartextBearerDisallowed before any dial.
//
// Requires github.com/apache/arrow/go/v15 + google.golang.org/grpc (see go.mod).
// Run `go mod tidy && go build ./...` after adding this file to fetch deps.
func (c *Client) QueryFlight(
	ctx context.Context,
	sql string,
	flightEndpoint string,
	purpose string,
	bearer string,
) (arrow.Table, error) {
	endpoint := ResolveFlightEndpoint(c.baseURL, flightEndpoint)
	ticketSQL := FlightTicket(sql, purpose)
	if bearer == "" {
		bearer = c.bearerToken
	}
	if bearer != "" && !isSecureFlightEndpoint(endpoint) && !c.allowCleartextBearer {
		return nil, ErrCleartextBearerDisallowed
	}
	return queryFlightDoGet(ctx, endpoint, ticketSQL, bearer)
}

// queryFlightDoGet opens a gRPC Flight client, sends DoGet with the ticket, and
// decodes the FlightData stream into an arrow.Table by reconstructing the IPC
// streaming framing (each FlightData carries one IPC Message header + body).
//
// #2362: the transport is TLS-protected whenever endpoint uses the
// `grpcs://`/`tls://` scheme (see flightTransportCredentials) — required
// because the bearer token below travels as cleartext gRPC metadata over
// whatever transport is selected here.
func queryFlightDoGet(ctx context.Context, endpoint, ticketSQL, bearer string) (arrow.Table, error) {
	return queryFlightDoGetWithDialOpts(ctx, endpoint, ticketSQL, bearer)
}

// queryFlightDoGetWithDialOpts is queryFlightDoGet's testable seam (#2758):
// extraDialOpts are appended after the transport-credentials option, letting
// a test inject grpc.WithContextDialer(bufconn.Listener.DialContext) to run
// the real dial → DoGet → IPC-reconstruction path in-process against a fake
// Arrow Flight gRPC service (google.golang.org/grpc/test/bufconn), with no
// live server required. Production callers (queryFlightDoGet, and therefore
// Client.QueryFlight) never pass extraDialOpts, so this is purely additive —
// the real dial path is unaffected.
func queryFlightDoGetWithDialOpts(ctx context.Context, endpoint, ticketSQL, bearer string, extraDialOpts ...grpc.DialOption) (arrow.Table, error) {
	target := stripFlightScheme(endpoint)
	dialOpts := append(
		[]grpc.DialOption{grpc.WithTransportCredentials(flightTransportCredentials(endpoint))},
		extraDialOpts...,
	)
	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("relata flight: dial %s: %w", endpoint, err)
	}
	defer conn.Close()

	fc := flight.NewFlightServiceClient(conn)
	if bearer != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+bearer)
	}

	stream, err := fc.DoGet(ctx, &flight.Ticket{Ticket: []byte(ticketSQL)})
	if err != nil {
		return nil, fmt.Errorf("relata flight: do_get: %w", err)
	}

	// Reassemble the IPC stream: per the Arrow Flight gRPC proto, each
	// FlightData.DataHeader is one IPC Message flatbuf and DataBody is the
	// record-batch body. IPC streaming frames are <continuation 0xFFFFFFFF>
	// <u32 len><header><body>.
	var buf bytes.Buffer
	for {
		fd, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, fmt.Errorf("relata flight: recv: %w", recvErr)
		}
		header := fd.GetDataHeader()
		body := fd.GetDataBody()
		if len(header) == 0 {
			continue
		}
		writeIPCFrame(&buf, header, body)
	}

	rdr, err := ipc.NewReader(&buf, ipc.WithAllocator(memory.DefaultAllocator))
	if err != nil {
		return nil, fmt.Errorf("relata flight: decode ipc stream: %w", err)
	}
	defer rdr.Release()

	var records []arrow.Record
	for {
		rec, readErr := rdr.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("relata flight: read record: %w", readErr)
		}
		rec.Retain()
		records = append(records, rec)
	}
	table := array.NewTableFromRecords(rdr.Schema(), records)
	for _, rec := range records {
		rec.Release()
	}
	return table, nil
}

// writeIPCFrame appends one IPC streaming frame (continuation + length +
// header + body) to buf.
func writeIPCFrame(buf *bytes.Buffer, header, body []byte) {
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], 0xFFFFFFFF)
	buf.Write(lenBuf[:])
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(header)))
	buf.Write(lenBuf[:])
	buf.Write(header)
	if len(body) > 0 {
		buf.Write(body)
	}
}
