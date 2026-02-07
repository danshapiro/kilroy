package cxdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zeebo/blake3"
)

const (
	binaryProtocolVersion uint32 = 1

	msgTypeHello     uint16 = 1
	msgTypeCtxCreate uint16 = 2
	msgTypeCtxFork   uint16 = 3
	msgTypeGetHead   uint16 = 4
	msgTypeAppend    uint16 = 5
	msgTypePutBlob   uint16 = 11
	msgTypeError     uint16 = 255

	maxFrameSize uint32 = 64 * 1024 * 1024
)

type BinaryClient struct {
	Addr      string
	ClientTag string

	mu   sync.Mutex
	conn net.Conn

	nextReqID atomic.Uint64

	SessionID       uint64
	ProtocolVersion uint32
	ServerTag       string
}

type BinaryError struct {
	Code   uint32
	Detail string
}

func (e *BinaryError) Error() string {
	if e == nil {
		return "cxdb binary error"
	}
	if e.Detail != "" {
		return fmt.Sprintf("cxdb binary error: code=%d detail=%s", e.Code, e.Detail)
	}
	return fmt.Sprintf("cxdb binary error: code=%d", e.Code)
}

type BinaryContextInfo struct {
	ContextID  uint64
	HeadTurnID uint64
	HeadDepth  uint32
}

type AppendAck struct {
	ContextID   uint64
	NewTurnID   uint64
	NewDepth    uint32
	ContentHash [32]byte
}

func DialBinary(ctx context.Context, addr string, clientTag string) (*BinaryClient, error) {
	addr = stringsTrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("cxdb binary addr is required")
	}
	clientTag = stringsTrimSpace(clientTag)
	d := net.Dialer{Timeout: 10 * time.Second}
	dial := func() (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }

	// Current protocol (per CXDB docs): HELLO uses u32 lengths and returns
	// protocol_version(u32), session_id(u64), and server_tag.
	conn, err := dial()
	if err != nil {
		return nil, err
	}
	c := &BinaryClient{
		Addr:            addr,
		ClientTag:       clientTag,
		conn:            conn,
		ProtocolVersion: binaryProtocolVersion,
	}
	c.nextReqID.Store(0)
	if errHello := c.helloV1(ctx); errHello == nil {
		return c, nil
	} else {
		_ = conn.Close()

		// Backward-compat: attempt legacy HELLO framing (older servers).
		conn2, err2 := dial()
		if err2 != nil {
			return nil, errHello
		}
		c2 := &BinaryClient{
			Addr:            addr,
			ClientTag:       clientTag,
			conn:            conn2,
			ProtocolVersion: binaryProtocolVersion,
		}
		c2.nextReqID.Store(0)
		if errLegacy := c2.helloLegacy(ctx); errLegacy == nil {
			return c2, nil
		} else {
			_ = conn2.Close()
			return nil, fmt.Errorf("cxdb hello failed: v1=%v legacy=%v", errHello, errLegacy)
		}
	}
}

func (c *BinaryClient) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *BinaryClient) CreateContext(ctx context.Context, baseTurnID uint64) (BinaryContextInfo, error) {
	var payload [8]byte
	binary.LittleEndian.PutUint64(payload[:], baseTurnID)
	reqID := c.nextReqID.Add(1)
	var out BinaryContextInfo
	if err := c.roundTrip(ctx, msgTypeCtxCreate, 0, reqID, payload[:], func(respType uint16, respPayload []byte) error {
		if respType != msgTypeCtxCreate {
			return fmt.Errorf("cxdb ctx_create: unexpected response type=%d", respType)
		}
		ci, err := parseBinaryContextInfo(respPayload)
		if err != nil {
			return err
		}
		out = ci
		return nil
	}); err != nil {
		return BinaryContextInfo{}, err
	}
	return out, nil
}

func (c *BinaryClient) ForkContext(ctx context.Context, baseTurnID uint64) (BinaryContextInfo, error) {
	var payload [8]byte
	binary.LittleEndian.PutUint64(payload[:], baseTurnID)
	reqID := c.nextReqID.Add(1)
	var out BinaryContextInfo
	if err := c.roundTrip(ctx, msgTypeCtxFork, 0, reqID, payload[:], func(respType uint16, respPayload []byte) error {
		if respType != msgTypeCtxFork {
			return fmt.Errorf("cxdb ctx_fork: unexpected response type=%d", respType)
		}
		ci, err := parseBinaryContextInfo(respPayload)
		if err != nil {
			return err
		}
		out = ci
		return nil
	}); err != nil {
		return BinaryContextInfo{}, err
	}
	return out, nil
}

