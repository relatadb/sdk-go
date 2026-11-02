package tests

import (
	"bytes"
	"testing"

	relata "github.com/relatadb/sdk-go/v2/relata"
)

// Object HEAD must report the stored size (coordinator-reported SD defect).
func TestRealFilesHeadSize(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	f := relata.NewFilesClient(c)
	key := uniq("head") + "/blob.bin"
	data := bytes.Repeat([]byte("relata-"), 300)
	if _, err := f.Put(ctx, key, data, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	h, err := f.Head(ctx, key, nil)
	must(t, err, "head")
	if h.Size != int64(len(data)) {
		t.Fatalf("Head size = %d, want %d", h.Size, len(data))
	}
	g, err := f.Get(ctx, key, nil)
	must(t, err, "get")
	if g.Size != int64(len(data)) || !bytes.Equal(g.Bytes, data) {
		t.Fatalf("Get size=%d len=%d", g.Size, len(g.Bytes))
	}
}

// WITH PROVENANCE results must keep the provenance chains in the result model.
func TestRealQueryProvenance(t *testing.T) {
	c := realClient(t)
	ctx := ctxT(t)
	ty := uniq("RpProv")
	ingestRowsWait(t, c, ty, []map[string]any{{"_pk": "p1", "v": 1}, {"_pk": "p2", "v": 2}})
	r, err := relata.NewQuery("SELECT * FROM "+ty).Purpose("audit").WithProvenance().Execute(ctx, c)
	must(t, err, "provenance query")
	if len(r.Rows) != 2 {
		t.Fatalf("rows: %v", r.Rows)
	}
	if len(r.Provenance) == 0 {
		t.Fatalf("provenance dropped by the result model: %+v", r)
	}
}
