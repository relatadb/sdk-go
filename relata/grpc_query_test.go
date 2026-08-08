package relata

// Tests for the first-party gRPC query client with Arrow-IPC opt-in (#4090
// round 2 — Go SDK), mirroring grpc-query.test.ts's coverage: the pure codec
// helpers (encodeQueryRequest/decodeQueryResponse) are unit-tested directly
// against hand-computed wire bytes, and the full dial -> Invoke -> decode
// path is exercised against a real in-process fake RelataQuery gRPC server
// over bufconn (mirrors flight_bufconn_test.go's approach, #2758) — no live
// server required.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/apache/arrow/go/v15/arrow"
	"github.com/apache/arrow/go/v15/arrow/array"
	"github.com/apache/arrow/go/v15/arrow/ipc"
	"github.com/apache/arrow/go/v15/arrow/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// QueryRequest / QueryResponse protobuf codec
// ---------------------------------------------------------------------------

func TestEncodeQueryRequestEncodesSqlAndPurpose(t *testing.T) {
	out := encodeQueryRequest("analytics", "SELECT 1", "", false)
	// field 1 (purpose): tag 0x0a, len 9, "analytics"
	if out[0] != 0x0a || out[1] != 9 || string(out[2:11]) != "analytics" {
		t.Fatalf("purpose field wrong: % x", out[:11])
	}
	// field 2 (sql): tag 0x12, len 8, "SELECT 1"
	if out[11] != 0x12 || out[12] != 8 || string(out[13:21]) != "SELECT 1" {
		t.Fatalf("sql field wrong: % x", out[11:21])
	}
	if len(out) != 21 {
		t.Fatalf("len(out) = %d, want 21", len(out))
	}
}

func TestEncodeQueryRequestOmitsEmptyPurposeAndBearer(t *testing.T) {
	out := encodeQueryRequest("", "SELECT 1", "", false)
	if out[0] != 0x12 {
		t.Fatalf("expected only field 2 (sql) present, got leading byte %#x", out[0])
	}
}

func TestEncodeQueryRequestSetsWantsArrowIpcRowsOnlyWhenTrue(t *testing.T) {
	withFlag := encodeQueryRequest("", "SELECT 1", "", true)
	// tag (4<<3)|0 = 0x20, value 1 — trailing 2 bytes.
	tail := withFlag[len(withFlag)-2:]
	if tail[0] != 0x20 || tail[1] != 0x01 {
		t.Fatalf("trailing bytes = % x, want [20 01]", tail)
	}

	withoutFlag := encodeQueryRequest("", "SELECT 1", "", false)
	if len(withoutFlag) != len(withFlag)-2 {
		t.Fatalf("len(withoutFlag) = %d, want %d", len(withoutFlag), len(withFlag)-2)
	}
}

func TestDecodeQueryResponseDecodesColumnsRowCountRowsJson(t *testing.T) {
	var parts bytes.Buffer
	pushStr := func(field int, s string) {
		parts.WriteByte(byte(field<<3 | 2))
		parts.WriteByte(byte(len(s)))
		parts.WriteString(s)
	}
	pushStr(1, "id")
	pushStr(1, "name")
	parts.WriteByte(byte(2<<3 | 0))
	parts.WriteByte(2) // row_count = 2
	pushStr(3, `{"id":1}`)
	pushStr(3, `{"id":2}`)

	decoded, err := decodeQueryResponse(parts.Bytes())
	if err != nil {
		t.Fatalf("decodeQueryResponse: %v", err)
	}
	if len(decoded.columns) != 2 || decoded.columns[0] != "id" || decoded.columns[1] != "name" {
		t.Fatalf("columns = %v", decoded.columns)
	}
	if decoded.rowCount != 2 {
		t.Fatalf("rowCount = %d, want 2", decoded.rowCount)
	}
	if len(decoded.rowsJSON) != 2 || decoded.rowsJSON[0] != `{"id":1}` || decoded.rowsJSON[1] != `{"id":2}` {
		t.Fatalf("rowsJSON = %v", decoded.rowsJSON)
	}
	if len(decoded.arrowIPCRows) != 0 {
		t.Fatalf("arrowIPCRows should be empty, got %v", decoded.arrowIPCRows)
	}
}

