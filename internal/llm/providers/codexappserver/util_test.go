package codexappserver

import (
	"encoding/json"
	"testing"
)

func TestUtil_AsHelpersAndNormalization(t *testing.T) {
	m := map[string]any{"x": 1}
	if got := asMap(m); got["x"] != 1 {
		t.Fatalf("asMap mismatch: %#v", got)
	}
	if got := asMap("not-a-map"); got != nil {
		t.Fatalf("expected nil for invalid map cast, got %#v", got)
	}

	s := []any{"a", 1}
	if got := asSlice(s); len(got) != 2 {
		t.Fatalf("asSlice mismatch: %#v", got)
	}
	if got := asSlice("not-a-slice"); got != nil {
		t.Fatalf("expected nil for invalid slice cast, got %#v", got)
	}

	if got := asString("abc"); got != "abc" {
		t.Fatalf("asString string mismatch: %q", got)
	}
	if got := asString(json.Number("123")); got != "123" {
		t.Fatalf("asString json.Number mismatch: %q", got)
	}
	if got := asString(99); got != "" {
		t.Fatalf("expected empty string for unsupported type, got %q", got)
	}

	if got := normalizeCode("  invalid-request  "); got != "INVALID_REQUEST" {
		t.Fatalf("normalizeCode mismatch: %q", got)
	}
	if got := firstNonEmpty("", "  ", "x", "y"); got != "x" {
		t.Fatalf("firstNonEmpty mismatch: %q", got)
	}
}

func TestUtil_AsIntAndBool(t *testing.T) {
	if got := asInt(int8(2), 0); got != 2 {
		t.Fatalf("asInt int8 mismatch: %d", got)
	}
	if got := asInt(int16(3), 0); got != 3 {
		t.Fatalf("asInt int16 mismatch: %d", got)
	}
	if got := asInt(int32(4), 0); got != 4 {
		t.Fatalf("asInt int32 mismatch: %d", got)
	}
	if got := asInt(int64(5), 0); got != 5 {
		t.Fatalf("asInt int64 mismatch: %d", got)
	}
	if got := asInt(float32(6.9), 0); got != 6 {
		t.Fatalf("asInt float32 mismatch: %d", got)
	}
	if got := asInt(float64(7.9), 0); got != 7 {
		t.Fatalf("asInt float64 mismatch: %d", got)
	}
	if got := asInt(json.Number("8"), 0); got != 8 {
		t.Fatalf("asInt json.Number mismatch: %d", got)
	}
	if got := asInt(" 9 ", 0); got != 9 {
		t.Fatalf("asInt string numeric mismatch: %d", got)
	}
	if got := asInt(" ", 42); got != 42 {
		t.Fatalf("asInt empty string should use default: %d", got)
	}
	if got := asInt("not-numeric", 42); got != 42 {
		t.Fatalf("asInt invalid string should use default: %d", got)
	}

	if got := asBool(true, false); !got {
		t.Fatalf("asBool true mismatch")
	}
	if got := asBool("bad", true); !got {
		t.Fatalf("asBool fallback mismatch")
	}
}

func TestUtil_DeepCopyAndDecodeJSONToMap(t *testing.T) {
	orig := map[string]any{"nested": map[string]any{"x": 1}}
	cp := deepCopyMap(orig)
	if cp == nil {
		t.Fatalf("deepCopyMap returned nil")
	}
	nested := asMap(cp["nested"])
	nested["x"] = 9
	if asMap(orig["nested"])["x"] != 1 {
		t.Fatalf("deepCopyMap should not alias nested map")
	}

	cp = deepCopyMap(nil)
	if cp != nil {
		t.Fatalf("deepCopyMap(nil) should return nil, got %#v", cp)
	}

	withUnmarshalable := map[string]any{
		"f": func() {},
		"x": "ok",
	}
	cp = deepCopyMap(withUnmarshalable)
	if cp["x"] != "ok" {
		t.Fatalf("deepCopyMap fallback copy mismatch: %#v", cp)
	}
	if _, ok := cp["f"]; !ok {
		t.Fatalf("deepCopyMap fallback should preserve unmarshalable key")
	}

	if got := decodeJSONToMap([]byte(`{"x":1}`)); asString(got["x"]) != "1" {
		t.Fatalf("decodeJSONToMap valid json mismatch: %#v", got)
	}
	if got := decodeJSONToMap([]byte(`not-json`)); len(got) != 0 {
		t.Fatalf("decodeJSONToMap invalid json should return empty map, got %#v", got)
	}
	if got := decodeJSONToMap([]byte(`null`)); len(got) != 0 {
		t.Fatalf("decodeJSONToMap null should return empty map, got %#v", got)
	}
}
