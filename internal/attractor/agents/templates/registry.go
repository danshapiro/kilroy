// Template registry for resolving tool names to invocation templates.
package templates

// Registry maps tool names to invocation templates.
type Registry struct {
	templates map[string]Template
}

// DefaultRegistry returns a registry with all built-in tool templates.
func DefaultRegistry() *Registry {
	r := &Registry{templates: map[string]Template{}}
	r.Register(Claude())
	r.Register(Codex())
	r.Register(Gemini())
	r.Register(OpenCode())
	return r
}

// Register adds a template to the registry.
func (r *Registry) Register(t Template) {
	r.templates[t.Name] = t
}

// Get returns the template for the given tool name, or nil if not found.
func (r *Registry) Get(name string) *Template {
	if t, ok := r.templates[name]; ok {
		return &t
	}
	return nil
}

// Names returns all registered template names.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.templates))
	for name := range r.templates {
		names = append(names, name)
	}
	return names
}
