package relata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
)

var receiptIntegerToken = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// DecodeDeletionProgress decodes ORIGINAL deletion-inspection response bytes.
// Every integer token (including version/counts) remains json.Number. This
// opt-in utility does not alter generic client decoding or authenticate receipts.
// maxBytes <= 0 uses the SDK response cap; the reader is owned by the caller.
func DecodeDeletionProgress(reader io.Reader, maxBytes int64) (map[string]any, error) {
	if maxBytes == math.MaxInt64 {
		return nil, fmt.Errorf("relata: receipt response cap must leave room for overflow detection")
	}
	data, err := (&Client{maxResponseBytes: maxBytes}).readCapped(reader)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var receipt map[string]any
	if err := decoder.Decode(&receipt); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("relata: trailing deletion receipt data")
	}
	version, ok := receipt["version"].(json.Number)
	revision, revisionNumber := receipt["revision"].(json.Number)
	_, revisionError := strconv.ParseUint(revision.String(), 10, 64)
	_, operation := receipt["operation_id"].(string)
	if !ok || version.String() != "1" || !revisionNumber || revisionError != nil || !operation || receipt["scope"] != "observed_local_source_versions" ||
		receipt["global_copy_inventory"] != "incomplete" || receipt["global_physical_erasure_verified"] != false {
		return nil, fmt.Errorf("relata: unsupported deletion progress receipt envelope")
	}
	if err := checkReceiptIntegers(receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func checkReceiptIntegers(value any) error {
	switch value := value.(type) {
	case json.Number:
		if !receiptIntegerToken.MatchString(value.String()) {
			return fmt.Errorf("relata: deletion receipt numbers must be integer tokens")
		}
		var err error
		if len(value.String()) > 21 {
			return fmt.Errorf("relata: deletion receipt integer exceeds supported i64/u64 range")
		}
		if value.String()[0] == '-' {
			_, err = strconv.ParseInt(value.String(), 10, 64)
		} else {
			_, err = strconv.ParseUint(value.String(), 10, 64)
		}
		if err != nil {
			return fmt.Errorf("relata: deletion receipt integer exceeds supported i64/u64 range")
		}
	case map[string]any:
		for _, child := range value {
			if err := checkReceiptIntegers(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := checkReceiptIntegers(child); err != nil {
				return err
			}
		}
	}
	return nil
}