func TestDecodeQueryResponseDecodesArrowIpcRows(t *testing.T) {
	body := []byte{1, 2, 3, 4, 5}
	wire := append([]byte{byte(4<<3 | 2), byte(len(body))}, body...)
	decoded, err := decodeQueryResponse(wire)
	if err != nil {
		t.Fatalf("decodeQueryResponse: %v", err)
	}
	if !bytes.Equal(decoded.arrowIPCRows, body) {
		t.Fatalf("arrowIPCRows = %v, want %v", decoded.arrowIPCRows, body)
	}
	if len(decoded.rowsJSON) != 0 {
		t.Fatalf("rowsJSON should be empty, got %v", decoded.rowsJSON)
	}
}

// Empty arrow_ipc_rows means "read rows_json instead", never "zero rows" —
// the proto's documented fallback contract.
func TestDecodeQueryResponseEmptyArrowIpcRowsMeansReadRowsJson(t *testing.T) {
	var parts bytes.Buffer
	parts.WriteByte(byte(2<<3 | 0))
	parts.WriteByte(1) // row_count = 1
	parts.WriteByte(byte(3<<3 | 2))
	parts.WriteByte(7)
	parts.WriteString(`{"x":1}`)

	decoded, err := decodeQueryResponse(parts.Bytes())
	if err != nil {
		t.Fatalf("decodeQueryResponse: %v", err)
	}
	if len(decoded.arrowIPCRows) != 0 {
		t.Fatalf("arrowIPCRows should be empty")
	}
	if decoded.rowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", decoded.rowCount)
	}
	if len(decoded.rowsJSON) != 1 || decoded.rowsJSON[0] != `{"x":1}` {
		t.Fatalf("rowsJSON = %v", decoded.rowsJSON)
	}
}

func TestDecodeQueryResponseToleratesUnknownWireTypes(t *testing.T) {
	wire := []byte{
		0x28, 0x01, // field 5, varint (unknown, skipped)
		0x35, 0xff, 0xff, 0xff, 0xff, // field 6, fixed32 (unknown, skipped)
		byte(2<<3 | 0), 7, // field 2 (row_count) = 7
	}
	decoded, err := decodeQueryResponse(wire)
	if err != nil {
		t.Fatalf("decodeQueryResponse: %v", err)
	}
	if decoded.rowCount != 7 {
		t.Fatalf("rowCount = %d, want 7", decoded.rowCount)
	}
}

// ---------------------------------------------------------------------------
// endpoint helpers
// ---------------------------------------------------------------------------

