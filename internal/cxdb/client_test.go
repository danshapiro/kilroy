package cxdb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient_CreateAndForkContext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/contexts/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var req map[string]any
		_ = json.Unmarshal(b, &req)
		if got := strings.TrimSpace(anyToString(req["base_turn_id"])); got != "0" {
			t.Fatalf("base_turn_id: got %q want %q", got, "0")
		}
		_ = json.NewEncoder(w).Encode(ContextInfo{ContextID: "1", HeadTurnID: "0", HeadDepth: 0})
	})
	mux.HandleFunc("/v1/contexts/fork", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var req map[string]any
		_ = json.Unmarshal(b, &req)
		if got := strings.TrimSpace(anyToString(req["base_turn_id"])); got != "123" {
			t.Fatalf("base_turn_id: got %q want %q", got, "123")
		}
		_ = json.NewEncoder(w).Encode(ContextInfo{ContextID: "2", HeadTurnID: "123", HeadDepth: 99})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	ci, err := c.CreateContext(ctx, "0")
	if err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	if ci.ContextID != "1" || ci.HeadTurnID != "0" {
		t.Fatalf("CreateContext: got %+v", ci)
	}

	ci2, err := c.ForkContext(ctx, "123")
	if err != nil {
		t.Fatalf("ForkContext: %v", err)
	}
	if ci2.ContextID != "2" || ci2.HeadTurnID != "123" {
		t.Fatalf("ForkContext: got %+v", ci2)
	}
}

func TestClient_AppendAndListTurns(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/contexts/1/append", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var req map[string]any
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatalf("unmarshal append request: %v", err)
		}
		if req["payload"] != nil {
			t.Fatalf("append request must not use legacy payload field")
		}
		if strings.TrimSpace(anyToString(req["type_id"])) == "" {
			t.Fatalf("append request missing type_id")
		}
		if anyToInt(req["type_version"]) != 1 {
			t.Fatalf("append request type_version: got %v", req["type_version"])
		}
		if _, ok := req["data"].(map[string]any); !ok {
			t.Fatalf("append request missing data object")
		}
		_ = json.NewEncoder(w).Encode(AppendTurnResponse{
			ContextID:   "1",
			TurnID:      "10",
			Depth:       1,
			ContentHash: "abc123",
		})
	})
	mux.HandleFunc("/v1/contexts/1/turns", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{
					"context_id":   "1",
					"head_turn_id": "10",
					"head_depth":   1,
				},
				"turns": []map[string]any{
					{
						"turn_id":        "10",
						"parent_turn_id": "0",
						"depth":          1,
						"declared_type": map[string]any{
							"type_id":      "com.kilroy.attractor.RunStarted",
							"type_version": 1,
						},
						"data": map[string]any{
							"run_id": "r1",
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := c.AppendTurn(ctx, "1", AppendTurnRequest{
		TypeID:       "com.kilroy.attractor.RunStarted",
		TypeVersion:  1,
		Data:         map[string]any{"run_id": "r1"},
		ParentTurnID: "0",
	})
	if err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if resp.TurnID != "10" {
		t.Fatalf("AppendTurn turn_id: got %q", resp.TurnID)
	}
	if resp.ContentHash != "abc123" {
		t.Fatalf("AppendTurn content hash: got %q want %q", resp.ContentHash, "abc123")
	}

	turns, err := c.ListTurns(ctx, "1", ListTurnsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(turns) != 1 || turns[0].TurnID != "10" || turns[0].TypeID != "com.kilroy.attractor.RunStarted" {
		t.Fatalf("ListTurns: %+v", turns)
	}
}

func TestClient_AppendTurn_FallsBackToTurnsWithPayload(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/contexts/1/append", func(w http.ResponseWriter, r *http.Request) {
		// Force the fallback path.
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/v1/contexts/1/turns", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var req map[string]any
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatalf("unmarshal append request: %v", err)
		}
		if req["data"] != nil {
			t.Fatalf("append request must not use data field for /turns")
		}
		if _, ok := req["payload"].(map[string]any); !ok {
			t.Fatalf("append request missing payload object")
		}
		if anyToString(req["parent_turn_id"]) != "0" {
			t.Fatalf("parent_turn_id: got %q want %q", anyToString(req["parent_turn_id"]), "0")
		}
		_ = json.NewEncoder(w).Encode(AppendTurnResponse{
			ContextID:   "1",
			TurnID:      "11",
			Depth:       2,
			ContentHash: "def456",
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := c.AppendTurn(ctx, "1", AppendTurnRequest{
		TypeID:       "com.kilroy.attractor.RunStarted",
		TypeVersion:  1,
		Data:         map[string]any{"run_id": "r1"},
		ParentTurnID: "0",
	})
	if err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if resp.TurnID != "11" {
		t.Fatalf("AppendTurn turn_id: got %q", resp.TurnID)
	}
	if resp.ContentHash != "def456" {
		t.Fatalf("AppendTurn content hash: got %q want %q", resp.ContentHash, "def456")
	}
}

func TestClient_GetContext_FallsBackToListContexts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/contexts/1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/v1/contexts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode([]ContextInfo{
			{ContextID: "1", HeadTurnID: "999", HeadDepth: 123},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ci, err := c.GetContext(ctx, "1")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if ci.ContextID != "1" || ci.HeadTurnID != "999" {
		t.Fatalf("GetContext: %+v", ci)
	}
}
