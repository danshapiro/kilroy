package validate

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func loadCreateRunfileTemplateMap(t *testing.T) map[string]any {
	t.Helper()
	p := filepath.Join(findRepoRoot(t), "skills", "create-runfile", "reference_run_template.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read create-runfile template: %v", err)
	}
	var data map[string]any
	if err := yaml.Unmarshal(b, &data); err != nil {
		t.Fatalf("parse create-runfile template: %v", err)
	}
	return data
}

func asMap(t *testing.T, v any, path string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s: expected map[string]any, got %T", path, v)
	}
	return m
}

func asSlice(t *testing.T, v any, path string) []any {
	t.Helper()
	s, ok := v.([]any)
	if !ok {
		t.Fatalf("%s: expected []any, got %T", path, v)
	}
	return s
}

func TestCreateRunfileTemplate_IncludesOperatorMetadata(t *testing.T) {
	template := loadCreateRunfileTemplateMap(t)
	if _, ok := template["graph"]; !ok {
		t.Fatal("template must include 'graph' operator metadata field")
	}
	if _, ok := template["task"]; !ok {
		t.Fatal("template must include 'task' operator metadata field")
	}
}

func TestCreateRunfileTemplate_OverridesMatchProfileDefaults(t *testing.T) {
	repoRoot := findRepoRoot(t)

	pdeBytes, err := os.ReadFile(filepath.Join(repoRoot, "skills", "shared", "profile_default_env.yaml"))
	if err != nil {
		t.Fatalf("read profile_default_env.yaml: %v", err)
	}
	var pdeFile struct {
		Profiles map[string]map[string]string `yaml:"profiles"`
	}
	if err := yaml.Unmarshal(pdeBytes, &pdeFile); err != nil {
		t.Fatalf("parse profile_default_env.yaml: %v", err)
	}

	template := loadCreateRunfileTemplateMap(t)
	artifactPolicy := asMap(t, template["artifact_policy"], "artifact_policy")
	envMap := asMap(t, artifactPolicy["env"], "artifact_policy.env")
	overrides := asMap(t, envMap["overrides"], "artifact_policy.env.overrides")

	// Only check profiles the template declares in artifact_policy.profiles.
	templateProfiles := asSlice(t, artifactPolicy["profiles"], "artifact_policy.profiles")
	declaredProfiles := map[string]struct{}{}
	for _, p := range templateProfiles {
		if s, ok := p.(string); ok {
			declaredProfiles[s] = struct{}{}
		}
	}

	for profile, envVars := range pdeFile.Profiles {
		if len(envVars) == 0 {
			continue
		}
		if _, declared := declaredProfiles[profile]; !declared {
			continue
		}
		profileOverridesRaw, ok := overrides[profile]
		if !ok {
			t.Errorf("template missing overrides for declared profile %q which has env vars in profile_default_env.yaml", profile)
			continue
		}
		profileOverrides := asMap(t, profileOverridesRaw, "artifact_policy.env.overrides."+profile)
		for k, v := range envVars {
			got, ok := profileOverrides[k]
			if !ok {
				t.Errorf("template missing %s override %q (expected %q)", profile, k, v)
				continue
			}
			if gotStr, ok := got.(string); !ok || gotStr != v {
				t.Errorf("template %s override %q = %v, want %q", profile, k, got, v)
			}
		}
	}
}

func TestCreateRunfileTemplate_UsesArtifactPolicyCheckpointExcludes(t *testing.T) {
	template := loadCreateRunfileTemplateMap(t)

	gitMap := asMap(t, template["git"], "git")
	if _, ok := gitMap["checkpoint_exclude_globs"]; ok {
		t.Fatal("template must not use deprecated git.checkpoint_exclude_globs")
	}

	artifactPolicy := asMap(t, template["artifact_policy"], "artifact_policy")
	checkpoint := asMap(t, artifactPolicy["checkpoint"], "artifact_policy.checkpoint")
	excludes := asSlice(t, checkpoint["exclude_globs"], "artifact_policy.checkpoint.exclude_globs")
	if len(excludes) == 0 {
		t.Fatal("artifact_policy.checkpoint.exclude_globs must not be empty")
	}
}

func TestCreateRunfileTemplate_IncludesInputMaterializationContract(t *testing.T) {
	template := loadCreateRunfileTemplateMap(t)
	inputs := asMap(t, template["inputs"], "inputs")
	materialize := asMap(t, inputs["materialize"], "inputs.materialize")

	if _, ok := materialize["enabled"]; !ok {
		t.Fatal("template must include inputs.materialize.enabled")
	}
	if _, ok := materialize["imports"]; !ok {
		t.Fatal("template must include inputs.materialize.imports")
	}
	if _, ok := materialize["include"]; ok {
		t.Fatal("template must not include inputs.materialize.include when imports schema is used")
	}
	if _, ok := materialize["default_include"]; ok {
		t.Fatal("template must not include inputs.materialize.default_include when imports schema is used")
	}
	fanIn := asMap(t, materialize["fan_in"], "inputs.materialize.fan_in")
	promoteRunScoped := asSlice(t, fanIn["promote_run_scoped"], "inputs.materialize.fan_in.promote_run_scoped")
	if len(promoteRunScoped) != 0 {
		t.Fatal("template default must not opt into fan-in promotion")
	}
	if _, ok := materialize["follow_references"]; !ok {
		t.Fatal("template must include inputs.materialize.follow_references")
	}
	if _, ok := materialize["infer_with_llm"]; !ok {
		t.Fatal("template must include inputs.materialize.infer_with_llm")
	}
}
