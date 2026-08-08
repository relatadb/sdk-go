package relata

// First-party gRPC query client with Arrow-IPC `RowBatch` opt-in (#4090
// round 2 — Go SDK). Follow-on to the TypeScript SDK's `grpc-query.ts`
// (#4090 round 1, PR #4095): PR #4086 (epic #3879 Wave 6, #3912/#4020 item 2)
// landed the server-side wire encoding for a negotiated Arrow-IPC row
// encoding on the plain gRPC `RelataQuery` service
// (`proto/relata.proto`'s `QueryRequest.wants_arrow_ipc_rows` /
// `QueryResponse.arrow_ipc_rows` / `RowBatch.arrow_ipc_rows`,
// `crates/relata-server/src/grpc.rs::encode_rows_arrow_ipc_or_json`),
// mirroring ADR-0255's own `ShardQueryRequest`/`ShardQueryResponse`
// negotiated-capability pattern. Round 1 found Python has zero gRPC
// scaffolding and TypeScript already had exactly the needed pieces
// (`@grpc/grpc-js` + `apache-arrow` peer deps, a hand-rolled protobuf wire
// codec in `flight.ts`, #2492) so it opted-in there first. Go SDK
// investigation confirmed the same shape of opportunity: `flight.go` already
// ships `google.golang.org/grpc` + `github.com/apache/arrow/go/v15` as
// direct dependencies (go.mod) for the Arrow Flight door, but no generated
// `.pb.go` stubs exist for the plain `RelataQuery` service — this file is
// the Go equivalent of `grpc-query.ts`'s "no proto-loader, no bundled
// .proto asset" approach: a small hand-rolled protobuf codec for
// `QueryRequest`/`QueryResponse` (every field a scalar or
// `repeated string`/`bytes` — no nested messages, no maps, no oneofs) driven
// over `google.golang.org/grpc`'s generic `ClientConn.Invoke` via a
// pass-through `encoding.Codec` (`grpc.ForceCodec`), instead of
// `apache/arrow/go/v15/arrow/flight`'s generated Flight service client.
//
// Both `RelataQuery` RPCs that make sense for a wants_arrow_ipc_rows caller
// are wired: the unary `Execute` (Client.QueryGrpc) and the server-streaming
// `ExecuteStream` (Client.QueryGrpcStream, #4090 round 7 — mirroring
// TypeScript's `GrpcQueryTransport.executeStream`, PR #4228).
// `ExecuteStream` negotiates the Arrow-IPC encoding per streamed frame
// (`RowBatch.arrow_ipc_rows`) and `RowBatch`'s field layout differs from
// `QueryResponse`'s — see decodeRowBatch. Both normalise to the same
// *QueryResult shape.
import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/apache/arrow/go/v15/arrow/ipc"
	"github.com/apache/arrow/go/v15/arrow/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// DefaultGrpcQueryPort is the server's default plain gRPC port
// (RELATA_GRPC_PORT), matching crates/relata-cli/src/main.rs. Distinct from
// DefaultFlightPort (flight.go) — the plain `RelataQuery` service and the
// Arrow Flight door are two different gRPC endpoints.
const DefaultGrpcQueryPort = "50051"

// ResolveGrpcQueryEndpoint derives the plain gRPC `RelataQuery` endpoint from
// baseURL unless grpcEndpoint overrides it. Same scheme convention as
// ResolveFlightEndpoint (flight.go, #3217): an https:// base defaults to a
// TLS-protected grpcs://<host>:50051 endpoint, anything else defaults to
// grpc://<host>:50051. Pass an explicit grpcs://host:port (or
// tls://host:port) to request TLS regardless.
func ResolveGrpcQueryEndpoint(baseURL, grpcEndpoint string) string {
	if grpcEndpoint != "" {
		return grpcEndpoint
	}
	host := "localhost"
	scheme := ""
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Hostname() != "" {
		host = parsed.Hostname()
		scheme = parsed.Scheme
	}
	if scheme == "https" {
		return "grpcs://" + host + ":" + DefaultGrpcQueryPort
	}
	return "grpc://" + host + ":" + DefaultGrpcQueryPort
}

