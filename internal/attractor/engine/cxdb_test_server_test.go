package engine

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zeebo/blake3"
)

type cxdbTestServer struct {
	srv *httptest.Server
	bin net.Listener

	mu sync.Mutex

	nextContextID int
	nextTurnID    int
	nextSessionID atomic.Uint64

	contexts map[string]*cxdbContextState
	bundles  map[string]any
	blobs    map[[32]byte][]byte
}

type cxdbContextState struct {
	ContextID  string
	HeadTurnID string
	HeadDepth  int
	Turns      []map[string]any

	idempotency map[string]map[string]map[string]any // path -> key -> response
}

func newCXDBTestServer(t *testing.T) *cxdbTestServer {
	t.Helper()

	s := &cxdbTestServer{
		nextContextID: 1,
		nextTurnID:    1,
		contexts:      map[string]*cxdbContextState{},
		bundles:       map[string]any{},
		blobs:         map[[32]byte][]byte{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/registry/bundles/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/registry/bundles/")
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var body any
		_ = json.Unmarshal(b, &body)
		s.mu.Lock()
		s.bundles[id] = body
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})

	handleContextCreate := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var req map[string]any
		_ = json.Unmarshal(b, &req)
		baseTurnID := strings.TrimSpace(anyToString(req["base_turn_id"]))
		if baseTurnID == "" {
			baseTurnID = "0"
		}

		s.mu.Lock()
		id := strconv.Itoa(s.nextContextID)
		s.nextContextID++
		s.contexts[id] = &cxdbContextState{
			ContextID:   id,
			HeadTurnID:  baseTurnID,
			HeadDepth:   0,
			Turns:       []map[string]any{},
			idempotency: map[string]map[string]map[string]any{},
		}
		ci := s.contexts[id]
		resp := map[string]any{
			"context_id":   ci.ContextID,
			"head_turn_id": ci.HeadTurnID,
			"head_depth":   ci.HeadDepth,
		}
		s.mu.Unlock()

		_ = json.NewEncoder(w).Encode(resp)
	}

	mux.HandleFunc("/v1/contexts/create", handleContextCreate)
	mux.HandleFunc("/v1/contexts/fork", handleContextCreate)
	mux.HandleFunc("/v1/contexts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleContextCreate(w, r) // legacy compat
		case http.MethodGet:
			s.mu.Lock()
			out := make([]map[string]any, 0, len(s.contexts))
			for _, c := range s.contexts {
				out = append(out, map[string]any{
					"context_id":   c.ContextID,
					"head_turn_id": c.HeadTurnID,
					"head_depth":   c.HeadDepth,
				})
			}
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/v1/contexts/", func(w http.ResponseWriter, r *http.Request) {
		// /v1/contexts/{id} or /v1/contexts/{id}/turns|append
		rest := strings.TrimPrefix(r.URL.Path, "/v1/contexts/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ctxID := parts[0]
		if len(parts) == 1 {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			s.mu.Lock()
			ci := s.contexts[ctxID]
			resp := map[string]any(nil)
			if ci != nil {
				resp = map[string]any{
					"context_id":   ci.ContextID,
					"head_turn_id": ci.HeadTurnID,
					"head_depth":   ci.HeadDepth,
				}
			}
			s.mu.Unlock()
			if resp == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		if len(parts) == 2 && parts[1] == "append" {
			switch r.Method {
			case http.MethodPost:
				b, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()
				var req map[string]any
				_ = json.Unmarshal(b, &req)
				typeID := strings.TrimSpace(anyToString(req["type_id"]))
				typeVer := anyToInt(req["type_version"])
				parent := strings.TrimSpace(anyToString(req["parent_turn_id"]))
				idemKey := strings.TrimSpace(anyToString(req["idempotency_key"]))
				data, _ := req["data"].(map[string]any)
				if data == nil {
					data = map[string]any{}
				}

				s.mu.Lock()
				ci := s.contexts[ctxID]
				if ci == nil {
					s.mu.Unlock()
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if ci.idempotency == nil {
					ci.idempotency = map[string]map[string]map[string]any{}
				}
				if strings.TrimSpace(idemKey) != "" {
					if ci.idempotency["append"] == nil {
						ci.idempotency["append"] = map[string]map[string]any{}
					}
					if resp, ok := ci.idempotency["append"][idemKey]; ok {
						s.mu.Unlock()
						_ = json.NewEncoder(w).Encode(resp)
						return
					}
				}
				turnID := strconv.Itoa(s.nextTurnID)
				s.nextTurnID++
				ci.HeadDepth++
				ci.HeadTurnID = turnID
				depth := ci.HeadDepth
				m := map[string]any{
					"turn_id":        turnID,
					"parent_turn_id": parent,
					"depth":          depth,
					"type_id":        typeID,
					"type_version":   typeVer,
					"payload":        data,
					"payload_hash":   "h" + turnID,
				}
				ci.Turns = append(ci.Turns, m)
				resp := map[string]any{
					"context_id":   ctxID,
					"turn_id":      turnID,
					"depth":        depth,
					"content_hash": "h" + turnID,
				}
				if strings.TrimSpace(idemKey) != "" {
					ci.idempotency["append"][idemKey] = resp
				}
				s.mu.Unlock()

				_ = json.NewEncoder(w).Encode(resp)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
			return
		}
		if len(parts) == 2 && parts[1] == "turns" {
			switch r.Method {
			case http.MethodPost:
				// Backward-compat: /turns expects JSON "payload" rather than "data".
				b, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()
				var req map[string]any
				_ = json.Unmarshal(b, &req)
				typeID := strings.TrimSpace(anyToString(req["type_id"]))
				typeVer := anyToInt(req["type_version"])
				parent := strings.TrimSpace(anyToString(req["parent_turn_id"]))
				idemKey := strings.TrimSpace(anyToString(req["idempotency_key"]))
				payload, _ := req["payload"].(map[string]any)
				if payload == nil {
					payload = map[string]any{}
				}

				s.mu.Lock()
				ci := s.contexts[ctxID]
				if ci == nil {
					s.mu.Unlock()
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if ci.idempotency == nil {
					ci.idempotency = map[string]map[string]map[string]any{}
				}
				if strings.TrimSpace(idemKey) != "" {
					if ci.idempotency["turns"] == nil {
						ci.idempotency["turns"] = map[string]map[string]any{}
					}
					if resp, ok := ci.idempotency["turns"][idemKey]; ok {
						s.mu.Unlock()
						_ = json.NewEncoder(w).Encode(resp)
						return
					}
				}
				turnID := strconv.Itoa(s.nextTurnID)
				s.nextTurnID++
				ci.HeadDepth++
				ci.HeadTurnID = turnID
				depth := ci.HeadDepth
				m := map[string]any{
					"turn_id":        turnID,
					"parent_turn_id": parent,
					"depth":          depth,
					"type_id":        typeID,
					"type_version":   typeVer,
					"payload":        payload,
					"payload_hash":   "h" + turnID,
				}
				ci.Turns = append(ci.Turns, m)
				resp := map[string]any{
					"context_id":   ctxID,
					"turn_id":      turnID,
					"depth":        depth,
					"content_hash": "h" + turnID,
				}
				if strings.TrimSpace(idemKey) != "" {
					ci.idempotency["turns"][idemKey] = resp
				}
				s.mu.Unlock()

				_ = json.NewEncoder(w).Encode(resp)
			case http.MethodGet:
				s.mu.Lock()
				ci := s.contexts[ctxID]
				var rawTurns []map[string]any
				head := ""
				depth := 0
				if ci != nil {
					rawTurns = append([]map[string]any{}, ci.Turns...)
					head = ci.HeadTurnID
					depth = ci.HeadDepth
				}
				s.mu.Unlock()
				outTurns := make([]map[string]any, 0, len(rawTurns))
				for _, tr := range rawTurns {
					outTurns = append(outTurns, map[string]any{
						"turn_id":        tr["turn_id"],
						"parent_turn_id": tr["parent_turn_id"],
						"depth":          tr["depth"],
						"declared_type": map[string]any{
							"type_id":      tr["type_id"],
							"type_version": tr["type_version"],
						},
						"data": tr["payload"],
					})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"meta": map[string]any{
						"context_id":   ctxID,
						"head_turn_id": head,
						"head_depth":   depth,
					},
					"turns": outTurns,
				})
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen binary: %v", err)
	}
	s.bin = ln
	t.Cleanup(func() { _ = ln.Close() })
	go s.serveBinary()

	return s
}

func (s *cxdbTestServer) URL() string { return s.srv.URL }
func (s *cxdbTestServer) BinaryAddr() string {
	if s == nil || s.bin == nil {
		return ""
	}
	return s.bin.Addr().String()
}

func (s *cxdbTestServer) Turns(contextID string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	ci := s.contexts[contextID]
	if ci == nil {
		return nil
	}
	return append([]map[string]any{}, ci.Turns...)
}

func (s *cxdbTestServer) ContextIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.contexts))
	for id := range s.contexts {
		out = append(out, id)
	}
	return out
}

type binFrameHeader struct {
	Len     uint32
	MsgType uint16
	Flags   uint16
	ReqID   uint64
}

func readBinFrame(r io.Reader) (binFrameHeader, []byte, error) {
	var hdrBuf [16]byte
	if _, err := io.ReadFull(r, hdrBuf[:]); err != nil {
		return binFrameHeader{}, nil, err
	}
	h := binFrameHeader{
		Len:     binary.LittleEndian.Uint32(hdrBuf[0:4]),
		MsgType: binary.LittleEndian.Uint16(hdrBuf[4:6]),
		Flags:   binary.LittleEndian.Uint16(hdrBuf[6:8]),
		ReqID:   binary.LittleEndian.Uint64(hdrBuf[8:16]),
	}
	payload := make([]byte, int(h.Len))
	if _, err := io.ReadFull(r, payload); err != nil {
		return binFrameHeader{}, nil, err
	}
	return h, payload, nil
}

func writeBinFrame(w io.Writer, msgType uint16, flags uint16, reqID uint64, payload []byte) error {
	var hdrBuf [16]byte
	binary.LittleEndian.PutUint32(hdrBuf[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint16(hdrBuf[4:6], msgType)
	binary.LittleEndian.PutUint16(hdrBuf[6:8], flags)
	binary.LittleEndian.PutUint64(hdrBuf[8:16], reqID)
	if _, err := w.Write(hdrBuf[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

func writeBinError(w io.Writer, reqID uint64, code uint32, detail string) error {
	detailBytes := []byte(detail)
	var payload []byte
	payload = make([]byte, 8+len(detailBytes))
	binary.LittleEndian.PutUint32(payload[0:4], code)
	binary.LittleEndian.PutUint32(payload[4:8], uint32(len(detailBytes)))
	copy(payload[8:], detailBytes)
	return writeBinFrame(w, 255, 0, reqID, payload)
}

func (s *cxdbTestServer) serveBinary() {
	for {
		conn, err := s.bin.Accept()
		if err != nil {
			return
		}
		go s.handleBinaryConn(conn)
	}
}

func (s *cxdbTestServer) handleBinaryConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	for {
		h, payload, err := readBinFrame(conn)
		if err != nil {
			return
		}
		switch h.MsgType {
		case 1: // HELLO
			// protocol_version(u32) + client_tag_len(u32) + client_tag
			if len(payload) < 8 {
				_ = writeBinError(conn, h.ReqID, 400, "hello: short payload")
				continue
			}
			ver := binary.LittleEndian.Uint32(payload[0:4])
			tagLen := binary.LittleEndian.Uint32(payload[4:8])
			if ver != 1 {
				_ = writeBinError(conn, h.ReqID, 422, fmt.Sprintf("hello: unsupported protocol_version=%d", ver))
				continue
			}
			if int(8+tagLen) > len(payload) {
				_ = writeBinError(conn, h.ReqID, 400, "hello: client_tag_len out of range")
				continue
			}
			_ = payload[8 : 8+tagLen] // ignore tag

			sessionID := s.nextSessionID.Add(1)
			serverTag := []byte("cxdb-test")

			resp := make([]byte, 4+8+4+len(serverTag))
			binary.LittleEndian.PutUint32(resp[0:4], 1)
			binary.LittleEndian.PutUint64(resp[4:12], sessionID)
			binary.LittleEndian.PutUint32(resp[12:16], uint32(len(serverTag)))
			copy(resp[16:], serverTag)
			_ = writeBinFrame(conn, 1, 0, h.ReqID, resp)

		case 11: // PUT_BLOB
			if len(payload) < 36 {
				_ = writeBinError(conn, h.ReqID, 400, "put_blob: short payload")
				continue
			}
			var wantHash [32]byte
			copy(wantHash[:], payload[0:32])
			rawLen := binary.LittleEndian.Uint32(payload[32:36])
			if int(36+rawLen) != len(payload) {
				_ = writeBinError(conn, h.ReqID, 400, fmt.Sprintf("put_blob: len mismatch: raw_len=%d payload=%d", rawLen, len(payload)))
				continue
			}
			raw := payload[36:]
			gotHash := blake3.Sum256(raw)
			if gotHash != wantHash {
				_ = writeBinError(conn, h.ReqID, 409, "put_blob: hash mismatch")
				continue
			}

			s.mu.Lock()
			_, existed := s.blobs[wantHash]
			if !existed {
				s.blobs[wantHash] = append([]byte{}, raw...)
			}
			s.mu.Unlock()

			resp := make([]byte, 33)
			copy(resp[0:32], wantHash[:])
			if existed {
				resp[32] = 0
			} else {
				resp[32] = 1
			}
			_ = writeBinFrame(conn, 11, 0, h.ReqID, resp)

		default:
			_ = writeBinError(conn, h.ReqID, 400, fmt.Sprintf("unsupported msg_type=%d", h.MsgType))
		}
	}
}

func anyToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func anyToInt(v any) int {
	switch x := v.(type) {
	case nil:
		return 0
	case int:
		return x
	case float64:
		return int(x)
	default:
		return 0
	}
}