func (c *BinaryClient) GetHead(ctx context.Context, contextID uint64) (BinaryContextInfo, error) {
	var payload [8]byte
	binary.LittleEndian.PutUint64(payload[:], contextID)
	reqID := c.nextReqID.Add(1)
	var out BinaryContextInfo
	if err := c.roundTrip(ctx, msgTypeGetHead, 0, reqID, payload[:], func(respType uint16, respPayload []byte) error {
		if respType != msgTypeGetHead {
			return fmt.Errorf("cxdb get_head: unexpected response type=%d", respType)
		}
		ci, err := parseBinaryContextInfo(respPayload)
		if err != nil {
			return err
		}
		out = ci
		return nil
	}); err != nil {
		return BinaryContextInfo{}, err
	}
	return out, nil
}

func (c *BinaryClient) AppendTurn(ctx context.Context, contextID uint64, parentTurnID uint64, declaredTypeID string, declaredTypeVersion uint32, msgpackPayload []byte) (AppendAck, error) {
	if c == nil {
		return AppendAck{}, fmt.Errorf("cxdb binary client is nil")
	}
	declaredTypeID = stringsTrimSpace(declaredTypeID)
	if declaredTypeID == "" || declaredTypeVersion == 0 {
		return AppendAck{}, fmt.Errorf("declared type id+version are required")
	}
	if msgpackPayload == nil {
		msgpackPayload = []byte{}
	}

	rawLen := uint32(len(msgpackPayload))
	sum := blake3.Sum256(msgpackPayload)
	// Idempotency keys are unique per context and let the server safely dedupe retries.
	// Including parent_turn_id avoids collapsing identical payloads at different DAG positions.
	idemKey := fmt.Sprintf("kilroy:%d:%s:%d:%x", parentTurnID, declaredTypeID, declaredTypeVersion, sum[:])

	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, contextID)
	_ = binary.Write(&buf, binary.LittleEndian, parentTurnID)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(declaredTypeID)))
	_, _ = buf.WriteString(declaredTypeID)
	_ = binary.Write(&buf, binary.LittleEndian, declaredTypeVersion)

	// encoding=1 (msgpack), compression=0 (none)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	_ = binary.Write(&buf, binary.LittleEndian, rawLen)
	_, _ = buf.Write(sum[:])

	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(msgpackPayload)))
	if len(msgpackPayload) > 0 {
		_, _ = buf.Write(msgpackPayload)
	}

	// idempotency key (recommended by protocol)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(idemKey)))
	if idemKey != "" {
		_, _ = buf.WriteString(idemKey)
	}

	reqID := c.nextReqID.Add(1)
	var out AppendAck
	if err := c.roundTrip(ctx, msgTypeAppend, 0, reqID, buf.Bytes(), func(respType uint16, respPayload []byte) error {
		if respType != msgTypeAppend {
			return fmt.Errorf("cxdb append_turn: unexpected response type=%d", respType)
		}
		ack, err := parseAppendAck(respPayload)
		if err != nil {
			return err
		}
		out = ack
		return nil
	}); err != nil {
		return AppendAck{}, err
	}
	return out, nil
}

func (c *BinaryClient) PutBlob(ctx context.Context, contentHash [32]byte, rawLen uint32, r io.Reader) (wasNew bool, err error) {
	if c == nil {
		return false, fmt.Errorf("cxdb binary client is nil")
	}
	if r == nil {
		r = bytes.NewReader(nil)
	}

	payloadLen := uint64(32 + 4)
	payloadLen += uint64(rawLen)
	if payloadLen > uint64(^uint32(0)) {
		return false, fmt.Errorf("cxdb put_blob: payload too large: %d", payloadLen)
	}

	reqID := c.nextReqID.Add(1)
	if err := c.roundTripWrite(ctx, msgTypePutBlob, 0, reqID, uint32(payloadLen), func(w io.Writer) error {
		if err := writeAll(w, contentHash[:]); err != nil {
			return err
		}
		var lenBuf [4]byte
		binary.LittleEndian.PutUint32(lenBuf[:], rawLen)
		if err := writeAll(w, lenBuf[:]); err != nil {
			return err
		}
		if rawLen == 0 {
			return nil
		}
		_, err := io.CopyN(w, r, int64(rawLen))
		return err
	}, func(respType uint16, respPayload []byte) error {
		if respType != msgTypePutBlob {
			return fmt.Errorf("cxdb put_blob: unexpected response type=%d", respType)
		}
		if len(respPayload) < 33 {
			return fmt.Errorf("cxdb put_blob ack: short payload len=%d", len(respPayload))
		}
		if !bytes.Equal(respPayload[:32], contentHash[:]) {
			return fmt.Errorf("cxdb put_blob ack: hash mismatch")
		}
		wasNew = respPayload[32] == 1
		return nil
	}); err != nil {
		return false, err
	}
	return wasNew, nil
}

