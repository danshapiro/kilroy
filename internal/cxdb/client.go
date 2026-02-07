package cxdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

type ContextInfo struct {
	ContextID  string `json:"context_id"`
	HeadTurnID string `json:"head_turn_id"`
	HeadDepth  int    `json:"head_depth"`
}

type AppendTurnRequest struct {
	TypeID         string         `json:"type_id"`
	TypeVersion    int            `json:"type_version"`
	Data           map[string]any `json:"data"`
	ParentTurnID   string         `json:"parent_turn_id,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

type AppendTurnResponse struct {
	ContextID   string `json:"context_id"`
	TurnID      string `json:"turn_id"`
	Depth       int    `json:"depth"`
	PayloadHash string `json:"payload_hash,omitempty"`
	ContentHash string `json:"content_hash,omitempty"` // backward-compat
}

type ErrorEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

type HTTPError struct {
	Path   string
	Status int
	Code   string
	Body   string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "cxdb http error"
	}
	msg := strings.TrimSpace(e.Body)
	if e.Code != "" && msg != "" {
		return fmt.Sprintf("cxdb %s: status=%d code=%s message=%s", e.Path, e.Status, e.Code, msg)
	}
	if e.Code != "" {
		return fmt.Sprintf("cxdb %s: status=%d code=%s", e.Path, e.Status, e.Code)
	}
	if msg != "" {
		return fmt.Sprintf("cxdb %s: status=%d body=%s", e.Path, e.Status, msg)
	}
	return fmt.Sprintf("cxdb %s: status=%d", e.Path, e.Status)
}

func (c *Client) Health(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("cxdb client is nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("cxdb health failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) CreateContext(ctx context.Context, baseTurnID string) (ContextInfo, error) {
	// Primary: POST /v1/contexts/create (CXDB HTTP API)
	ci, err := c.postContext(ctx, "/v1/contexts/create", baseTurnID)
	if err == nil {
		return ci, nil
	}
	// Backward-compat: alternate path (if present).
	if shouldTryCompat(err) {
		if ci2, err2 := c.postContext(ctx, "/v1/contexts", baseTurnID); err2 == nil {
			return ci2, nil
		}
	}
	return ContextInfo{}, err
}

func (c *Client) ForkContext(ctx context.Context, baseTurnID string) (ContextInfo, error) {
	// Primary: POST /v1/contexts/fork (CXDB HTTP API)
	ci, err := c.postContext(ctx, "/v1/contexts/fork", baseTurnID)
	if err == nil {
		return ci, nil
	}
	// Backward-compat: alternate path (if present).
	if shouldTryCompat(err) {
		if ci2, err2 := c.postContext(ctx, "/v1/contexts", baseTurnID); err2 == nil {
			return ci2, nil
		}
	}
	return ContextInfo{}, err
}

func (c *Client) postContext(ctx context.Context, path string, baseTurnID string) (ContextInfo, error) {
	if strings.TrimSpace(baseTurnID) == "" {
		baseTurnID = "0"
	}
	body := map[string]string{"base_turn_id": baseTurnID}
	b, _ := json.Marshal(body)
	bodyReader := bytes.NewReader(b)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bodyReader)
	if err != nil {
		return ContextInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return ContextInfo{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ContextInfo{}, httpErr(path, resp.StatusCode, raw)
	}
	ci, err := parseContextInfo(raw)
	if err != nil {
		return ContextInfo{}, err
	}
	if strings.TrimSpace(ci.ContextID) == "" {
		return ContextInfo{}, fmt.Errorf("cxdb create context: missing context_id")
	}
	return ci, nil
}

func (c *Client) AppendTurn(ctx context.Context, contextID string, reqBody AppendTurnRequest) (AppendTurnResponse, error) {
	if strings.TrimSpace(contextID) == "" {
		return AppendTurnResponse{}, fmt.Errorf("context_id is required")
	}
	if strings.TrimSpace(reqBody.TypeID) == "" || reqBody.TypeVersion <= 0 {
		return AppendTurnResponse{}, fmt.Errorf("type_id and type_version are required")
	}
	if reqBody.Data == nil {
		reqBody.Data = map[string]any{}
	}
	bAppend, err := json.Marshal(reqBody)
	if err != nil {
		return AppendTurnResponse{}, err
	}
	path := fmt.Sprintf("/v1/contexts/%s/append", url.PathEscape(contextID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(bAppend))
	if err != nil {
		return AppendTurnResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(httpReq)
	if err != nil {
		return AppendTurnResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		// Backward-compat: older servers used /turns with JSON "payload" rather than "data".
		compatReq := map[string]any{
			"type_id":      reqBody.TypeID,
			"type_version": reqBody.TypeVersion,
			"payload":      reqBody.Data,
		}
		if strings.TrimSpace(reqBody.ParentTurnID) != "" {
			compatReq["parent_turn_id"] = reqBody.ParentTurnID
		}
		if strings.TrimSpace(reqBody.IdempotencyKey) != "" {
			compatReq["idempotency_key"] = reqBody.IdempotencyKey
		}
		bCompat, _ := json.Marshal(compatReq)

		compatPath := fmt.Sprintf("/v1/contexts/%s/turns", url.PathEscape(contextID))
		httpReq2, err2 := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+compatPath, bytes.NewReader(bCompat))
		if err2 != nil {
			return AppendTurnResponse{}, err2
		}
		httpReq2.Header.Set("Content-Type", "application/json")
		resp2, err2 := c.http().Do(httpReq2)
		if err2 != nil {
			return AppendTurnResponse{}, err2
		}
		defer func() { _ = resp2.Body.Close() }()
		raw2, _ := io.ReadAll(resp2.Body)
		if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
			return AppendTurnResponse{}, httpErr(compatPath, resp2.StatusCode, raw2)
		}
		out, err := parseAppendTurnResponse(raw2)
		if err != nil {
			return AppendTurnResponse{}, err
		}
		if strings.TrimSpace(out.TurnID) == "" {
			return AppendTurnResponse{}, fmt.Errorf("cxdb append: missing turn_id")
		}
		return out, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AppendTurnResponse{}, httpErr(path, resp.StatusCode, raw)
	}
	out, err := parseAppendTurnResponse(raw)
	if err != nil {
		return AppendTurnResponse{}, err
	}
	if strings.TrimSpace(out.TurnID) == "" {
		return AppendTurnResponse{}, fmt.Errorf("cxdb append: missing turn_id")
	}
	return out, nil
}

func (c *Client) PublishRegistryBundle(ctx context.Context, bundleID string, bundle any) (int, error) {
	if strings.TrimSpace(bundleID) == "" {
		return 0, fmt.Errorf("bundle_id is required")
	}
	b, err := json.Marshal(bundle)
	if err != nil {
		return 0, err
	}
	path := fmt.Sprintf("/v1/registry/bundles/%s", bundleID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, httpErr(path, resp.StatusCode, raw)
}

func (c *Client) GetContext(ctx context.Context, contextID string) (ContextInfo, error) {
	if strings.TrimSpace(contextID) == "" {
		return ContextInfo{}, fmt.Errorf("context_id is required")
	}
	path := fmt.Sprintf("/v1/contexts/%s", url.PathEscape(contextID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return ContextInfo{}, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return ContextInfo{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		// Older servers may not support direct GET; fall back to listing.
		list, err := c.ListContexts(ctx)
		if err != nil {
			return ContextInfo{}, err
		}
		for _, ci := range list {
			if ci.ContextID == contextID {
				return ci, nil
			}
		}
		return ContextInfo{}, httpErr(path, resp.StatusCode, raw)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ContextInfo{}, httpErr(path, resp.StatusCode, raw)
	}
	ci, err := parseContextInfo(raw)
	if err != nil {
		return ContextInfo{}, err
	}
	if strings.TrimSpace(ci.ContextID) == "" {
		ci.ContextID = contextID
	}
	return ci, nil
}

func (c *Client) ListContexts(ctx context.Context) ([]ContextInfo, error) {
	path := "/v1/contexts"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpErr(path, resp.StatusCode, raw)
	}
	maps := []map[string]any{}
	if err := json.Unmarshal(raw, &maps); err == nil {
		out := make([]ContextInfo, 0, len(maps))
		for _, m := range maps {
			out = append(out, parseContextInfoMap(m))
		}
		return out, nil
	}
	// Some servers may wrap: {"contexts":[...]}.
	var wrapped struct {
		Contexts []map[string]any `json:"contexts"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	out := make([]ContextInfo, 0, len(wrapped.Contexts))
	for _, m := range wrapped.Contexts {
		out = append(out, parseContextInfoMap(m))
	}
	return out, nil
}