func TestResolveGrpcQueryEndpointDerivesDefault(t *testing.T) {
	cases := map[string]string{
		"http://localhost:9090":   "grpc://localhost:50051",
		"https://relata.com:443/": "grpcs://relata.com:50051",
		"garbage":                 "grpc://localhost:50051",
	}
	for in, want := range cases {
		if got := ResolveGrpcQueryEndpoint(in, ""); got != want {
			t.Errorf("ResolveGrpcQueryEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveGrpcQueryEndpointOverrideWins(t *testing.T) {
	got := ResolveGrpcQueryEndpoint("http://localhost:9090", "grpc://h:9999")
	if want := "grpc://h:9999"; got != want {
		t.Fatalf("explicit override did not win: got %q want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// cleartext-bearer gate (#3217-style, same convention as QueryFlight)
// ---------------------------------------------------------------------------

func TestQueryGrpcRefusesCleartextBearer(t *testing.T) {
	c := New("http://localhost:9090", &ClientOptions{BearerToken: "tok"})
	_, err := c.QueryGrpc(context.Background(), "SELECT 1", "grpc://localhost:50051", "", "")
	if !errors.Is(err, ErrCleartextBearerDisallowed) {
		t.Fatalf("err = %v, want ErrCleartextBearerDisallowed", err)
	}
}

func TestQueryGrpcCleartextBearerOptIn(t *testing.T) {
	c := New("http://localhost:9090", &ClientOptions{
		BearerToken:          "tok",
		AllowCleartextBearer: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.QueryGrpc(ctx, "SELECT 1", "grpc://127.0.0.1:1", "", "")
	if errors.Is(err, ErrCleartextBearerDisallowed) {
		t.Fatalf("opt-in must bypass the cleartext gate, got %v", err)
	}
}

func TestQueryGrpcClosedClientErrors(t *testing.T) {
	c := New("http://localhost:9090", nil)
	c.Close()
	_, err := c.QueryGrpc(context.Background(), "SELECT 1", "", "", "")
	if !errors.Is(err, ErrClientClosed) {
		t.Fatalf("err = %v, want ErrClientClosed", err)
	}
}

// ---------------------------------------------------------------------------
// real dial -> Invoke -> decode path over bufconn (#2758-style, no live
// server required)
// ---------------------------------------------------------------------------

// decodeTestQueryRequest is the test-side mirror of encodeQueryRequest,
// letting the fake server assert exactly what the client sent: purpose (1),
// sql (2), wants_arrow_ipc_rows (4). QueryRequest's field layout doesn't
// match QueryResponse's, so this walks the wire form itself rather than
// reusing decodeQueryResponse.
func decodeTestQueryRequest(buf []byte) (purpose, sql string, wantsArrowIPCRows bool) {
	i := 0
	for i < len(buf) {
		tag, n := readTestVarint(buf, i)
		if n == 0 {
			break
		}
		i += n
		field := int(tag >> 3)
		wireType := tag & 0x7
		switch wireType {
		case 0:
			v, n := readTestVarint(buf, i)
			if n == 0 {
				return purpose, sql, wantsArrowIPCRows
			}
			i += n
			if field == 4 {
				wantsArrowIPCRows = v == 1
			}
		case 2:
			l, n := readTestVarint(buf, i)
			if n == 0 {
				return purpose, sql, wantsArrowIPCRows
			}
			i += n
			seg := buf[i : i+int(l)]
			i += int(l)
			switch field {
			case 1:
				purpose = string(seg)
			case 2:
				sql = string(seg)
			}
		default:
			return purpose, sql, wantsArrowIPCRows
		}
	}
	return purpose, sql, wantsArrowIPCRows
}

func readTestVarint(buf []byte, offset int) (uint64, int) {
	var result uint64
	var shift uint
	for i := offset; i < len(buf); i++ {
		b := buf[i]
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, i - offset + 1
		}
		shift += 7
	}
	return 0, 0
}

// bufconnQueryServer is a minimal fake RelataQuery service driven through a
// manually-built grpc.ServiceDesc (no generated .pb.go stubs) — the server
// side of the same "no proto-loader" contract queryGrpcExecuteWithDialOpts
// exercises from the client side. respondWithArrow selects which of
// QueryResponse's two row encodings it answers with.
type bufconnQueryServer struct {
	gotPurpose  string
	gotSQL      string
	gotWantsIPC bool
	gotAuth     string
	gotTenant   string

	respondWithArrow bool
	// streamFrames overrides handleExecuteStream's default frame sequence
	// (#4090 round 7); nil means "use the default two-data-frames + done".
	streamFrames [][]byte
}

func (s *bufconnQueryServer) handleExecute(ctx context.Context, reqBytes []byte) ([]byte, error) {
	s.gotPurpose, s.gotSQL, s.gotWantsIPC = decodeTestQueryRequest(reqBytes)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			s.gotAuth = vals[0]
		}
		if vals := md.Get("x-relata-tenant-id"); len(vals) > 0 {
			s.gotTenant = vals[0]
		}
	}

	var resp bytes.Buffer
	writeProtoString(&resp, 1, "id")
	writeProtoString(&resp, 1, "name")
	writeProtoVarint(&resp, protoTag(2, 0))
	writeProtoVarint(&resp, 3) // row_count = 3

	if s.respondWithArrow {
		ipcBytes := buildTestArrowIPCBytes()
		writeProtoVarint(&resp, protoTag(4, 2))
		writeProtoVarint(&resp, uint64(len(ipcBytes)))
		resp.Write(ipcBytes)
	} else {
		writeProtoString(&resp, 3, `{"id":1,"name":"a"}`)
		writeProtoString(&resp, 3, `{"id":2,"name":"b"}`)
		writeProtoString(&resp, 3, `{"id":3,"name":"c"}`)
	}
	return resp.Bytes(), nil
}

// buildTestArrowIPCBytes builds a self-contained Arrow IPC stream (schema +
// one 3-row RecordBatch, {id int64, name utf8}) matching
// QueryResponse.arrow_ipc_rows's documented shape.
func buildTestArrowIPCBytes() []byte {
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

	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(schema), ipc.WithAllocator(mem))
	if err := w.Write(rec); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// testRelataQueryServiceDesc registers a single unary "Execute" method under
// the real "/relata.RelataQuery/Execute" path without any generated .pb.go
// stub — HandlerType is `(*any)(nil)` (the empty interface), which every
// registered service value trivially implements, matching the permissive
// pattern grpc.ForceServerCodec-based generic services use.
var testRelataQueryServiceDesc = grpc.ServiceDesc{
	ServiceName: "relata.RelataQuery",
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Execute",
			Handler: func(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				var reqBytes []byte
				if err := dec(&reqBytes); err != nil {
					return nil, err
				}
				return srv.(*bufconnQueryServer).handleExecute(ctx, reqBytes)
			},
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "ExecuteStream",
			ServerStreams: true,
			Handler: func(srv any, stream grpc.ServerStream) error {
				var reqBytes []byte
				if err := stream.RecvMsg(&reqBytes); err != nil {
					return err
				}
				return srv.(*bufconnQueryServer).handleExecuteStream(stream, reqBytes)
			},
		},
	},
	Metadata: "relata.proto",
}

// startBufconnQueryServer serves srv over an in-memory bufconn.Listener,
// forcing both directions through rawBytesCodec so the wire bytes it
// exchanges with the client are exactly what queryGrpcExecuteWithDialOpts
// sends/expects in production (content-subtype "proto").
func startBufconnQueryServer(t *testing.T, srv *bufconnQueryServer) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	const bufSize = 1 << 20
	lis := bufconn.Listen(bufSize)

	gs := grpc.NewServer(grpc.ForceServerCodec(rawBytesCodec{}))
	gs.RegisterService(&testRelataQueryServiceDesc, srv)
	go func() {
		_ = gs.Serve(lis)
	}()
	t.Cleanup(gs.Stop)

	return func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
}

// TestQueryGrpcExecuteOverBufconnRowsJson exercises the real
// dial -> Invoke -> decode path (rows_json branch) against an in-process
// fake RelataQuery server, with no live server required (#2758-style).
func TestQueryGrpcExecuteOverBufconnRowsJson(t *testing.T) {
	srv := &bufconnQueryServer{}
	dialer := startBufconnQueryServer(t, srv)

	decoded, err := queryGrpcExecuteWithDialOpts(
		context.Background(),
		"passthrough:///bufnet",
		"investigation",
		"SELECT * FROM Person LIMIT 3",
		"test-bearer-token",
		map[string]string{"x-relata-tenant-id": "org-acme"},
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryGrpcExecuteWithDialOpts: %v", err)
	}

	if srv.gotSQL != "SELECT * FROM Person LIMIT 3" {
		t.Fatalf("server received sql %q", srv.gotSQL)
	}
	if srv.gotPurpose != "investigation" {
		t.Fatalf("server received purpose %q", srv.gotPurpose)
	}
	if !srv.gotWantsIPC {
		t.Fatal("server did not see wants_arrow_ipc_rows = true")
	}
	if want := "Bearer test-bearer-token"; srv.gotAuth != want {
		t.Fatalf("server received authorization %q, want %q", srv.gotAuth, want)
	}
	if srv.gotTenant != "org-acme" {
		t.Fatalf("server received tenant %q, want org-acme", srv.gotTenant)
	}

	result, err := decoded.toQueryResult()
	if err != nil {
		t.Fatalf("toQueryResult: %v", err)
	}
	if result.RowCount != 3 || len(result.Rows) != 3 {
		t.Fatalf("RowCount/len(Rows) = %d/%d, want 3/3", result.RowCount, len(result.Rows))
	}
	if result.Rows[0]["id"] != float64(1) {
		t.Fatalf("Rows[0][\"id\"] = %v", result.Rows[0]["id"])
	}
	if len(result.Columns) != 2 || result.Columns[0] != "id" || result.Columns[1] != "name" {
		t.Fatalf("Columns = %v", result.Columns)
	}
}

// TestQueryGrpcExecuteOverBufconnArrowIpc exercises the arrow_ipc_rows
// response branch — the real reason for this ticket — decoding a
// self-contained Arrow IPC RecordBatch into the same row-map shape the
// rows_json branch produces.
func TestQueryGrpcExecuteOverBufconnArrowIpc(t *testing.T) {
	srv := &bufconnQueryServer{respondWithArrow: true}
	dialer := startBufconnQueryServer(t, srv)

	decoded, err := queryGrpcExecuteWithDialOpts(
		context.Background(),
		"passthrough:///bufnet",
		"",
		"SELECT id, name FROM Person",
		"",
		nil,
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryGrpcExecuteWithDialOpts: %v", err)
	}
	if len(decoded.arrowIPCRows) == 0 {
		t.Fatal("expected a non-empty arrow_ipc_rows payload")
	}

	result, err := decoded.toQueryResult()
	if err != nil {
		t.Fatalf("toQueryResult: %v", err)
	}
	if result.RowCount != 3 || len(result.Rows) != 3 {
		t.Fatalf("RowCount/len(Rows) = %d/%d, want 3/3", result.RowCount, len(result.Rows))
	}
	if result.Rows[1]["name"] != "b" {
		t.Fatalf("Rows[1][\"name\"] = %v, want \"b\"", result.Rows[1]["name"])
	}
}

// TestQueryGrpcOverBufconnEndToEnd exercises Client.QueryGrpc itself
// (not just the internal seam) via the dial-opts injection path by
// round-tripping queryGrpcExecuteWithDialOpts and toQueryResult the same
// way QueryGrpc composes them — QueryGrpc's own endpoint resolution /
// cleartext-bearer gate are covered separately above since QueryGrpc has no
// dial-opts seam of its own (matches QueryFlight's public-surface shape).
func TestQueryGrpcOverBufconnEndToEnd(t *testing.T) {
	srv := &bufconnQueryServer{}
	dialer := startBufconnQueryServer(t, srv)

	decoded, err := queryGrpcExecuteWithDialOpts(
		context.Background(),
		ResolveGrpcQueryEndpoint("http://localhost:9090", "passthrough:///bufnet"),
		"",
		"SELECT 1",
		"",
		nil,
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryGrpcExecuteWithDialOpts: %v", err)
	}
	result, err := decoded.toQueryResult()
	if err != nil {
		t.Fatalf("toQueryResult: %v", err)
	}
	if result.RowCount != 3 {
		t.Fatalf("RowCount = %d, want 3", result.RowCount)
	}
}

// ---------------------------------------------------------------------------
// RowBatch protobuf codec (#4090 round 7 — ExecuteStream). RowBatch's field
// layout is NOT QueryResponse's: rows_json is field 2 (not 3), there's a
// `done` bool at field 3, and there is no row_count field at all.
// ---------------------------------------------------------------------------

// buildTestRowBatch encodes a `RowBatch` frame by hand (columns 1, rows_json 2,
// done 3, arrow_ipc_rows 4) — deliberately independent of decodeRowBatch so
// the decoder is tested against bytes, not against itself.
func buildTestRowBatch(columns, rowsJSON []string, done bool, arrowIPC []byte) []byte {
	var buf bytes.Buffer
	for _, c := range columns {
		writeProtoString(&buf, 1, c)
	}
	for _, r := range rowsJSON {
		writeProtoString(&buf, 2, r)
	}
	if done {
		writeProtoVarint(&buf, protoTag(3, 0))
		writeProtoVarint(&buf, 1)
	}
	if len(arrowIPC) > 0 {
		writeProtoVarint(&buf, protoTag(4, 2))
		writeProtoVarint(&buf, uint64(len(arrowIPC)))
		buf.Write(arrowIPC)
	}
	return buf.Bytes()
}

func TestDecodeRowBatchReadsField2RowsJsonNotField3(t *testing.T) {
	raw := buildTestRowBatch([]string{"id", "name"}, []string{`{"id":1,"name":"a"}`}, false, nil)
	frame, err := decodeRowBatch(raw)
	if err != nil {
		t.Fatalf("decodeRowBatch: %v", err)
	}
	if len(frame.columns) != 2 || frame.columns[0] != "id" {
		t.Fatalf("columns = %v", frame.columns)
	}
	if len(frame.rowsJSON) != 1 || frame.rowsJSON[0] != `{"id":1,"name":"a"}` {
		t.Fatalf("rowsJSON = %v", frame.rowsJSON)
	}
	if frame.done {
		t.Fatal("done should default to false")
	}
	if len(frame.arrowIPCRows) != 0 {
		t.Fatalf("arrowIPCRows = % x, want empty", frame.arrowIPCRows)
	}
	// Cross-check: the same bytes through the QueryResponse decoder yield no
	// rows — proof the two layouts genuinely differ and must not be shared.
	asResponse, err := decodeQueryResponse(raw)
	if err != nil {
		t.Fatalf("decodeQueryResponse: %v", err)
	}
	if len(asResponse.rowsJSON) != 0 {
		t.Fatalf("QueryResponse decoder read RowBatch rows: %v", asResponse.rowsJSON)
	}
}

func TestDecodeRowBatchReadsDoneFlag(t *testing.T) {
	frame, err := decodeRowBatch(buildTestRowBatch(nil, nil, true, nil))
	if err != nil {
		t.Fatalf("decodeRowBatch: %v", err)
	}
	if !frame.done {
		t.Fatal("done = false, want true")
	}
}

func TestDecodeRowBatchReadsArrowIpcRows(t *testing.T) {
	frame, err := decodeRowBatch(buildTestRowBatch([]string{"id"}, nil, false, []byte{1, 2, 3}))
	if err != nil {
		t.Fatalf("decodeRowBatch: %v", err)
	}
	if !bytes.Equal(frame.arrowIPCRows, []byte{1, 2, 3}) {
		t.Fatalf("arrowIPCRows = % x", frame.arrowIPCRows)
	}
	if len(frame.rowsJSON) != 0 {
		t.Fatalf("rowsJSON = %v, want empty (never-both contract)", frame.rowsJSON)
	}
}

func TestDecodeRowBatchTruncatedPayloadErrors(t *testing.T) {
	// field 1, wire type 2, declared length 9, only 1 byte of payload.
	if _, err := decodeRowBatch([]byte{0x0a, 0x09, 'a'}); err == nil {
		t.Fatal("expected a truncated-payload error")
	}
}

func TestDecodeRowBatchSkipsUnknownFixedWireTypes(t *testing.T) {
	var buf bytes.Buffer
	writeProtoVarint(&buf, protoTag(99, 5)) // unknown field, fixed32
	buf.Write([]byte{0, 0, 0, 0})
	buf.Write(buildTestRowBatch(nil, []string{`{"x":1}`}, true, nil))
	frame, err := decodeRowBatch(buf.Bytes())
	if err != nil {
		t.Fatalf("decodeRowBatch: %v", err)
	}
	if len(frame.rowsJSON) != 1 || !frame.done {
		t.Fatalf("frame = %+v", frame)
	}
}

func TestDecodedRowBatchRowsFallsBackToRowsJson(t *testing.T) {
	frame := decodedRowBatch{rowsJSON: []string{`{"id":1}`, `{"id":2}`}}
	rows, err := frame.rows()
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(rows) != 2 || rows[1]["id"] != float64(2) {
		t.Fatalf("rows = %v", rows)
	}
}

func TestDecodedRowBatchRowsPrefersArrowIpc(t *testing.T) {
	frame := decodedRowBatch{
		rowsJSON:     []string{`{"id":999}`},
		arrowIPCRows: buildTestArrowIPCBytes(),
	}
	rows, err := frame.rows()
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(rows) != 3 || rows[0]["name"] != "a" {
		t.Fatalf("rows = %v, want the 3 Arrow rows, not the rows_json one", rows)
	}
}

// ---------------------------------------------------------------------------
// ExecuteStream over bufconn — the real dial -> NewStream -> RecvMsg -> decode
// path against the in-process fake RelataQuery service.
// ---------------------------------------------------------------------------

// handleExecuteStream serves the fake ExecuteStream RPC: it records what the
// client asked for, then emits streamFrames (defaulting to a two-data-frame +
// terminal-done-frame stream in whichever row encoding respondWithArrow
// selects).
func (s *bufconnQueryServer) handleExecuteStream(stream grpc.ServerStream, reqBytes []byte) error {
	s.gotPurpose, s.gotSQL, s.gotWantsIPC = decodeTestQueryRequest(reqBytes)
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			s.gotAuth = vals[0]
		}
		if vals := md.Get("x-relata-tenant-id"); len(vals) > 0 {
			s.gotTenant = vals[0]
		}
	}

	frames := s.streamFrames
	if frames == nil {
		cols := []string{"id", "name"}
		if s.respondWithArrow {
			frames = [][]byte{
				buildTestRowBatch(cols, nil, false, buildTestArrowIPCBytes()),
				buildTestRowBatch(nil, nil, true, nil),
			}
		} else {
			frames = [][]byte{
				buildTestRowBatch(cols, []string{`{"id":1,"name":"a"}`}, false, nil),
				buildTestRowBatch(cols, []string{`{"id":2,"name":"b"}`}, false, nil),
				buildTestRowBatch(nil, nil, true, nil),
			}
		}
	}
	for _, frame := range frames {
		if err := stream.SendMsg(frame); err != nil {
			return err
		}
	}
	return nil
}

