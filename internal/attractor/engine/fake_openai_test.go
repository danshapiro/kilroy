package engine

import (
	"encoding/json"
	"io"
	"net/http"
)

// writeOpenAIResponseAuto detects whether the request has "stream": true and
// writes either an SSE streaming response or a plain JSON response accordingly.
// This allows fake test servers to handle both Complete() and Stream() calls.
func writeOpenAIResponseAuto(w http.ResponseWriter, r *http.Request, responseObj map[string]any) {
	b, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	var reqBody map[string]any
	_ = json.Unmarshal(b, &reqBody)
	streaming, _ := reqBody["stream"].(bool)

	if !streaming {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responseObj)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	completedPayload, _ := json.Marshal(map[string]any{
		"type":     "response.completed",
		"response": responseObj,
	})

	lines := []string{
		"event: response.completed",
		"data: " + string(completedPayload),
		"",
	}
	for _, line := range lines {
		_, _ = w.Write([]byte(line + "\n"))
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
