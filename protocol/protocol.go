// Package protocol is the wire protocol between the Goban connector and the
// Goban web app. Both sides send JSON text messages over one WebSocket.
// Every message is a Message with a Type and the fields that type needs.
package protocol

import (
	"errors"
	"fmt"
)

// Version is the protocol version the connector sends in its hello message.
const Version = 1

// MaxMessage is the biggest message, in bytes, that either side may send:
// 16 MiB. An SGF may be 5242880 bytes, and escaping it in JSON can nearly
// double that.
const MaxMessage = 16 << 20

// The message types.
const (
	Hello   = "hello"   // connector to server: the first message
	Welcome = "welcome" // server to connector: the answer to hello
	Start   = "start"   // server to connector: start a process
	Started = "started" // connector to server: the process is running
	Stdin   = "stdin"   // server to connector: text for the process
	Stdout  = "stdout"  // connector to server: text from the process
	Close   = "close"   // server to connector: close the process's stdin
	Ended   = "ended"   // connector to server: the process stopped
	Run     = "run"     // server to connector: run a short command
	Result  = "result"  // connector to server: the answer to run
)

// The WebSocket close codes.
const (
	CloseBadMessage = 4400 // bad protocol version or bad message
	CloseBadToken   = 4401 // the connector token is not valid
	CloseReplaced   = 4409 // a newer connection took this connector's place
	CloseRemoved    = 4410 // the connector was removed
)

// Message is every message on the wire. Which fields are used depends on
// Type; the others stay empty and are left out of the JSON.
type Message struct {
	Type string `json:"type"`

	// hello
	Protocol int    `json:"protocol,omitempty"`
	Version  string `json:"version,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Katago   string `json:"katago,omitempty"`
	MaxGames int    `json:"maxGames,omitempty"`

	// welcome
	ConnectorID int64  `json:"connectorId,omitempty"`
	Name        string `json:"name,omitempty"`

	// start, started, stdin, stdout, close, ended
	Stream uint32   `json:"stream,omitempty"`
	Args   []string `json:"args,omitempty"` // start and run
	Data   string   `json:"data,omitempty"` // stdin and stdout

	// run and result
	ID     uint32 `json:"id,omitempty"`
	Stdin  string `json:"stdin,omitempty"`  // run
	Stdout string `json:"stdout,omitempty"` // result
	Error  string `json:"error,omitempty"`  // ended and result
	Stderr string `json:"stderr,omitempty"` // ended and result
}

// Check says whether the message has the fields its type needs. It does not
// check the values of Args; CheckProcessArgs and CheckShortArgs do that.
func (m Message) Check() error {
	switch m.Type {
	case Hello:
		if m.Protocol <= 0 {
			return errors.New("hello needs protocol")
		}
	case Welcome:
	case Start:
		if m.Stream == 0 || len(m.Args) == 0 {
			return errors.New("start needs stream and args")
		}
	case Started, Stdin, Stdout, Close, Ended:
		if m.Stream == 0 {
			return fmt.Errorf("%s needs stream", m.Type)
		}
	case Run, Result:
		if m.ID == 0 {
			return fmt.Errorf("%s needs id", m.Type)
		}
	default:
		return fmt.Errorf("unknown message type %q", m.Type)
	}
	return nil
}
