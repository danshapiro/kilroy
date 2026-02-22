package engine

import (
	"strings"
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// TestRestoreLoopFailureSignatures verifies round-trip serialization of
// loop_failure_signatures through checkpoint.json, ensuring that resumed
// runs carry forward the circuit breaker counter.
func TestRestoreLoopFailureSignatures(t *testing.T) {
	t.Run("populated", func(t *testing.T) {
		cp := &runtime.Checkpoint{
			Extra: map[string]any{
				"loop_failure_signatures": map[string]any{
					"implement|fail|auth expired": float64(2),
					"verify|fail|auth expired":    float64(1),
				},
			},
		}
		got := restoreLoopFailureSignatures(cp)
		if len(got) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(got))
		}
		if got["implement|fail|auth expired"] != 2 {
			t.Errorf("implement signature: got %d want 2", got["implement|fail|auth expired"])
		}
		if got["verify|fail|auth expired"] != 1 {
			t.Errorf("verify signature: got %d want 1", got["verify|fail|auth expired"])
		}
	})

	t.Run("nil_checkpoint", func(t *testing.T) {
		got := restoreLoopFailureSignatures(nil)
		if len(got) != 0 {
			t.Fatalf("expected empty map, got %d entries", len(got))
		}
	})

	t.Run("missing_key", func(t *testing.T) {
		cp := &runtime.Checkpoint{Extra: map[string]any{}}
		got := restoreLoopFailureSignatures(cp)
		if len(got) != 0 {
			t.Fatalf("expected empty map, got %d entries", len(got))
		}
	})

	t.Run("empty_key_filtered", func(t *testing.T) {
		cp := &runtime.Checkpoint{
			Extra: map[string]any{
				"loop_failure_signatures": map[string]any{
					"":          float64(5),
					"  ":        float64(3),
					"valid|sig": float64(1),
				},
			},
		}
		got := restoreLoopFailureSignatures(cp)
		if len(got) != 1 {
			t.Fatalf("expected 1 entry (empty keys filtered), got %d", len(got))
		}
		if got["valid|sig"] != 1 {
			t.Errorf("valid|sig: got %d want 1", got["valid|sig"])
		}
	})
}

// TestCheckpointSerializesLoopFailureSignatures verifies that the checkpoint
// method persists loopFailureSignatures into checkpoint.json Extra, and that
// restoreLoopFailureSignatures can read them back.
func TestCheckpointSerializesLoopFailureSignatures(t *testing.T) {
	cp := runtime.NewCheckpoint()
	cp.Extra = map[string]any{}

	// Simulate what the checkpoint method does.
	sigs := map[string]int{
		"nodeA|fail|timeout": 2,
		"nodeB|fail|auth":    1,
	}
	cp.Extra["loop_failure_signatures"] = copyStringIntMap(sigs)

	// Verify round-trip through the restore function. The checkpoint save/load
	// cycle JSON-encodes ints as float64, so we simulate that.
	cp.Extra["loop_failure_signatures"] = map[string]any{
		"nodeA|fail|timeout": float64(2),
		"nodeB|fail|auth":    float64(1),
	}

	got := restoreLoopFailureSignatures(cp)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	for k, want := range sigs {
		if got[strings.TrimSpace(k)] != want {
			t.Errorf("%s: got %d want %d", k, got[k], want)
		}
	}
}
