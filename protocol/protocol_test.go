package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	msgs := []Message{
		{Type: Hello, Protocol: Version, Version: "0.1.0", OS: "linux", Arch: "amd64", Mode: "command", Katago: "KataGo v1.15.3", MaxGames: 2},
		{Type: Welcome, ConnectorID: 12, Name: "Home PC"},
		{Type: Start, Stream: 1, Args: []string{"gtp", "game-k7Qm2xPa9T", "--user", "7"}},
		{Type: Started, Stream: 1},
		{Type: Stdin, Stream: 1, Data: "name\n"},
		{Type: Stdout, Stream: 1, Data: "= KataGo\n\n"},
		{Type: Close, Stream: 1},
		{Type: Ended, Stream: 1, Error: "exit status 1", Stderr: "boom"},
		{Type: Run, ID: 5, Args: []string{"put-sgf", "7", "k7Qm2xPa9T"}, Stdin: "(;GM[1])"},
		{Type: Result, ID: 5, Stdout: "ok\n", Error: "failed", Stderr: "boom"},
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		seen[m.Type] = true
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("Marshal(%+v): %v", m, err)
		}
		var got Message
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", b, err)
		}
		if !reflect.DeepEqual(got, m) {
			t.Errorf("%s: got %+v, want %+v", b, got, m)
		}
		if err := got.Check(); err != nil {
			t.Errorf("%s: Check: %v", b, err)
		}
	}
	for _, typ := range []string{Hello, Welcome, Start, Started, Stdin, Stdout, Close, Ended, Run, Result} {
		if !seen[typ] {
			t.Errorf("type %q has no round trip case", typ)
		}
	}
}

func TestMessageJSON(t *testing.T) {
	cases := []struct {
		m    Message
		want string
	}{
		{Message{Type: Close, Stream: 3}, `{"type":"close","stream":3}`},
		{Message{Type: Started, Stream: 1}, `{"type":"started","stream":1}`},
		{Message{Type: Welcome, ConnectorID: 9, Name: "PC"}, `{"type":"welcome","connectorId":9,"name":"PC"}`},
		{Message{Type: Hello, Protocol: 1, MaxGames: 2}, `{"type":"hello","protocol":1,"maxGames":2}`},
		{Message{Type: Result, ID: 4}, `{"type":"result","id":4}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.m)
		if err != nil || string(b) != c.want {
			t.Errorf("Marshal(%+v) = %s, %v; want %s", c.m, b, err, c.want)
		}
	}
}

func TestConstants(t *testing.T) {
	if Version != 1 || MaxMessage != 16*1024*1024 {
		t.Errorf("Version = %d, MaxMessage = %d", Version, MaxMessage)
	}
	types := map[string]string{
		Hello: "hello", Welcome: "welcome", Start: "start", Started: "started", Stdin: "stdin",
		Stdout: "stdout", Close: "close", Ended: "ended", Run: "run", Result: "result",
	}
	for got, want := range types {
		if got != want {
			t.Errorf("type constant = %q, want %q", got, want)
		}
	}
	if CloseBadMessage != 4400 || CloseBadToken != 4401 || CloseReplaced != 4409 || CloseRemoved != 4410 {
		t.Error("a close code is wrong")
	}
}

func TestCheck(t *testing.T) {
	good := []Message{
		{Type: Hello, Protocol: 1},
		{Type: Welcome},
		{Type: Start, Stream: 1, Args: []string{"analysis", "analysis"}},
		{Type: Started, Stream: 2},
		{Type: Stdin, Stream: 2},
		{Type: Stdout, Stream: 2, Data: "x"},
		{Type: Close, Stream: 2},
		{Type: Ended, Stream: 2},
		{Type: Run, ID: 1},
		{Type: Run, ID: 1, Args: []string{"version"}},
		{Type: Result, ID: 1},
	}
	for _, m := range good {
		if err := m.Check(); err != nil {
			t.Errorf("Check(%+v) = %v; want nil", m, err)
		}
	}

	bad := []Message{
		{},
		{Type: "ping"},
		{Type: "Hello", Protocol: 1},
		{Type: Hello},
		{Type: Start},
		{Type: Start, Stream: 1},
		{Type: Start, Args: []string{"gtp"}},
		{Type: Started},
		{Type: Stdin},
		{Type: Stdout},
		{Type: Close},
		{Type: Ended},
		{Type: Run},
		{Type: Run, Stream: 1},
		{Type: Result},
		{Type: Result, Stream: 1},
	}
	for _, m := range bad {
		if err := m.Check(); err == nil {
			t.Errorf("Check(%+v) = nil; want an error", m)
		}
	}
}
