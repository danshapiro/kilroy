package cxdb

import (
	"strconv"
	"testing"
)

func TestKilroyAttractorRegistryBundle_IncludesRequiredTypes(t *testing.T) {
	id, bundle, sha, err := KilroyAttractorRegistryBundle()
	if err != nil {
		t.Fatalf("KilroyAttractorRegistryBundle: %v", err)
	}
	if id == "" || sha == "" || bundle.BundleID != id {
		t.Fatalf("bundle ids: id=%q sha=%q bundle_id=%q", id, sha, bundle.BundleID)
	}

	required := []string{
		"com.kilroy.attractor.RunStarted",
		"com.kilroy.attractor.RunCompleted",
		"com.kilroy.attractor.RunFailed",
		"com.kilroy.attractor.StageStarted",
		"com.kilroy.attractor.StageFinished",
		"com.kilroy.attractor.ToolCall",
		"com.kilroy.attractor.ToolResult",
		"com.kilroy.attractor.Artifact",
		"com.kilroy.attractor.GitCheckpoint",
		"com.kilroy.attractor.CheckpointSaved",
		"com.kilroy.attractor.BackendTraceRef",
		"com.kilroy.attractor.Blob",
		"com.kilroy.attractor.AssistantMessage",
		"com.kilroy.attractor.Prompt",
	}
	for _, typ := range required {
		if _, ok := bundle.Types[typ]; !ok {
			t.Fatalf("missing type: %s", typ)
		}
	}
}

func TestRegistryBundle_FieldTagsAreNumericAndUnique(t *testing.T) {
	_, bundle, _, err := KilroyAttractorRegistryBundle()
	if err != nil {
		t.Fatalf("KilroyAttractorRegistryBundle: %v", err)
	}
	for typeID, defAny := range bundle.Types {
		def, ok := defAny.(map[string]any)
		if !ok {
			t.Fatalf("%s: type def not an object", typeID)
		}
		versionsAny, ok := def["versions"].(map[string]any)
		if !ok {
			t.Fatalf("%s: missing versions", typeID)
		}
		v1Any, ok := versionsAny["1"].(map[string]any)
		if !ok {
			t.Fatalf("%s: missing versions.1", typeID)
		}
		fieldsAny, ok := v1Any["fields"].(map[string]any)
		if !ok {
			t.Fatalf("%s: missing fields", typeID)
		}
		seen := map[int]bool{}
		for tagStr := range fieldsAny {
			tag, err := strconv.Atoi(tagStr)
			if err != nil || tag <= 0 {
				t.Fatalf("%s: invalid field tag %q", typeID, tagStr)
			}
			if seen[tag] {
				t.Fatalf("%s: duplicate field tag %d", typeID, tag)
			}
			seen[tag] = true
		}
	}
}