type Turn struct {
	TurnID       string         `json:"turn_id"`
	ParentTurnID string         `json:"parent_turn_id,omitempty"`
	Depth        int            `json:"depth,omitempty"`
	TypeID       string         `json:"type_id,omitempty"`
	TypeVersion  int            `json:"type_version,omitempty"`
	Payload      map[string]any `json:"payload,omitempty"`
	PayloadHash  string         `json:"payload_hash,omitempty"`
}

type ListTurnsOptions struct {
	Limit        int
	BeforeTurnID string
	View         string
}

func (c *Client) ListTurns(ctx context.Context, contextID string, opts ListTurnsOptions) ([]Turn, error) {
	if strings.TrimSpace(contextID) == "" {
		return nil, fmt.Errorf("context_id is required")
	}
	q := url.Values{}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if strings.TrimSpace(opts.BeforeTurnID) != "" {
		q.Set("before_turn_id", strings.TrimSpace(opts.BeforeTurnID))
	}
	if strings.TrimSpace(opts.View) != "" {
		q.Set("view", strings.TrimSpace(opts.View))
	}
	path := fmt.Sprintf("/v1/contexts/%s/turns", url.PathEscape(contextID))
	full := c.BaseURL + path
	if enc := q.Encode(); enc != "" {
		full += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpErr(path, resp.StatusCode, raw)
	}

	// Response is either a JSON array of turns or a wrapper object.
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		var wrapped struct {
			Turns []map[string]any `json:"turns"`
		}
		if err2 := json.Unmarshal(raw, &wrapped); err2 != nil {
			return nil, err
		}
		arr = wrapped.Turns
	}
	out := make([]Turn, 0, len(arr))
	for _, m := range arr {
		out = append(out, parseTurnMap(m))
	}
	return out, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func httpErr(path string, status int, raw []byte) error {
	var env ErrorEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && strings.TrimSpace(env.Error.Message) != "" {
		return &HTTPError{
			Path:   path,
			Status: status,
			Code:   env.Error.Code,
			Body:   env.Error.Message,
		}
	}
	return &HTTPError{
		Path:   path,
		Status: status,
		Body:   strings.TrimSpace(string(raw)),
	}
}