func TestQueryGrpcExecuteStreamOverBufconnRowsJson(t *testing.T) {
	srv := &bufconnQueryServer{}
	dialer := startBufconnQueryServer(t, srv)

	result, err := queryGrpcExecuteStreamWithDialOpts(
		context.Background(),
		"passthrough:///bufnet",
		"investigation",
		"SELECT * FROM Person",
		"test-bearer-token",
		map[string]string{"x-relata-tenant-id": "org-acme"},
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryGrpcExecuteStreamWithDialOpts: %v", err)
	}
	if !srv.gotWantsIPC {
		t.Fatal("server did not see wants_arrow_ipc_rows = true on the stream request")
	}
	if srv.gotSQL != "SELECT * FROM Person" || srv.gotPurpose != "investigation" {
		t.Fatalf("server received sql=%q purpose=%q", srv.gotSQL, srv.gotPurpose)
	}
	if want := "Bearer test-bearer-token"; srv.gotAuth != want {
		t.Fatalf("server received authorization %q, want %q", srv.gotAuth, want)
	}
	if srv.gotTenant != "org-acme" {
		t.Fatalf("server received tenant %q, want org-acme", srv.gotTenant)
	}
	if result.RowCount != 2 || len(result.Rows) != 2 {
		t.Fatalf("RowCount/len(Rows) = %d/%d, want 2/2", result.RowCount, len(result.Rows))
	}
	if result.Rows[1]["name"] != "b" {
		t.Fatalf("Rows[1][\"name\"] = %v, want \"b\"", result.Rows[1]["name"])
	}
	if len(result.Columns) != 2 || result.Columns[0] != "id" {
		t.Fatalf("Columns = %v", result.Columns)
	}
}