func parseBinaryContextInfo(payload []byte) (BinaryContextInfo, error) {
	if len(payload) < 20 {
		return BinaryContextInfo{}, fmt.Errorf("cxdb ctx resp: short payload len=%d", len(payload))
	}
	return BinaryContextInfo{
		ContextID:  binary.LittleEndian.Uint64(payload[:8]),
		HeadTurnID: binary.LittleEndian.Uint64(payload[8:16]),
		HeadDepth:  binary.LittleEndian.Uint32(payload[16:20]),
	}, nil
}

func parseAppendAck(payload []byte) (AppendAck, error) {
	if len(payload) < 52 {
		return AppendAck{}, fmt.Errorf("cxdb append ack: short payload len=%d", len(payload))
	}
	var hash [32]byte
	copy(hash[:], payload[20:52])
	return AppendAck{
		ContextID:   binary.LittleEndian.Uint64(payload[:8]),
		NewTurnID:   binary.LittleEndian.Uint64(payload[8:16]),
		NewDepth:    binary.LittleEndian.Uint32(payload[16:20]),
		ContentHash: hash,
	}, nil
}

type frameHeader struct {
	Len     uint32
	MsgType uint16
	Flags   uint16
	ReqID   uint64
}

func readFrame(r io.Reader) (frameHeader, []byte, error) {
	var hdrBuf [16]byte
	if _, err := io.ReadFull(r, hdrBuf[:]); err != nil {
		return frameHeader{}, nil, err
	}
	h := frameHeader{
		Len:     binary.LittleEndian.Uint32(hdrBuf[0:4]),
		MsgType: binary.LittleEndian.Uint16(hdrBuf[4:6]),
		Flags:   binary.LittleEndian.Uint16(hdrBuf[6:8]),
		ReqID:   binary.LittleEndian.Uint64(hdrBuf[8:16]),
	}
	if h.Len > maxFrameSize {
		return frameHeader{}, nil, fmt.Errorf("cxdb frame too large: %d > %d", h.Len, maxFrameSize)
	}
	payload := make([]byte, int(h.Len))
	if _, err := io.ReadFull(r, payload); err != nil {
		return frameHeader{}, nil, err
	}
	return h, payload, nil
}

func writeFrameHeader(w io.Writer, msgType uint16, flags uint16, reqID uint64, payloadLen uint32) error {
	var hdrBuf [16]byte
	binary.LittleEndian.PutUint32(hdrBuf[0:4], payloadLen)
	binary.LittleEndian.PutUint16(hdrBuf[4:6], msgType)
	binary.LittleEndian.PutUint16(hdrBuf[6:8], flags)
	binary.LittleEndian.PutUint64(hdrBuf[8:16], reqID)
	return writeAll(w, hdrBuf[:])
}

func writeFrame(w io.Writer, msgType uint16, flags uint16, reqID uint64, payload []byte) error {
	if err := writeFrameHeader(w, msgType, flags, reqID, uint32(len(payload))); err != nil {
		return err
	}
	if len(payload) > 0 {
		return writeAll(w, payload)
	}
	return nil
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func (c *BinaryClient) roundTrip(ctx context.Context, msgType uint16, flags uint16, reqID uint64, payload []byte, handle func(respType uint16, respPayload []byte) error) error {
	return c.roundTripWrite(ctx, msgType, flags, reqID, uint32(len(payload)), func(w io.Writer) error {
		if len(payload) == 0 {
			return nil
		}
		return writeAll(w, payload)
	}, handle)
}

func (c *BinaryClient) roundTripWrite(ctx context.Context, msgType uint16, flags uint16, reqID uint64, payloadLen uint32, writePayload func(w io.Writer) error, handle func(respType uint16, respPayload []byte) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("cxdb binary connection is closed")
	}

	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = c.conn.SetDeadline(deadline)
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

	if err := writeFrameHeader(c.conn, msgType, flags, reqID, payloadLen); err != nil {
		return err
	}
	if payloadLen > 0 {
		if err := writePayload(c.conn); err != nil {
			return err
		}
	}

	h, respPayload, err := readFrame(c.conn)
	if err != nil {
		return err
	}
	if h.ReqID != reqID {
		return fmt.Errorf("cxdb: response req_id mismatch: got=%d want=%d", h.ReqID, reqID)
	}
	if h.MsgType == msgTypeError {
		return parseBinaryError(respPayload)
	}
	return handle(h.MsgType, respPayload)
}

