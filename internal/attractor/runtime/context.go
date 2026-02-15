package runtime

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Context is the shared key-value store for a pipeline run.
// Values must be JSON-serializable for checkpointing.
type Context struct {
	mu     sync.RWMutex
	values map[string]any
	logs   []string
}

func NewContext() *Context {
	return &Context{
		values: map[string]any{},
		logs:   []string{},
	}
}

func (c *Context) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = map[string]any{}
	}
	c.values[key] = value
}

func (c *Context) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.values[key]
	return v, ok
}

func (c *Context) GetString(key string, def string) string {
	v, ok := c.Get(key)
	if !ok || v == nil {
		return def
	}
	return fmt.Sprint(v)
}

func (c *Context) AppendLog(entry string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, entry)
}

func (c *Context) SnapshotValues() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]any, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

func (c *Context) SnapshotLogs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.logs))
	copy(out, c.logs)
	return out
}

// Clone returns a deep copy of the context for parallel branch isolation.
// Values are deep-copied via JSON round-trip (all context values must be
// JSON-serializable per the Context contract). If JSON serialization fails
// for any value, that value is shallow-copied as a fallback.
func (c *Context) Clone() *Context {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := NewContext()
	for k, v := range c.values {
		out.values[k] = deepCopyValue(v)
	}
	out.logs = append(out.logs, c.logs...)
	return out
}

// deepCopyValue performs a deep copy of a value via JSON round-trip.
// This is safe because all context values must be JSON-serializable.
// Falls back to returning the original value if marshaling fails (e.g.,
// for primitive types like strings and ints that don't need deep copying).
func deepCopyValue(v any) any {
	if v == nil {
		return nil
	}
	// Fast path: primitive types that are immutable and don't need deep copying.
	switch v.(type) {
	case string, bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return v
	}
	// Deep copy via JSON round-trip for composite types (maps, slices, etc.).
	b, err := json.Marshal(v)
	if err != nil {
		return v // fallback: shallow copy
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v // fallback: shallow copy
	}
	return out
}

func (c *Context) ApplyUpdates(updates map[string]any) {
	if len(updates) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range updates {
		if c.values == nil {
			c.values = map[string]any{}
		}
		c.values[k] = v
	}
}

func (c *Context) ReplaceSnapshot(values map[string]any, logs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values = map[string]any{}
	for k, v := range values {
		c.values[k] = v
	}
	c.logs = append([]string{}, logs...)
}