// ---------------------------------------------------------------------------
// Minimal protobuf codec for QueryRequest / QueryResponse (proto/relata.proto)
// — mirrors grpc-query.ts's hand-rolled encode/decode. Tag varints reuse
// encoding/binary's LEB128 (Uvarint/PutUvarint), which is bit-for-bit the
// same encoding protobuf varints use.
// ---------------------------------------------------------------------------

func protoTag(field int, wireType uint64) uint64 {
	return uint64(field)<<3 | wireType
}

func writeProtoVarint(buf *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	buf.Write(tmp[:n])
}

func writeProtoString(buf *bytes.Buffer, field int, s string) {
	if s == "" {
		return
	}
	writeProtoVarint(buf, protoTag(field, 2))
	writeProtoVarint(buf, uint64(len(s)))
	buf.WriteString(s)
}

// encodeQueryRequest encodes a `QueryRequest` (`proto/relata.proto`):
// purpose (1), sql (2), bearer_token (3, unused server-side today — auth
// travels as gRPC "authorization" metadata, same as QueryFlight), and
// wants_arrow_ipc_rows (4, bool varint — only emitted when true, since
// proto3 elides default `false`).
func encodeQueryRequest(purpose, sql, bearerToken string, wantsArrowIPCRows bool) []byte {
	var buf bytes.Buffer
	writeProtoString(&buf, 1, purpose)
	writeProtoString(&buf, 2, sql)
	writeProtoString(&buf, 3, bearerToken)
	if wantsArrowIPCRows {
		writeProtoVarint(&buf, protoTag(4, 0))
		writeProtoVarint(&buf, 1)
	}
	return buf.Bytes()
}

// decodedQueryResponse is the decoded shape of a `QueryResponse`
// (`proto/relata.proto`). arrowIPCRows empty means "read rowsJSON instead"
// per the proto's documented fallback contract — never "zero rows."
type decodedQueryResponse struct {
	columns      []string
	rowCount     int64
	rowsJSON     []string
	arrowIPCRows []byte
}

// decodeQueryResponse decodes a `QueryResponse` protobuf: columns (1,
// repeated string), row_count (2, int64 varint), rows_json (3, repeated
// string), arrow_ipc_rows (4, bytes). Unknown fields/wire types are skipped
// where the length is knowable, so the decoder stays forward-compatible with
// future response fields (mirrors decodeQueryResponse in grpc-query.ts).
func decodeQueryResponse(buf []byte) (decodedQueryResponse, error) {
	var out decodedQueryResponse
	i := 0
	for i < len(buf) {
		tag, n := binary.Uvarint(buf[i:])
		if n <= 0 {
			return out, fmt.Errorf("relata grpc query: truncated protobuf tag in QueryResponse")
		}
		i += n
		field := int(tag >> 3)
		wireType := tag & 0x7
		switch wireType {
		case 0: // varint
			v, n := binary.Uvarint(buf[i:])
			if n <= 0 {
				return out, fmt.Errorf("relata grpc query: truncated protobuf varint in QueryResponse")
			}
			i += n
			if field == 2 {
				out.rowCount = int64(v)
			}
		case 2: // length-delimited
			l, n := binary.Uvarint(buf[i:])
			if n <= 0 {
				return out, fmt.Errorf("relata grpc query: truncated protobuf length in QueryResponse")
			}
			i += n
			end := i + int(l)
			if l > uint64(len(buf)) || end > len(buf) || end < i {
				return out, fmt.Errorf("relata grpc query: truncated protobuf payload in QueryResponse")
			}
			seg := buf[i:end]
			i = end
			switch field {
			case 1:
				out.columns = append(out.columns, string(seg))
			case 3:
				out.rowsJSON = append(out.rowsJSON, string(seg))
			case 4:
				out.arrowIPCRows = seg
			}
		case 1: // fixed64 (unused by QueryResponse today — skip for forward-compat)
			if i+8 > len(buf) {
				return out, fmt.Errorf("relata grpc query: truncated fixed64 in QueryResponse")
			}
			i += 8
		case 5: // fixed32 (unused by QueryResponse today — skip for forward-compat)
			if i+4 > len(buf) {
				return out, fmt.Errorf("relata grpc query: truncated fixed32 in QueryResponse")
			}
			i += 4
		default:
			return out, fmt.Errorf("relata grpc query: unsupported protobuf wire type %d in QueryResponse", wireType)
		}
	}
	return out, nil
}

