package runtime

import (
	"testing"
)

func TestContext_SetGetAndSnapshots(t *testing.T) {
	c := NewContext()
	c.Set("a", "1")
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Fatalf("Get(a)=%v ok=%v", v, ok)
	}
	if got := c.GetString("a", ""); got != "1" {
		t.Fatalf("GetString(a)=%q", got)
	}
	if got := c.GetString("missing", "d"); got != "d" {
		t.Fatalf("GetString(missing)=%q", got)
	}

	c.AppendLog("warn")
	vals := c.SnapshotValues()
	logs := c.SnapshotLogs()
	if vals["a"] != "1" || len(logs) != 1 || logs[0] != "warn" {
		t.Fatalf("snapshots: vals=%v logs=%v", vals, logs)
	}
}

func TestContext_CloneAndReplaceSnapshot(t *testing.T) {
	c := NewContext()
	c.Set("x", 1)
	c.AppendLog("l1")

	cl := c.Clone()
	cl.Set("x", 2)
	cl.AppendLog("l2")

	if got := c.GetString("x", ""); got != "1" {
		t.Fatalf("original mutated: %q", got)
	}
	if got := cl.GetString("x", ""); got != "2" {
		t.Fatalf("clone not updated: %q", got)
	}

	c.ReplaceSnapshot(map[string]any{"k": "v"}, []string{"l3"})
	if got := c.GetString("k", ""); got != "v" {
		t.Fatalf("ReplaceSnapshot values: %q", got)
	}
	if _, ok := c.Get("x"); ok {
		t.Fatalf("ReplaceSnapshot should replace values entirely")
	}
	if logs := c.SnapshotLogs(); len(logs) != 1 || logs[0] != "l3" {
		t.Fatalf("ReplaceSnapshot logs: %v", logs)
	}
}

func TestContext_Clone_DeepCopiesNestedValues(t *testing.T) {
	// Verify that Clone() deep-copies nested maps and slices so that
	// mutations in one branch don't contaminate the other (spec §5.1).
	c := NewContext()

	// Set a nested map value.
	innerMap := map[string]any{
		"key1": "original",
		"key2": float64(42),
	}
	c.Set("nested_map", innerMap)

	// Set a slice value.
	innerSlice := []any{"a", "b", "c"}
	c.Set("nested_slice", innerSlice)

	// Set primitive values (should be shared safely since they're immutable).
	c.Set("str", "hello")
	c.Set("num", 123)

	// Clone the context.
	cl := c.Clone()

	// Mutate the ORIGINAL's nested map in-place.
	innerMap["key1"] = "mutated"
	innerMap["key3"] = "new"

	// Mutate the ORIGINAL's nested slice in-place.
	innerSlice[0] = "MUTATED"

	// Verify that the clone's nested map was NOT affected.
	clonedMap, ok := cl.Get("nested_map")
	if !ok {
		t.Fatal("clone missing nested_map")
	}
	m, ok := clonedMap.(map[string]any)
	if !ok {
		t.Fatalf("cloned nested_map wrong type: %T", clonedMap)
	}
	if m["key1"] != "original" {
		t.Fatalf("clone's nested map was mutated: key1=%v", m["key1"])
	}
	if _, exists := m["key3"]; exists {
		t.Fatal("clone's nested map gained key3 from original mutation")
	}

	// Verify that the clone's nested slice was NOT affected.
	clonedSlice, ok := cl.Get("nested_slice")
	if !ok {
		t.Fatal("clone missing nested_slice")
	}
	s, ok := clonedSlice.([]any)
	if !ok {
		t.Fatalf("cloned nested_slice wrong type: %T", clonedSlice)
	}
	if s[0] != "a" {
		t.Fatalf("clone's nested slice was mutated: [0]=%v", s[0])
	}

	// Verify primitives are still correct.
	if got := cl.GetString("str", ""); got != "hello" {
		t.Fatalf("clone str=%q", got)
	}
	if v, ok := cl.Get("num"); !ok {
		t.Fatal("clone missing num")
	} else {
		// Primitive int hits the fast path and remains int (no JSON round-trip).
		switch n := v.(type) {
		case int:
			if n != 123 {
				t.Fatalf("clone num=%v", n)
			}
		case float64:
			if n != 123 {
				t.Fatalf("clone num=%v", n)
			}
		default:
			t.Fatalf("clone num unexpected type: %T", v)
		}
	}
}

func TestContext_Clone_NilValue(t *testing.T) {
	c := NewContext()
	c.Set("nil_val", nil)

	cl := c.Clone()
	v, ok := cl.Get("nil_val")
	if !ok {
		t.Fatal("clone missing nil_val key")
	}
	if v != nil {
		t.Fatalf("clone nil_val=%v, want nil", v)
	}
}

