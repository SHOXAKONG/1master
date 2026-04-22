package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
)

// Message types
const (
	TypeRegister     = 1 // Client -> Server: register a new tunnel
	TypeRegisterResp = 2 // Server -> Client: registration response
	TypeProxy        = 3 // Server -> Client: incoming HTTP request
	TypeProxyResp    = 4 // Client -> Server: HTTP response from local service
)

// Message is the wire format for all communication
type Message struct {
	Type      uint8  `json:"type"`
	ID        string `json:"id,omitempty"` // request ID for matching req/resp
	Subdomain string `json:"subdomain,omitempty"`
	Data      []byte `json:"data,omitempty"` // HTTP request or response bytes
	Error     string `json:"error,omitempty"`
}

// Send writes a length-prefixed JSON message to the connection
func Send(conn net.Conn, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	// Write 4-byte length header + payload
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(data)))

	if _, err := conn.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}

// Recv reads a length-prefixed JSON message from the connection
func Recv(conn net.Conn) (*Message, error) {
	// Read 4-byte length header
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	length := binary.BigEndian.Uint32(header)
	if length > 10*1024*1024 { // 10MB max
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	// Read payload
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &msg, nil
}
