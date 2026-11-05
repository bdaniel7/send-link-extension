package main

// A minimal RFC 6455 WebSocket server — no external dependencies. It only
// implements what this relay needs: unfragmented text frames, ping/pong,
// and close. It does not handle continuation frames, since browsers send
// the small one-shot JSON payloads used here as single unfragmented frames.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type opcode byte

const (
	opContinuation opcode = 0x0
	opText         opcode = 0x1
	opClose        opcode = 0x8
	opPing         opcode = 0x9
	opPong         opcode = 0xA
)

// wsConn wraps one upgraded connection. Writes go through mu so a
// heartbeat ping and a broadcast message can never interleave their bytes
// on the wire; alive is toggled by the read loop when a Pong arrives and
// checked by the heartbeat loop on the next tick.
type wsConn struct {
	conn   net.Conn
	br     *bufio.Reader
	mu     sync.Mutex
	closed bool
	alive  atomic.Bool
}

func upgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("not a websocket upgrade request")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("hijacking not supported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}

	// rw.Reader may already hold bytes the net/http server buffered past
	// the handshake headers, so reads must continue from it, not a fresh
	// reader on conn directly.
	return &wsConn{conn: conn, br: rw.Reader}, nil
}

func (c *wsConn) readFrame() (opcode, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.br, header); err != nil {
		return 0, nil, err
	}
	op := opcode(header[0] & 0x0f)
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)

	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(c.br, ext); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(c.br, ext); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext)
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return op, payload, nil
}

// writeFrame sends an unmasked frame, as required for server-to-client
// traffic by the spec.
func (c *wsConn) writeFrame(op opcode, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("connection closed")
	}

	n := len(payload)
	b0 := byte(0x80) | byte(op) // FIN=1, no fragmentation needed for our payload sizes

	var header []byte
	switch {
	case n <= 125:
		header = []byte{b0, byte(n)}
	case n <= 0xFFFF:
		header = make([]byte, 4)
		header[0], header[1] = b0, 126
		binary.BigEndian.PutUint16(header[2:], uint16(n))
	default:
		header = make([]byte, 10)
		header[0], header[1] = b0, 127
		binary.BigEndian.PutUint64(header[2:], uint64(n))
	}

	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if n > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *wsConn) writeText(s string) error { return c.writeFrame(opText, []byte(s)) }
func (c *wsConn) writePing() error         { return c.writeFrame(opPing, nil) }

func (c *wsConn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	// Best-effort close frame; ignore errors since we're tearing down anyway.
	c.conn.Write([]byte{0x80 | byte(opClose), 0})
	c.conn.Close()
}
