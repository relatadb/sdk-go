package relata

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestBytesIteratorNextEndsAtEOF(t *testing.T) {
	it := &BytesIterator{reader: bufio.NewReader(strings.NewReader("abc")), body: io.NopCloser(strings.NewReader(""))}
	if !it.Next() {
		t.Fatal("Next must be true while bytes remain")
	}
	b, _ := io.ReadAll(it)
	if string(b) != "abc" {
		t.Fatalf("read %q", b)
	}
	if it.Next() || it.Err() != nil {
		t.Fatalf("Next must be false at EOF with no error (err=%v)", it.Err())
	}
}