func parseBinaryError(payload []byte) error {
	if len(payload) < 8 {
		return &BinaryError{Code: 0, Detail: fmt.Sprintf("short error payload len=%d", len(payload))}
	}
	code := binary.LittleEndian.Uint32(payload[:4])
	detailLen := binary.LittleEndian.Uint32(payload[4:8])
	detail := ""
	if int(8+detailLen) <= len(payload) && detailLen > 0 {
		detail = string(payload[8 : 8+detailLen])
	} else if len(payload) > 8 {
		detail = string(payload[8:])
	}
	return &BinaryError{Code: code, Detail: detail}
}

func (c *BinaryClient) helloV1(ctx context.Context) error {
	// CXDB v1 HELLO request:
	// protocol_version(u32) + client_tag_len(u32) + client_tag
	var payload bytes.Buffer
	_ = binary.Write(&payload, binary.LittleEndian, binaryProtocolVersion)
	_ = binary.Write(&payload, binary.LittleEndian, uint32(len(c.ClientTag)))
	if c.ClientTag != "" {
		_, _ = payload.WriteString(c.ClientTag)
	}
	reqID := c.nextReqID.Add(1)
	return c.roundTrip(ctx, msgTypeHello, 0, reqID, payload.Bytes(), func(respType uint16, respPayload []byte) error {
		if respType != msgTypeHello {
			return fmt.Errorf("cxdb hello: unexpected response type=%d", respType)
		}
		// protocol_version(u32) + session_id(u64) + server_tag_len(u32) + server_tag
		if len(respPayload) < 16 {
			return fmt.Errorf("cxdb hello: short response payload len=%d", len(respPayload))
		}
		c.ProtocolVersion = binary.LittleEndian.Uint32(respPayload[0:4])
		c.SessionID = binary.LittleEndian.Uint64(respPayload[4:12])
		serverTagLen := binary.LittleEndian.Uint32(respPayload[12:16])
		if int(16+serverTagLen) <= len(respPayload) && serverTagLen > 0 {
			c.ServerTag = string(respPayload[16 : 16+serverTagLen])
		}
		return nil
	})
}

func (c *BinaryClient) helloLegacy(ctx context.Context) error {
	// Legacy HELLO request:
	// protocol_version(u16) + client_tag_len(u16) + client_tag + meta_json_len(u32) + meta_json
	var payload bytes.Buffer
	_ = binary.Write(&payload, binary.LittleEndian, uint16(binaryProtocolVersion))
	_ = binary.Write(&payload, binary.LittleEndian, uint16(len(c.ClientTag)))
	if c.ClientTag != "" {
		_, _ = payload.WriteString(c.ClientTag)
	}
	_ = binary.Write(&payload, binary.LittleEndian, uint32(0)) // meta_json_len

	reqID := c.nextReqID.Add(1)
	return c.roundTrip(ctx, msgTypeHello, 0, reqID, payload.Bytes(), func(respType uint16, respPayload []byte) error {
		if respType != msgTypeHello {
			return fmt.Errorf("cxdb hello (legacy): unexpected response type=%d", respType)
		}
		// session_id(u64) + protocol_version(u16)
		if len(respPayload) < 10 {
			return fmt.Errorf("cxdb hello (legacy): short response payload len=%d", len(respPayload))
		}
		c.SessionID = binary.LittleEndian.Uint64(respPayload[:8])
		c.ProtocolVersion = uint32(binary.LittleEndian.Uint16(respPayload[8:10]))
		return nil
	})
}

// stringsTrimSpace avoids importing strings in this file just for TrimSpace.
func stringsTrimSpace(s string) string {
	// Minimal TrimSpace (ASCII only) to keep this file's imports tight.
	i := 0
	j := len(s)
	for i < j {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			goto right
		}
	}
right:
	for j > i {
		switch s[j-1] {
		case ' ', '\t', '\n', '\r':
			j--
		default:
			return s[i:j]
		}
	}
	return ""
}