// toQueryResult decodes into the same *QueryResult shape Query/QueryFlight
// return, so callers never need to know which wire encoding the server
// chose — decoding from arrowIPCRows when present, else parsing each
// rowsJSON element.
func (d decodedQueryResponse) toQueryResult() (*QueryResult, error) {
	var rows []map[string]any
	if len(d.arrowIPCRows) > 0 {
		var err error
		rows, err = arrowIPCRowsToObjects(d.arrowIPCRows)
		if err != nil {
			return nil, err
		}
	} else {
		rows = make([]map[string]any, 0, len(d.rowsJSON))
		for _, s := range d.rowsJSON {
			var row map[string]any
			if err := json.Unmarshal([]byte(s), &row); err != nil {
				return nil, fmt.Errorf("relata grpc query: decode rows_json row: %w", err)
			}
			rows = append(rows, row)
		}
	}
	columns := d.columns
	if columns == nil {
		columns = []string{}
	}
	return &QueryResult{Rows: rows, RowCount: len(rows), Columns: columns}, nil
}

// decodedRowBatch is the decoded shape of one `RowBatch` frame of a streamed
// `RelataQuery.ExecuteStream` response (`proto/relata.proto`). The field
// layout is NOT the same as `QueryResponse`: columns (1, repeated string),
// rows_json (2, repeated string -- field 2 here, field 3 on QueryResponse),
// done (3, bool varint -- QueryResponse has no equivalent), arrow_ipc_rows
// (4, bytes, same field number and fallback contract as QueryResponse).
// There is no row_count field -- the caller totals the frames it received.
type decodedRowBatch struct {
	columns      []string
	rowsJSON     []string
	done         bool
	arrowIPCRows []byte
}

// decodeRowBatch decodes a single `RowBatch` protobuf frame. Unknown
// fields/wire types are skipped where the length is knowable, so the decoder
// stays forward-compatible with future frame fields (mirrors
// decodeQueryResponse, and decodeRowBatch in grpc-query.ts).
func decodeRowBatch(buf []byte) (decodedRowBatch, error) {
	var out decodedRowBatch
	i := 0
	for i < len(buf) {
		tag, n := binary.Uvarint(buf[i:])
		if n <= 0 {
			return out, fmt.Errorf("relata grpc query: truncated protobuf tag in RowBatch")
		}
		i += n
		field := int(tag >> 3)
		wireType := tag & 0x7
		switch wireType {
		case 0: // varint
			v, n := binary.Uvarint(buf[i:])
			if n <= 0 {
				return out, fmt.Errorf("relata grpc query: truncated protobuf varint in RowBatch")
			}
			i += n
			if field == 3 {
				out.done = v != 0
			}
		case 2: // length-delimited
			l, n := binary.Uvarint(buf[i:])
			if n <= 0 {
				return out, fmt.Errorf("relata grpc query: truncated protobuf length in RowBatch")
			}
			i += n
			end := i + int(l)
			if l > uint64(len(buf)) || end > len(buf) || end < i {
				return out, fmt.Errorf("relata grpc query: truncated protobuf payload in RowBatch")
			}
			seg := buf[i:end]
			i = end
			switch field {
			case 1:
				out.columns = append(out.columns, string(seg))
			case 2:
				out.rowsJSON = append(out.rowsJSON, string(seg))
			case 4:
				out.arrowIPCRows = seg
			}
		case 1: // fixed64 (unused by RowBatch today -- skip for forward-compat)
			if i+8 > len(buf) {
				return out, fmt.Errorf("relata grpc query: truncated fixed64 in RowBatch")
			}
			i += 8
		case 5: // fixed32 (unused by RowBatch today -- skip for forward-compat)
			if i+4 > len(buf) {
				return out, fmt.Errorf("relata grpc query: truncated fixed32 in RowBatch")
			}
			i += 4
		default:
			return out, fmt.Errorf("relata grpc query: unsupported protobuf wire type %d in RowBatch", wireType)
		}
	}
	return out, nil
}

