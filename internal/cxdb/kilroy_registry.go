package cxdb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type RegistryBundle struct {
	RegistryVersion int            `json:"registry_version"`
	BundleID        string         `json:"bundle_id"`
	Types           map[string]any `json:"types"`
	Enums           map[string]any `json:"enums,omitempty"`
}

// KilroyAttractorRegistryBundle returns a minimal registry bundle implementing the metaspec-required
// types for Kilroy Attractor event turns.
func KilroyAttractorRegistryBundle() (bundleID string, bundle RegistryBundle, sha256hex string, err error) {
	bundle = RegistryBundle{
		RegistryVersion: 1,
		BundleID:        "",
		Types: map[string]any{
			"com.kilroy.attractor.RunStarted": typeDef(map[string]any{
				"1":  field("run_id", "string"),
				"2":  fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"3":  field("repo_path", "string"),
				"4":  field("base_sha", "string"),
				"5":  field("run_branch", "string"),
				"6":  field("logs_root", "string"),
				"7":  field("worktree_dir", "string"),
				"8":  field("graph_name", "string", opt()),
				"9":  field("goal", "string", opt()),
				"10": field("modeldb_catalog_sha256", "string", opt()),
				"11": field("modeldb_catalog_source", "string", opt()),
				"12": field("graph_dot", "string", opt()),
			}),
			"com.kilroy.attractor.RunCompleted": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"3": field("final_status", "string"),
				"4": field("final_git_commit_sha", "string"),
				"5": field("cxdb_context_id", "string", opt()),
				"6": field("cxdb_head_turn_id", "string", opt()),
			}),
			"com.kilroy.attractor.RunFailed": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"3": field("reason", "string"),
				"4": field("node_id", "string", opt()),
				"5": field("git_commit_sha", "string", opt()),
			}),
			"com.kilroy.attractor.StageStarted": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("handler_type", "string", opt()),
				"5": field("attempt", "u32", opt()),
			}),
			"com.kilroy.attractor.StageFinished": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("status", "string"),
				"5": field("preferred_label", "string", opt()),
				"6": field("failure_reason", "string", opt()),
				"7": field("notes", "string", opt()),
				"8": fieldArray("suggested_next_ids", "string", opt()),
			}),
			"com.kilroy.attractor.ToolCall": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string", opt()),
				"3": field("tool_name", "string"),
				"4": field("call_id", "string"),
				"5": field("arguments_json", "string", opt()),
			}),
			"com.kilroy.attractor.ToolResult": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string", opt()),
				"3": field("tool_name", "string"),
				"4": field("call_id", "string"),
				"5": field("output", "string", opt()),
				"6": field("is_error", "bool", opt()),
			}),
			"com.kilroy.attractor.Blob": typeDef(map[string]any{
				"1": field("bytes", "bytes"),
			}),
			"com.kilroy.attractor.Artifact": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string", opt()),
				"3": field("name", "string"),
				"4": field("mime", "string", opt()),
				"5": field("content_hash", "string"),
				"6": field("bytes_len", "u64", opt()),
				"7": field("local_path", "string", opt()),
			}),
			"com.kilroy.attractor.GitCheckpoint": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": field("status", "string"),
				"4": field("git_commit_sha", "string"),
				"5": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
			}),
			"com.kilroy.attractor.CheckpointSaved": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"3": field("checkpoint_path", "string"),
				"4": field("cxdb_context_id", "string"),
				"5": field("cxdb_head_turn_id", "string"),
				"6": field("node_id", "string", opt()),
			}),
			"com.kilroy.attractor.AssistantMessage": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string", opt()),
				"3": field("text", "string", opt()),
				"4": field("model", "string", opt()),
				"5": fieldSemantic("input_tokens", "u64", "count", opt()),
				"6": fieldSemantic("output_tokens", "u64", "count", opt()),
				"7": field("tool_use_count", "u32", opt()),
				"8": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
			}),
			"com.kilroy.attractor.Prompt": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": field("text", "string"),
				"4": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
			}),
			"com.kilroy.attractor.BackendTraceRef": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string", opt()),
				"3": field("provider", "string"),
				"4": field("backend", "string"),
				"5": field("description", "string", opt()),
				"6": field("artifact_hash", "string", opt()),
			}),
			// Spec §9.6: Stage lifecycle events.
			"com.kilroy.attractor.StageFailed": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("failure_reason", "string"),
				"5": field("will_retry", "bool", opt()),
				"6": field("attempt", "u32", opt()),
			}),
			"com.kilroy.attractor.StageRetrying": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("attempt", "u32"),
				"5": fieldSemantic("delay_ms", "u64", "duration_ms", opt()),
			}),
			// Spec §9.6: Parallel execution events.
			"com.kilroy.attractor.ParallelStarted": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("branch_count", "u32"),
				"5": field("join_policy", "string", opt()),
				"6": field("error_policy", "string", opt()),
			}),
			"com.kilroy.attractor.ParallelBranchStarted": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("branch_key", "string"),
				"5": field("branch_index", "u32"),
			}),
			"com.kilroy.attractor.ParallelBranchCompleted": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("branch_key", "string"),
				"5": field("branch_index", "u32"),
				"6": field("status", "string"),
				"7": fieldSemantic("duration_ms", "u64", "duration_ms", opt()),
			}),
			"com.kilroy.attractor.ParallelCompleted": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("success_count", "u32"),
				"5": field("failure_count", "u32"),
				"6": fieldSemantic("duration_ms", "u64", "duration_ms", opt()),
			}),
			// Spec §9.6: Human interaction events.
			"com.kilroy.attractor.InterviewStarted": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("question_text", "string", opt()),
				"5": field("question_type", "string", opt()),
			}),
			"com.kilroy.attractor.InterviewCompleted": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("answer_value", "string", opt()),
				"5": fieldSemantic("duration_ms", "u64", "duration_ms", opt()),
			}),
			"com.kilroy.attractor.InterviewTimeout": typeDef(map[string]any{
				"1": field("run_id", "string"),
				"2": field("node_id", "string"),
				"3": fieldSemantic("timestamp_ms", "u64", "unix_ms"),
				"4": field("question_text", "string", opt()),
				"5": fieldSemantic("duration_ms", "u64", "duration_ms", opt()),
			}),
		},
		Enums: map[string]any{},
	}

	raw, err := json.Marshal(bundle)
	if err != nil {
		return "", RegistryBundle{}, "", err
	}
	sum := sha256.Sum256(raw)
	sha256hex = hex.EncodeToString(sum[:])
	bundleID = fmt.Sprintf("kilroy-attractor-v1#%s", sha256hex[:12])
	bundle.BundleID = bundleID
	return bundleID, bundle, sha256hex, nil
}

func typeDef(fields map[string]any) map[string]any {
	return map[string]any{
		"versions": map[string]any{
			"1": map[string]any{
				"fields": fields,
			},
		},
	}
}

func field(name, typ string, opts ...map[string]any) map[string]any {
	out := map[string]any{"name": name, "type": typ}
	for _, o := range opts {
		for k, v := range o {
			out[k] = v
		}
	}
	return out
}

func fieldSemantic(name, typ, semantic string, opts ...map[string]any) map[string]any {
	out := field(name, typ, opts...)
	out["semantic"] = semantic
	return out
}

func fieldArray(name, itemsType string, opts ...map[string]any) map[string]any {
	out := map[string]any{
		"name":  name,
		"type":  "array",
		"items": itemsType,
	}
	for _, o := range opts {
		for k, v := range o {
			out[k] = v
		}
	}
	return out
}

func opt() map[string]any { return map[string]any{"optional": true} }