func shouldTryCompat(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusNotFound || he.Status == http.StatusMethodNotAllowed
	}
	return false
}

func pickHash(resp AppendTurnResponse) string {
	if strings.TrimSpace(resp.PayloadHash) != "" {
		return strings.TrimSpace(resp.PayloadHash)
	}
	return strings.TrimSpace(resp.ContentHash)
}

func parseTurnMap(m map[string]any) Turn {
	out := Turn{}
	if m == nil {
		return out
	}
	out.TurnID = anyToString(m["turn_id"])
	out.ParentTurnID = anyToString(m["parent_turn_id"])
	out.TypeID = anyToString(m["type_id"])
	out.TypeVersion = anyToInt(m["type_version"])
	if out.TypeID == "" || out.TypeVersion == 0 {
		// CXDB HTTP API view=typed uses nested declared_type/decoded_as objects.
		if dt, ok := m["declared_type"].(map[string]any); ok {
			if out.TypeID == "" {
				out.TypeID = anyToString(dt["type_id"])
			}
			if out.TypeVersion == 0 {
				out.TypeVersion = anyToInt(dt["type_version"])
			}
		}
		if da, ok := m["decoded_as"].(map[string]any); ok {
			if out.TypeID == "" {
				out.TypeID = anyToString(da["type_id"])
			}
			if out.TypeVersion == 0 {
				out.TypeVersion = anyToInt(da["type_version"])
			}
		}
	}
	out.Depth = anyToInt(m["depth"])
	out.PayloadHash = anyToString(m["payload_hash"])
	if v, ok := m["payload"].(map[string]any); ok {
		out.Payload = v
	} else if v, ok := m["data"].(map[string]any); ok {
		// CXDB HTTP API uses "data" for typed view.
		out.Payload = v
	}
	return out
}

func parseContextInfo(raw []byte) (ContextInfo, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return ContextInfo{}, err
	}
	return parseContextInfoMap(m), nil
}

func parseContextInfoMap(m map[string]any) ContextInfo {
	if m == nil {
		return ContextInfo{}
	}
	return ContextInfo{
		ContextID:  anyToString(m["context_id"]),
		HeadTurnID: anyToString(m["head_turn_id"]),
		HeadDepth:  anyToInt(m["head_depth"]),
	}
}

func parseAppendTurnResponse(raw []byte) (AppendTurnResponse, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return AppendTurnResponse{}, err
	}
	out := AppendTurnResponse{
		ContextID:   anyToString(m["context_id"]),
		TurnID:      anyToString(m["turn_id"]),
		Depth:       anyToInt(m["depth"]),
		PayloadHash: anyToString(m["payload_hash"]),
		ContentHash: anyToString(m["content_hash"]),
	}
	out.ContentHash = pickHash(out)
	return out, nil
}

func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		// JSON numbers decode to float64 by default; treat as integer-ish ID when possible.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return fmt.Sprintf("%v", t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	default:
		return fmt.Sprint(v)
	}
}

func anyToInt(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(t))
		return i
	default:
		return 0
	}
}
