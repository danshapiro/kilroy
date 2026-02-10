package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// appendProgress writes compact, machine-readable progress events under logs_root.
//
// Files:
// - progress.ndjson: append-only stream (one JSON object per line)
// - live.json: last event (overwritten)
//
// This is best-effort: progress logging must never block or fail a run.
func (e *Engine) appendProgress(ev map[string]any) {
	if e == nil {
		return
	}
	sink := e.progressSink
	logsRoot := strings.TrimSpace(e.LogsRoot)
	if ev == nil {
		ev = map[string]any{}
	}
	now := time.Now().UTC()
	if _, ok := ev["ts"]; !ok {
		ev["ts"] = now.Format(time.RFC3339Nano)
	}
	if _, ok := ev["run_id"]; !ok && strings.TrimSpace(e.Options.RunID) != "" {
		ev["run_id"] = e.Options.RunID
	}
	sinkEvent := copyMap(ev)
	if logsRoot == "" {
		if sink != nil {
			sink(sinkEvent)
		}
		return
	}

	b, err := json.Marshal(ev)
	if err != nil {
		return
	}

	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	e.lastProgressAt = now

	// Append to progress.ndjson.
	// Intentionally open/close on each event so writes are immediately flushed
	// and resilient to abrupt process termination.
	if f, err := os.OpenFile(filepath.Join(logsRoot, "progress.ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}

	// Overwrite live.json with the last event.
	_ = os.WriteFile(filepath.Join(logsRoot, "live.json"), append(b, '\n'), 0o644)
	if sink != nil {
		sink(sinkEvent)
	}
}

func (e *Engine) setLastProgressTime(ts time.Time) {
	if e == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	e.progressMu.Lock()
	e.lastProgressAt = ts
	e.progressMu.Unlock()
}

func (e *Engine) lastProgressTime() time.Time {
	if e == nil {
		return time.Time{}
	}
	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	return e.lastProgressAt
}

func copyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCopyAny(v)
	}
	return out
}

func deepCopyAny(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		return copyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = deepCopyAny(typed[i])
		}
		return out
	}

	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil
	}

	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		it := rv.MapRange()
		for it.Next() {
			key := it.Key()
			value := it.Value()
			copied := reflect.ValueOf(deepCopyAny(value.Interface()))
			if !copied.IsValid() {
				out.SetMapIndex(key, reflect.Zero(rv.Type().Elem()))
				continue
			}
			if copied.Type().AssignableTo(rv.Type().Elem()) {
				out.SetMapIndex(key, copied)
				continue
			}
			if copied.Type().ConvertibleTo(rv.Type().Elem()) {
				out.SetMapIndex(key, copied.Convert(rv.Type().Elem()))
				continue
			}
			out.SetMapIndex(key, value)
		}
		return out.Interface()
	case reflect.Slice:
		if rv.IsNil() {
			return v
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			value := rv.Index(i)
			copied := reflect.ValueOf(deepCopyAny(value.Interface()))
			if !copied.IsValid() {
				out.Index(i).Set(reflect.Zero(rv.Type().Elem()))
				continue
			}
			if copied.Type().AssignableTo(rv.Type().Elem()) {
				out.Index(i).Set(copied)
				continue
			}
			if copied.Type().ConvertibleTo(rv.Type().Elem()) {
				out.Index(i).Set(copied.Convert(rv.Type().Elem()))
				continue
			}
			out.Index(i).Set(value)
		}
		return out.Interface()
	default:
		return v
	}
}
