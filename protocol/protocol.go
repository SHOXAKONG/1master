// Package protocol defines the wire format for the 1master tunnel.
//
// The design follows jprq (github.com/azimjohn/jprq): a single long-lived
// *control* connection carries only tiny JSON events, while every public
// request is served by its own dedicated raw TCP *data* connection that is
// byte-piped end to end. Because each request has its own connection, there is
// no multiplexing over a shared socket — which means no frame interleaving, no
// head-of-line blocking, and native support for streaming, chunked transfer,
// large bodies and WebSocket upgrades.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

// Control-channel message types.
const (
	TypeRegister   uint8 = 1 // client -> server: authenticate and open a tunnel
	TypeRegistered uint8 = 2 // server -> client: tunnel is open (or Error set)
	TypeNewConn    uint8 = 3 // server -> client: a public connection is waiting
)

// ConnIDSize is the number of raw bytes a client writes on a data connection to
// identify which pending public connection it is pairing with.
const ConnIDSize = 8

// Message is a control-channel event. It is never used on data connections.
type Message struct {
	Type      uint8  `json:"type"`
	Token     string `json:"token,omitempty"`     // register: service token
	Subdomain string `json:"subdomain,omitempty"` // register: requested / registered subdomain
	Hostname  string `json:"hostname,omitempty"`  // registered: full public hostname
	DataPort  string `json:"data_port,omitempty"` // registered: port the client dials per request
	ConnID    string `json:"conn_id,omitempty"`   // new-conn: hex id to echo on the data connection
	Error     string `json:"error,omitempty"`     // registered: non-empty means failure
}

// maxMessageSize bounds a single control frame (control events are tiny).
const maxMessageSize = 1 << 20 // 1 MiB

// Send writes a length-prefixed JSON control message as a single frame.
//
// NOTE: not safe for concurrent use on one connection; wrap shared connections
// in SafeConn and use SafeConn.Send.
func Send(conn net.Conn, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(data)))
	copy(buf[4:], data)
	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// Recv reads a single length-prefixed JSON control message.
func Recv(conn net.Conn) (*Message, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	length := binary.BigEndian.Uint32(header)
	if length > maxMessageSize {
		return nil, fmt.Errorf("control message too large: %d bytes", length)
	}
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

// SafeConn wraps a control connection so that events pushed from multiple
// goroutines (e.g. one per incoming public connection) cannot interleave their
// frames on the wire. Recv is expected to be driven by a single reader.
type SafeConn struct {
	net.Conn
	writeMu sync.Mutex
}

// NewSafeConn wraps c with a write mutex.
func NewSafeConn(c net.Conn) *SafeConn {
	return &SafeConn{Conn: c}
}

// Send serializes the whole message against other Send calls on this connection.
func (c *SafeConn) Send(msg *Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return Send(c.Conn, msg)
}

// Bind pipes bytes bidirectionally between a and b until either side closes,
// then tears both down. This is the raw data path: no HTTP parsing, no
// buffering limits, so streaming responses and WebSockets pass through
// untouched.
func Bind(a, b net.Conn) {
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		io.Copy(dst, src)
		// Closing both ends unblocks the opposite copy.
		a.Close()
		b.Close()
		done <- struct{}{}
	}
	go pipe(a, b)
	go pipe(b, a)
	<-done
	<-done
}
