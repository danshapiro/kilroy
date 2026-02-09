package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/strongdm/kilroy/internal/providerspec"
)

type ProviderRuntime struct {
	Key           string
	Backend       BackendKind
	Executable    string
	API           providerspec.APISpec
	CLI           *providerspec.CLISpec
	APIHeadersMap map[string]string
	Failover      []string
	ProfileFamily string
}

func (r ProviderRuntime) APIHeaders() map[string]string {
	return cloneStringMap(r.APIHeadersMap)
}

func resolveProviderRuntimes(cfg *RunConfigFile) (map[string]ProviderRuntime, error) {
	out := map[string]ProviderRuntime{}
	originByCanonical := map[string]string{}
	if cfg == nil {
		return out, nil
	}
	rawKeys := make([]string, 0, len(cfg.LLM.Providers))
	for rawKey := range cfg.LLM.Providers {
		rawKeys = append(rawKeys, rawKey)
	}
	sort.Strings(rawKeys)
	for _, rawKey := range rawKeys {
		pc := cfg.LLM.Providers[rawKey]
		key := providerspec.CanonicalProviderKey(rawKey)
		if key == "" {
			continue
		}
		if prevRaw, dup := originByCanonical[key]; dup {
			return nil, fmt.Errorf("duplicate provider config after canonicalization: %q and %q both map to %q", prevRaw, rawKey, key)
		}
		originByCanonical[key] = rawKey

		builtin, _ := providerspec.Builtin(key)
		rt := ProviderRuntime{
			Key:        key,
			Backend:    pc.Backend,
			Executable: strings.TrimSpace(pc.Executable),
			CLI:        cloneCLISpec(builtin.CLI),
		}
		if builtin.API != nil {
			rt.API = *builtin.API
		}
		if p := strings.TrimSpace(pc.API.Protocol); p != "" {
			rt.API.Protocol = providerspec.APIProtocol(p)
		}
		if v := strings.TrimSpace(pc.API.BaseURL); v != "" {
			rt.API.DefaultBaseURL = v
		}
		if v := strings.TrimSpace(pc.API.Path); v != "" {
			rt.API.DefaultPath = v
		}
		if v := strings.TrimSpace(pc.API.APIKeyEnv); v != "" {
			rt.API.DefaultAPIKeyEnv = v
		}
		if v := strings.TrimSpace(pc.API.ProviderOptionsKey); v != "" {
			rt.API.ProviderOptionsKey = v
		}
		if v := strings.TrimSpace(pc.API.ProfileFamily); v != "" {
			rt.API.ProfileFamily = v
		}
		rt.APIHeadersMap = cloneStringMap(pc.API.Headers)
		rt.ProfileFamily = rt.API.ProfileFamily
		if len(pc.Failover) > 0 {
			rt.Failover = providerspec.CanonicalizeProviderList(pc.Failover)
		} else if len(builtin.Failover) > 0 {
			rt.Failover = providerspec.CanonicalizeProviderList(builtin.Failover)
		}
		out[key] = rt
	}

	// Synthesize builtin failover targets recursively so fallback chains can resolve
	// without requiring explicit config entries for each target.
	queue := make([]string, 0, len(out))
	for key := range out {
		queue = append(queue, key)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		rt := out[cur]
		for _, target := range rt.Failover {
			if _, ok := out[target]; ok {
				continue
			}
			builtin, ok := providerspec.Builtin(target)
			if !ok {
				continue
			}
			synth := ProviderRuntime{
				Key:        target,
				Backend:    defaultBackendForSpec(builtin),
				Executable: "",
				CLI:        cloneCLISpec(builtin.CLI),
				Failover:   providerspec.CanonicalizeProviderList(builtin.Failover),
			}
			if builtin.API != nil {
				synth.API = *builtin.API
				synth.ProfileFamily = builtin.API.ProfileFamily
			}
			out[target] = synth
			queue = append(queue, target)
		}
	}
	return out, nil
}

func defaultBackendForSpec(spec providerspec.Spec) BackendKind {
	if spec.API != nil {
		return BackendAPI
	}
	return BackendCLI
}

func cloneCLISpec(in *providerspec.CLISpec) *providerspec.CLISpec {
	if in == nil {
		return nil
	}
	cp := *in
	cp.InvocationTemplate = append([]string{}, in.InvocationTemplate...)
	cp.HelpProbeArgs = append([]string{}, in.HelpProbeArgs...)
	cp.CapabilityAll = append([]string{}, in.CapabilityAll...)
	if len(in.CapabilityAnyOf) > 0 {
		cp.CapabilityAnyOf = make([][]string, 0, len(in.CapabilityAnyOf))
		for _, group := range in.CapabilityAnyOf {
			cp.CapabilityAnyOf = append(cp.CapabilityAnyOf, append([]string{}, group...))
		}
	}
	return &cp
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