// rows decodes this frame's payload into plain row maps, honouring the same
// "non-empty arrow_ipc_rows wins, empty means read rows_json" contract
// decodedQueryResponse.toQueryResult applies to a whole response -- here
// applied per streamed frame.
func (d decodedRowBatch) rows() ([]map[string]any, error) {
	if len(d.arrowIPCRows) > 0 {
		return arrowIPCRowsToObjects(d.arrowIPCRows)
	}
	rows := make([]map[string]any, 0, len(d.rowsJSON))
	for _, s := range d.rowsJSON {
		var row map[string]any
		if err := json.Unmarshal([]byte(s), &row); err != nil {
			return nil, fmt.Errorf("relata grpc query: decode RowBatch rows_json row: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// arrowIPCRowsToObjects converts a self-contained Arrow IPC stream (schema +
// one RecordBatch, per QueryResponse.arrow_ipc_rows's documented shape,
// crates/relata-cluster/src/transport.rs::encode_shard_rows_arrow_ipc) into
// plain row maps — the same shape json.Unmarshal(rowsJSON[i]) would produce.
func arrowIPCRowsToObjects(data []byte) ([]map[string]any, error) {
	rdr, err := ipc.NewReader(bytes.NewReader(data), ipc.WithAllocator(memory.DefaultAllocator))
	if err != nil {
		return nil, fmt.Errorf("relata grpc query: decode arrow_ipc_rows: %w", err)
	}
	defer rdr.Release()

	fields := rdr.Schema().Fields()
	fieldNames := make([]string, len(fields))
	for i, f := range fields {
		fieldNames[i] = f.Name
	}

	rows := make([]map[string]any, 0)
	for {
		rec, readErr := rdr.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("relata grpc query: read arrow_ipc_rows record: %w", readErr)
		}
		for r := 0; r < int(rec.NumRows()); r++ {
			row := make(map[string]any, len(fieldNames))
			for c, name := range fieldNames {
				row[name] = rec.Column(c).GetOneForMarshal(r)
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// rawBytesCodec — a pass-through grpc encoding.Codec that transmits
// already-protobuf-encoded []byte payloads verbatim, without depending on
// generated .proto stubs. Named "proto" so the content-subtype it selects
// (grpc.ForceCodec derives content-subtype from Name()) matches the standard
// "application/grpc+proto" a real protobuf client sends — the server sees
// wire-identical traffic; only the client-side marshal/unmarshal step is
// hand-rolled instead of generated. Used only as a per-call grpc.ForceCodec
// CallOption; never globally registered via encoding.RegisterCodec.
// ---------------------------------------------------------------------------

type rawBytesCodec struct{}

func (rawBytesCodec) Marshal(v any) ([]byte, error) {
	b, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("relata grpc query: rawBytesCodec.Marshal: expected []byte, got %T", v)
	}
	return b, nil
}

func (rawBytesCodec) Unmarshal(data []byte, v any) error {
	p, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("relata grpc query: rawBytesCodec.Unmarshal: expected *[]byte, got %T", v)
	}
	*p = append((*p)[:0], data...)
	return nil
}

func (rawBytesCodec) Name() string { return "proto" }

// queryGrpcExecute opens a gRPC connection to endpoint and drives the unary
// `RelataQuery/Execute` RPC, opting into wants_arrow_ipc_rows.
//
// #2362-equivalent: the transport is TLS-protected whenever endpoint uses
// the grpcs:///tls:// scheme (see flightTransportCredentials, reused as-is —
// its scheme-driven TLS-selection logic is not Flight-specific).
//
// meta carries the tenant/delegation/correlation headers as gRPC metadata,
// same convention as QueryFlight's flightMetadata (#3213).
func queryGrpcExecute(ctx context.Context, endpoint, purpose, sql, bearer string, meta map[string]string) (decodedQueryResponse, error) {
	return queryGrpcExecuteWithDialOpts(ctx, endpoint, purpose, sql, bearer, meta)
}

// queryGrpcExecuteWithDialOpts is queryGrpcExecute's testable seam (mirrors
// queryFlightDoGetWithDialOpts, #2758): extraDialOpts are appended after the
// transport-credentials option, letting a test inject
// grpc.WithContextDialer(bufconn.Listener.DialContext) to run the real
// dial -> Invoke -> decode path in-process against a fake RelataQuery gRPC
// service, with no live server required. Production callers never pass
// extraDialOpts.
func queryGrpcExecuteWithDialOpts(
	ctx context.Context,
	endpoint, purpose, sql, bearer string,
	meta map[string]string,
	extraDialOpts ...grpc.DialOption,
) (decodedQueryResponse, error) {
	conn, callCtx, err := grpcQueryDial(ctx, endpoint, bearer, meta, extraDialOpts...)
	if err != nil {
		return decodedQueryResponse{}, err
	}
	defer conn.Close()

	reqBytes := encodeQueryRequest(purpose, sql, "", true)
	var respBytes []byte
	if err := conn.Invoke(callCtx, "/relata.RelataQuery/Execute", reqBytes, &respBytes, grpc.ForceCodec(rawBytesCodec{})); err != nil {
		return decodedQueryResponse{}, fmt.Errorf("relata grpc query: Execute: %w", err)
	}
	return decodeQueryResponse(respBytes)
}

// grpcQueryDial opens the ClientConn both RPCs share and returns the call
// context with the tenant/delegation/correlation headers attached as gRPC
// metadata (same convention as QueryFlight's flightMetadata, #3213). The
// transport is TLS-protected whenever endpoint uses the grpcs:///tls://
// scheme (#2362-equivalent; flightTransportCredentials is reused as-is, its
// scheme-driven TLS selection is not Flight-specific). The caller owns
// conn.Close().
func grpcQueryDial(
	ctx context.Context,
	endpoint, bearer string,
	meta map[string]string,
	extraDialOpts ...grpc.DialOption,
) (*grpc.ClientConn, context.Context, error) {
	target := stripFlightScheme(endpoint)
	dialOpts := append(
		[]grpc.DialOption{grpc.WithTransportCredentials(flightTransportCredentials(endpoint))},
		extraDialOpts...,
	)
	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, ctx, fmt.Errorf("relata grpc query: dial %s: %w", endpoint, err)
	}
	if bearer != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+bearer)
	}
	if len(meta) > 0 {
		kv := make([]string, 0, len(meta)*2)
		for k, v := range meta {
			kv = append(kv, k, v)
		}
		ctx = metadata.AppendToOutgoingContext(ctx, kv...)
	}
	return conn, ctx, nil
}

// executeStreamDesc describes the server-streaming `ExecuteStream` RPC for
// ClientConn.NewStream — the hand-rolled equivalent of the StreamDesc a
// generated .pb.go client would carry.
var executeStreamDesc = &grpc.StreamDesc{StreamName: "ExecuteStream", ServerStreams: true}

// queryGrpcExecuteStream opens a gRPC connection to endpoint and drives the
// server-streaming `RelataQuery/ExecuteStream` RPC, opting into
// wants_arrow_ipc_rows, collecting every streamed RowBatch frame into one
// *QueryResult.
func queryGrpcExecuteStream(ctx context.Context, endpoint, purpose, sql, bearer string, meta map[string]string) (*QueryResult, error) {
	return queryGrpcExecuteStreamWithDialOpts(ctx, endpoint, purpose, sql, bearer, meta)
}

// queryGrpcExecuteStreamWithDialOpts is queryGrpcExecuteStream's testable
// seam, mirroring queryGrpcExecuteWithDialOpts: extraDialOpts let a test
// inject grpc.WithContextDialer(bufconn.Listener.DialContext) to run the real
// dial -> NewStream -> RecvMsg -> decode path in-process against a fake
// RelataQuery service. Production callers never pass extraDialOpts.
func queryGrpcExecuteStreamWithDialOpts(
	ctx context.Context,
	endpoint, purpose, sql, bearer string,
	meta map[string]string,
	extraDialOpts ...grpc.DialOption,
) (*QueryResult, error) {
	conn, callCtx, err := grpcQueryDial(ctx, endpoint, bearer, meta, extraDialOpts...)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	stream, err := conn.NewStream(callCtx, executeStreamDesc, "/relata.RelataQuery/ExecuteStream", grpc.ForceCodec(rawBytesCodec{}))
	if err != nil {
		return nil, fmt.Errorf("relata grpc query: ExecuteStream: %w", err)
	}
	if err := stream.SendMsg(encodeQueryRequest(purpose, sql, "", true)); err != nil {
		return nil, fmt.Errorf("relata grpc query: ExecuteStream send: %w", err)
	}
	if err := stream.CloseSend(); err != nil {
		return nil, fmt.Errorf("relata grpc query: ExecuteStream close-send: %w", err)
	}

	columns := []string{}
	rows := make([]map[string]any, 0)
	for {
		var frameBytes []byte
		if recvErr := stream.RecvMsg(&frameBytes); recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("relata grpc query: ExecuteStream recv: %w", recvErr)
		}
		frame, decodeErr := decodeRowBatch(frameBytes)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if len(frame.columns) > 0 {
			columns = frame.columns
		}
		frameRows, rowsErr := frame.rows()
		if rowsErr != nil {
			return nil, rowsErr
		}
		rows = append(rows, frameRows...)
		if frame.done {
			// Terminal frame per the server's one-batch-lookahead contract;
			// stop reading rather than waiting for the stream's own EOF.
			break
		}
	}
	return &QueryResult{Rows: rows, RowCount: len(rows), Columns: columns}, nil
}

// QueryGrpc executes sql against the server's plain gRPC `RelataQuery.Execute`
// RPC (#4090), opting into `QueryRequest.wants_arrow_ipc_rows` so the server
// answers with a self-contained Arrow IPC row batch
// (`QueryResponse.arrow_ipc_rows`) instead of per-row JSON strings when it
// can. Decodes transparently into the same *QueryResult shape Query/
// QueryFlight return, regardless of which wire encoding the server chose —
// callers never need to know which one came back.
//
// grpcEndpoint defaults to grpc://<Client.baseURL host>:50051 (grpcs://
// when the base URL is https, matching QueryFlight's convention); pass an
// explicit grpc://host:port (or grpcs://, tls://) to override. Bearer auth
// is sent as gRPC "authorization" metadata, using the Client's token unless
// bearer overrides it.
//
// Same #3217-style cleartext-bearer fail-closed gate as QueryFlight: the
// bearer is never attached on a plaintext (grpc:// / http://) endpoint
// unless ClientOptions.AllowCleartextBearer is set — otherwise the call
// fails closed with ErrCleartextBearerDisallowed before any dial.
//
// Requires github.com/apache/arrow/go/v15 + google.golang.org/grpc (already
// direct dependencies of this module — see go.mod, added for QueryFlight).
func (c *Client) QueryGrpc(
	ctx context.Context,
	sql string,
	grpcEndpoint string,
	purpose string,
	bearer string,
) (*QueryResult, error) {
	if c.destroyed {
		return nil, ErrClientClosed
	}
	endpoint := ResolveGrpcQueryEndpoint(c.baseURL, grpcEndpoint)
	if bearer == "" {
		bearer = c.bearerToken
	}
	if bearer != "" && !isSecureFlightEndpoint(endpoint) && !c.allowCleartextBearer {
		return nil, ErrCleartextBearerDisallowed
	}
	decoded, err := queryGrpcExecute(ctx, endpoint, purpose, sql, bearer, c.flightMetadata())
	if err != nil {
		return nil, err
	}
	return decoded.toQueryResult()
}

// QueryGrpcStream executes sql against the server's server-streaming plain
// gRPC `RelataQuery.ExecuteStream` RPC (#4090), opting into
// `QueryRequest.wants_arrow_ipc_rows`. The streaming counterpart to
// QueryGrpc: the server answers with a stream of `RowBatch` frames instead of
// one buffered `QueryResponse`, and every frame independently negotiates
// `arrow_ipc_rows` vs `rows_json` (same empty-means-fall-back contract), so a
// large result set never has to be materialized as one response message. All
// frames are collected into the same *QueryResult shape QueryGrpc/Query/
// QueryFlight return.
//
// RowBatch carries no row_count field, so the returned RowCount is the number
// of rows actually received (unlike QueryGrpc, which reports the server's own
// QueryResponse.row_count).
//
// Endpoint resolution, TLS selection, bearer handling and the #3217-style
// cleartext-bearer fail-closed gate are identical to QueryGrpc's.
func (c *Client) QueryGrpcStream(
	ctx context.Context,
	sql string,
	grpcEndpoint string,
	purpose string,
	bearer string,
) (*QueryResult, error) {
	if c.destroyed {
		return nil, ErrClientClosed
	}
	endpoint := ResolveGrpcQueryEndpoint(c.baseURL, grpcEndpoint)
	if bearer == "" {
		bearer = c.bearerToken
	}
	if bearer != "" && !isSecureFlightEndpoint(endpoint) && !c.allowCleartextBearer {
		return nil, ErrCleartextBearerDisallowed
	}
	return queryGrpcExecuteStream(ctx, endpoint, purpose, sql, bearer, c.flightMetadata())
}
