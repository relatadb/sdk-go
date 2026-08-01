package relata

import (
	"bytes"
	"context"
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
// (grpc://<host>:8815 by default) unless flightEndpoint overrides it.
func ResolveFlightEndpoint(baseURL, flightEndpoint string) string {
	if flightEndpoint != "" {
		return flightEndpoint
	}
	host := "localhost"
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Hostname() != "" {
		host = parsed.Hostname()
	}
	return "grpc://" + host + ":" + DefaultFlightPort
}

// QueryFlight executes sql against the server's Arrow Flight door (do_get) and
// returns the result as a zero-copy arrow.Table — no JSON intermediate (#2253,
// #958). This mirrors relata-sdk-rust's query_flight.
//
// The Flight door is enabled server-side with RELATA_FLIGHT_ENABLE=true.
// flightEndpoint defaults to grpc://<Client.baseURL host>:8815; pass an explicit
// grpc://host:port to override. The SQL ticket may include a leading
// /* PURPOSE '<id>' */ comment; pass a non-empty purpose to inject one.
// Bearer auth is sent as gRPC "authorization" metadata, using the Client's
// token unless bearer overrides it.
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
	return queryFlightDoGet(ctx, endpoint, ticketSQL, bearer)
}

// queryFlightDoGet opens a gRPC Flight client, sends DoGet with the ticket, and
// decodes the FlightData stream into an arrow.Table by reconstructing the IPC
// streaming framing (each FlightData carries one IPC Message header + body).
func queryFlightDoGet(ctx context.Context, endpoint, ticketSQL, bearer string) (arrow.Table, error) {
	target := strings.TrimPrefix(strings.TrimPrefix(endpoint, "grpc://"), "http://")
	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
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
	table, err := array.NewTableFromRecords(rdr.Schema(), records)
	if err != nil {
		return nil, fmt.Errorf("relata flight: build table: %w", err)
	}
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