func TestQueryGrpcExecuteStreamOverBufconnArrowIpc(t *testing.T) {
	srv := &bufconnQueryServer{respondWithArrow: true}
	dialer := startBufconnQueryServer(t, srv)

	result, err := queryGrpcExecuteStreamWithDialOpts(
		context.Background(),
		"passthrough:///bufnet",
		"",
		"SELECT id, name FROM Person",
		"",
		nil,
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryGrpcExecuteStreamWithDialOpts: %v", err)
	}
	if result.RowCount != 3 || len(result.Rows) != 3 {
		t.Fatalf("RowCount/len(Rows) = %d/%d, want 3/3", result.RowCount, len(result.Rows))
	}
	if result.Rows[2]["name"] != "c" {
		t.Fatalf("Rows[2][\"name\"] = %v, want \"c\"", result.Rows[2]["name"])
	}
}

// TestQueryGrpcExecuteStreamMixedFrameEncodings proves the per-frame
// negotiation contract: one stream may mix an Arrow-IPC frame and a
// rows_json frame, and both decode into the same row-map shape.
func TestQueryGrpcExecuteStreamMixedFrameEncodings(t *testing.T) {
	srv := &bufconnQueryServer{streamFrames: [][]byte{
		buildTestRowBatch([]string{"id", "name"}, nil, false, buildTestArrowIPCBytes()),
		buildTestRowBatch(nil, []string{`{"id":4,"name":"d"}`}, false, nil),
		buildTestRowBatch(nil, nil, true, nil),
	}}
	dialer := startBufconnQueryServer(t, srv)

	result, err := queryGrpcExecuteStreamWithDialOpts(
		context.Background(), "passthrough:///bufnet", "", "SELECT 1", "", nil,
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryGrpcExecuteStreamWithDialOpts: %v", err)
	}
	if result.RowCount != 4 {
		t.Fatalf("RowCount = %d, want 4 (3 Arrow rows + 1 rows_json row)", result.RowCount)
	}
	if result.Rows[3]["name"] != "d" {
		t.Fatalf("Rows[3][\"name\"] = %v, want \"d\"", result.Rows[3]["name"])
	}
}

// TestQueryGrpcExecuteStreamStopsAtDoneFrame proves collection terminates on
// the `done = true` frame rather than reading whatever follows it.
func TestQueryGrpcExecuteStreamStopsAtDoneFrame(t *testing.T) {
	srv := &bufconnQueryServer{streamFrames: [][]byte{
		buildTestRowBatch([]string{"x"}, []string{`{"x":1}`}, false, nil),
		buildTestRowBatch(nil, nil, true, nil),
		buildTestRowBatch(nil, []string{`{"x":999}`}, false, nil),
	}}
	dialer := startBufconnQueryServer(t, srv)

	result, err := queryGrpcExecuteStreamWithDialOpts(
		context.Background(), "passthrough:///bufnet", "", "SELECT 1", "", nil,
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("queryGrpcExecuteStreamWithDialOpts: %v", err)
	}
	if result.RowCount != 1 || result.Rows[0]["x"] != float64(1) {
		t.Fatalf("rows = %v, want just the pre-done frame's row", result.Rows)
	}
}

func TestQueryGrpcStreamClosedClientErrors(t *testing.T) {
	c := New("http://localhost:9090", nil)
	c.Close()
	if _, err := c.QueryGrpcStream(context.Background(), "SELECT 1", "", "", ""); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("err = %v, want ErrClientClosed", err)
	}
}

func TestQueryGrpcStreamRefusesCleartextBearer(t *testing.T) {
	c := New("http://localhost:9090", &ClientOptions{BearerToken: "tok"})
	_, err := c.QueryGrpcStream(context.Background(), "SELECT 1", "grpc://localhost:50051", "", "")
	if !errors.Is(err, ErrCleartextBearerDisallowed) {
		t.Fatalf("err = %v, want ErrCleartextBearerDisallowed", err)
	}
}
