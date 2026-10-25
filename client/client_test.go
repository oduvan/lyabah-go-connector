package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/oduvan/lyabah-go-connector/engine"
	"github.com/oduvan/lyabah-go-connector/protocol"
)

const testToken = "gct_test-token"

// fakeEngine is an engine.Engine whose processes echo stdin to stdout. It
// writes down what was done to it, in order.
type fakeEngine struct {
	// stubborn processes keep running after stdin closes, until killed.
	stubborn bool

	mu     sync.Mutex
	events []string
}

func (e *fakeEngine) event(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, s)
}

func (e *fakeEngine) Events() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.events)
}

func (e *fakeEngine) Start(ctx context.Context, args []string) (engine.Process, error) {
	if err := protocol.CheckProcessArgs(args); err != nil {
		return nil, err
	}
	p := newFakeProc(e, args[1])
	// Like the real engine: the process is killed when ctx ends.
	context.AfterFunc(ctx, func() { p.Kill() })
	e.event("start " + args[1])
	return p, nil
}

func (e *fakeEngine) Run(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	if err := protocol.CheckShortArgs(args); err != nil {
		return nil, err
	}
	e.event("run " + strings.Join(args, " ") + " <" + string(stdin))
	switch args[0] {
	case "list-sgf":
		return []byte("a1b2c3d4e5\nk7Qm2xPa9T\n"), nil
	case "del-user":
		return nil, errors.New("del-user failed")
	}
	return nil, nil
}

func (e *fakeEngine) Cleanup(ctx context.Context) error {
	e.event("cleanup")
	return nil
}

func (e *fakeEngine) Version(ctx context.Context) (string, error) {
	return "katago: v1.18.2", nil
}

var errKilled = errors.New("killed")

type fakeProc struct {
	e         *fakeEngine
	name      string
	inR, outR *io.PipeReader
	inW, outW *io.PipeWriter
	killOnce  sync.Once
	killedCh  chan struct{}
	done      chan struct{}
	err       error
}

func newFakeProc(e *fakeEngine, name string) *fakeProc {
	p := &fakeProc{e: e, name: name, killedCh: make(chan struct{}), done: make(chan struct{})}
	p.inR, p.inW = io.Pipe()
	p.outR, p.outW = io.Pipe()
	go func() {
		defer close(p.done)
		io.Copy(p.outW, p.inR)
		if e.stubborn {
			<-p.killedCh
		}
		select {
		case <-p.killedCh:
			p.err = errKilled
		default:
		}
		p.outW.Close()
	}()
	return p
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.inW }
func (p *fakeProc) Stdout() io.Reader     { return p.outR }
func (p *fakeProc) Stderr() string        { return "echo stderr" }
func (p *fakeProc) Wait() error {
	<-p.done
	return p.err
}

func (p *fakeProc) Kill() error {
	p.killOnce.Do(func() {
		select {
		case <-p.done:
			return // it ended already
		default:
		}
		p.e.event("kill " + p.name)
		close(p.killedCh)
		p.inR.CloseWithError(errKilled)
		p.outW.CloseWithError(errKilled)
	})
	return nil
}

// goban plays the Goban side of the connection.
type goban struct {
	t     *testing.T
	url   string
	conns chan *peer
	// refuse, when not 0, is the HTTP status every connection gets instead
	// of a WebSocket.
	refuse atomic.Int32
	// refused gets the time of each refused connection.
	refused chan time.Time
}

// peer is one connection, seen from the Goban side.
type peer struct {
	t         *testing.T
	c         *websocket.Conn
	hello     protocol.Message
	userAgent string
	at        time.Time // when the connection came
}

func newGoban(t *testing.T) *goban {
	g := &goban{t: t, conns: make(chan *peer, 10), refused: make(chan time.Time, 10)}
	srv := httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(srv.Close)
	g.url = srv.URL
	return g
}

func (g *goban) serve(w http.ResponseWriter, r *http.Request) {
	at := time.Now()
	if r.URL.Path != "/api/connector" {
		http.NotFound(w, r)
		return
	}
	if code := g.refuse.Load(); code != 0 {
		g.refused <- at
		http.Error(w, "no", int(code))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, `{"error":"The connector token is not valid."}`, http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		g.t.Errorf("accept: %v", err)
		return
	}
	c.SetReadLimit(protocol.MaxMessage)
	p := &peer{t: g.t, c: c, userAgent: r.Header.Get("User-Agent"), at: at}
	if p.hello, err = p.read(); err != nil {
		g.t.Errorf("reading hello: %v", err)
		return
	}
	if err := p.write(protocol.Message{Type: protocol.Welcome, ConnectorID: 5, Name: "Desk"}); err != nil {
		g.t.Errorf("sending welcome: %v", err)
		return
	}
	g.conns <- p
}

// accept waits for the next connection that got its welcome.
func (g *goban) accept() *peer {
	g.t.Helper()
	select {
	case p := <-g.conns:
		g.t.Cleanup(func() { p.c.CloseNow() })
		return p
	case <-time.After(5 * time.Second):
		g.t.Fatal("the connector did not connect")
		return nil
	}
}

func (p *peer) read() (protocol.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var m protocol.Message
	_, data, err := p.c.Read(ctx)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(data, &m)
	return m, err
}

func (p *peer) write(m protocol.Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.c.Write(ctx, websocket.MessageText, data)
}

func (p *peer) recv() protocol.Message {
	p.t.Helper()
	m, err := p.read()
	if err != nil {
		p.t.Fatalf("reading from the connector: %v", err)
	}
	return m
}

func (p *peer) send(m protocol.Message) {
	p.t.Helper()
	if err := p.write(m); err != nil {
		p.t.Fatalf("sending to the connector: %v", err)
	}
}

// running is a Run in the background.
type running struct {
	t      *testing.T
	cancel context.CancelFunc
	errc   chan error
	once   sync.Once
	err    error
}

// startClient runs the connector against g until the test ends.
func startClient(t *testing.T, g *goban, e engine.Engine, timing Timing, token string) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{t: t, cancel: cancel, errc: make(chan error, 1)}
	go func() {
		r.errc <- Run(ctx, Options{
			Server:   g.url,
			Token:    token,
			Engine:   e,
			Version:  "1.2.3",
			Mode:     "command",
			MaxGames: 2,
			Log:      slog.New(slog.DiscardHandler),
			Timing:   timing,
		})
	}()
	t.Cleanup(func() { r.stop() })
	return r
}

// wait waits for Run to return and gives its error.
func (r *running) wait() error {
	r.once.Do(func() {
		select {
		case r.err = <-r.errc:
		case <-time.After(10 * time.Second):
			r.t.Error("Run did not return")
		}
	})
	return r.err
}

// stop ends Run's context and waits for it to return.
func (r *running) stop() error {
	r.cancel()
	return r.wait()
}

func quick() Timing {
	return Timing{Backoff: []time.Duration{10 * time.Millisecond}}
}

func TestHelloAndWelcome(t *testing.T) {
	g := newGoban(t)
	e := &fakeEngine{}
	r := startClient(t, g, e, quick(), testToken)
	p := g.accept()

	want := protocol.Message{
		Type: protocol.Hello, Protocol: 1, Version: "1.2.3", OS: runtime.GOOS, Arch: runtime.GOARCH,
		Mode: "command", Katago: "katago: v1.18.2", MaxGames: 2,
	}
	if !equal(p.hello, want) {
		t.Errorf("hello = %+v; want %+v", p.hello, want)
	}
	if p.userAgent != "goban-connector/1.2.3" {
		t.Errorf("User-Agent = %q", p.userAgent)
	}
	if ev := e.Events(); len(ev) != 1 || ev[0] != "cleanup" {
		t.Errorf("events before serving = %q; want one cleanup", ev)
	}

	// When its context ends, Run closes the connection normally. Goban
	// reads all the time, so it answers the close at once. (The answer to
	// a run shows that the connector got the welcome and serves.)
	p.send(protocol.Message{Type: protocol.Run, ID: 1, Args: []string{"version"}})
	if m := p.recv(); m.Type != protocol.Result {
		t.Fatalf("after run: %+v; want the result", m)
	}
	closed := make(chan error, 1)
	go func() {
		_, err := p.read()
		closed <- err
	}()
	start := time.Now()
	if err := r.stop(); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v; want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Run took %v to stop", d)
	}
	if err := <-closed; websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Errorf("the connection ended with %v; want a normal close", err)
	}
}

func equal(a, b protocol.Message) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func TestStartStdinStdoutClose(t *testing.T) {
	g := newGoban(t)
	startClient(t, g, &fakeEngine{}, quick(), testToken)
	p := g.accept()

	p.send(protocol.Message{Type: protocol.Start, Stream: 1, Args: []string{"gtp", "game-k7Qm2xPa9T", "--user", "7"}})
	if m := p.recv(); !equal(m, protocol.Message{Type: protocol.Started, Stream: 1}) {
		t.Fatalf("after start: %+v; want started", m)
	}
	p.send(protocol.Message{Type: protocol.Stdin, Stream: 1, Data: "version\n"})
	var out strings.Builder
	for out.Len() < len("version\n") {
		m := p.recv()
		if m.Type != protocol.Stdout || m.Stream != 1 {
			t.Fatalf("after stdin: %+v; want stdout", m)
		}
		out.WriteString(m.Data)
	}
	if out.String() != "version\n" {
		t.Errorf("stdout = %q", out.String())
	}
	p.send(protocol.Message{Type: protocol.Close, Stream: 1})
	want := protocol.Message{Type: protocol.Ended, Stream: 1, Stderr: "echo stderr"}
	if m := p.recv(); !equal(m, want) {
		t.Errorf("after close: %+v; want %+v", m, want)
	}

	// Messages for a stream that has ended are left alone.
	p.send(protocol.Message{Type: protocol.Stdin, Stream: 1, Data: "late\n"})
	p.send(protocol.Message{Type: protocol.Close, Stream: 1})
	p.send(protocol.Message{Type: protocol.Run, ID: 1, Args: []string{"version"}})
	if m := p.recv(); m.Type != protocol.Result || m.ID != 1 {
		t.Errorf("after messages for an ended stream: %+v; want the result", m)
	}
}

func TestStdoutChunks(t *testing.T) {
	g := newGoban(t)
	startClient(t, g, &fakeEngine{}, quick(), testToken)
	p := g.accept()

	// 300000 bytes of a three-byte character: the pipe hands them over in
	// pieces that end inside a character.
	text := strings.Repeat("€", 100000)
	p.send(protocol.Message{Type: protocol.Start, Stream: 4, Args: []string{"analysis", "analysis"}})
	if m := p.recv(); m.Type != protocol.Started {
		t.Fatalf("after start: %+v", m)
	}
	p.send(protocol.Message{Type: protocol.Stdin, Stream: 4, Data: text})
	p.send(protocol.Message{Type: protocol.Close, Stream: 4})
	var out strings.Builder
	for {
		m := p.recv()
		if m.Type == protocol.Ended {
			if m.Error != "" {
				t.Errorf("ended with %q", m.Error)
			}
			break
		}
		if m.Type != protocol.Stdout || m.Stream != 4 {
			t.Fatalf("got %+v; want stdout", m)
		}
		if len(m.Data) > 64<<10 || !utf8.ValidString(m.Data) {
			t.Fatalf("a stdout chunk of %d bytes, valid UTF-8 %v", len(m.Data), utf8.ValidString(m.Data))
		}
		out.WriteString(m.Data)
	}
	if out.String() != text {
		t.Errorf("stdout has %d bytes; want the %d bytes sent", out.Len(), len(text))
	}
}

func TestRunShortCommand(t *testing.T) {
	g := newGoban(t)
	e := &fakeEngine{}
	startClient(t, g, e, quick(), testToken)
	p := g.accept()

	cases := []struct {
		run  protocol.Message
		want protocol.Message
	}{
		{
			protocol.Message{Type: protocol.Run, ID: 3, Args: []string{"list-sgf", "7"}},
			protocol.Message{Type: protocol.Result, ID: 3, Stdout: "a1b2c3d4e5\nk7Qm2xPa9T\n"},
		},
		{
			protocol.Message{Type: protocol.Run, ID: 4, Args: []string{"put-sgf", "7", "k7Qm2xPa9T"}, Stdin: "(;GM[1])"},
			protocol.Message{Type: protocol.Result, ID: 4},
		},
		{
			protocol.Message{Type: protocol.Run, ID: 5, Args: []string{"del-user", "7"}},
			protocol.Message{Type: protocol.Result, ID: 5, Error: "del-user failed"},
		},
		{
			protocol.Message{Type: protocol.Run, ID: 6, Args: []string{"rm", "-rf", "/"}},
			protocol.Message{Type: protocol.Result, ID: 6, Error: protocol.ErrBadCommand.Error()},
		},
	}
	for _, c := range cases {
		p.send(c.run)
		if m := p.recv(); !equal(m, c.want) {
			t.Errorf("run %q: %+v; want %+v", c.run.Args, m, c.want)
		}
	}
	if ev := e.Events(); !slices.Contains(ev, "run put-sgf 7 k7Qm2xPa9T <(;GM[1])") {
		t.Errorf("events = %q; want put-sgf with its stdin", ev)
	}
}

func TestRefusedStartIsEnded(t *testing.T) {
	g := newGoban(t)
	startClient(t, g, &fakeEngine{}, quick(), testToken)
	p := g.accept()

	// gtp needs --user.
	p.send(protocol.Message{Type: protocol.Start, Stream: 2, Args: []string{"gtp", "game-k7Qm2xPa9T"}})
	m := p.recv()
	if m.Type != protocol.Ended || m.Stream != 2 || !strings.Contains(m.Error, protocol.ErrBadCommand.Error()) {
		t.Errorf("after a refused start: %+v; want ended with the error", m)
	}
}

func TestProcessesKilledWhenConnectionDrops(t *testing.T) {
	g := newGoban(t)
	e := &fakeEngine{stubborn: true}
	startClient(t, g, e, quick(), testToken)
	p := g.accept()

	for i, name := range []string{"game-1", "game-2"} {
		p.send(protocol.Message{Type: protocol.Start, Stream: uint32(i + 1), Args: []string{"gtp", name, "--user", "7"}})
		if m := p.recv(); m.Type != protocol.Started {
			t.Fatalf("after start %s: %+v", name, m)
		}
	}
	p.c.CloseNow()

	// Both are killed before the next connection's cleanup.
	g.accept()
	ev := e.Events()
	second := -1
	for i, s := range ev {
		if s == "cleanup" && i > 0 {
			second = i
		}
	}
	k1, k2 := slices.Index(ev, "kill game-1"), slices.Index(ev, "kill game-2")
	if second < 0 || k1 < 0 || k2 < 0 || k1 > second || k2 > second {
		t.Errorf("events = %q; want both kills before the second cleanup", ev)
	}
}

func TestReconnectBackoff(t *testing.T) {
	g := newGoban(t)
	timing := Timing{Backoff: []time.Duration{100 * time.Millisecond, time.Second}}
	startClient(t, g, &fakeEngine{}, timing, testToken)
	p := g.accept()

	// After a connection that was online, the first wait.
	g.refuse.Store(http.StatusServiceUnavailable)
	lost := time.Now()
	p.c.CloseNow()
	var tried time.Time
	select {
	case tried = <-g.refused:
	case <-time.After(5 * time.Second):
		t.Fatal("the connector did not come back")
	}
	if d := tried.Sub(lost); d < 100*time.Millisecond || d >= time.Second {
		t.Errorf("came back after %v; want the first wait, 100ms", d)
	}

	// After a failed try, the next wait.
	g.refuse.Store(0)
	p = g.accept()
	if d := p.at.Sub(tried); d < time.Second {
		t.Errorf("tried again after %v; want the second wait, 1s", d)
	}

	// Being online again starts the waits from the first one.
	lost = time.Now()
	p.c.CloseNow()
	p = g.accept()
	if d := p.at.Sub(lost); d < 100*time.Millisecond || d >= time.Second {
		t.Errorf("came back after %v; want the first wait again, 100ms", d)
	}
}

func TestMissedPingsDropTheConnection(t *testing.T) {
	g := newGoban(t)
	e := &fakeEngine{}
	timing := Timing{Ping: 50 * time.Millisecond, Missed: 2, Backoff: []time.Duration{10 * time.Millisecond}}
	startClient(t, g, e, timing, testToken)

	// This side never reads, so no ping gets its answer.
	g.accept()
	g.accept()
}

func TestUnauthorizedStops(t *testing.T) {
	// The HTTP answer before the WebSocket.
	g := newGoban(t)
	r := startClient(t, g, &fakeEngine{}, quick(), "gct_wrong")
	if err := r.wait(); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Run with a wrong token = %v; want ErrUnauthorized", err)
	}

	// The close codes of a token that is not valid any more, and of a
	// removed connector.
	for _, code := range []websocket.StatusCode{protocol.CloseBadToken, protocol.CloseRemoved} {
		g := newGoban(t)
		r := startClient(t, g, &fakeEngine{}, quick(), testToken)
		p := g.accept()
		p.c.Close(code, "")
		if err := r.wait(); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Run after close %d = %v; want ErrUnauthorized", code, err)
		}
	}
}

func TestReplacedStops(t *testing.T) {
	g := newGoban(t)
	e := &fakeEngine{stubborn: true}
	r := startClient(t, g, e, quick(), testToken)
	p := g.accept()
	p.send(protocol.Message{Type: protocol.Start, Stream: 1, Args: []string{"gtp", "game-1", "--user", "7"}})
	if m := p.recv(); m.Type != protocol.Started {
		t.Fatalf("after start: %+v", m)
	}
	p.c.Close(protocol.CloseReplaced, "")
	if err := r.wait(); !errors.Is(err, ErrReplaced) {
		t.Errorf("Run after close 4409 = %v; want ErrReplaced", err)
	}
	if ev := e.Events(); !slices.Contains(ev, "kill game-1") {
		t.Errorf("events = %q; want the process killed", ev)
	}
}

func TestBadMessageClosesTheConnection(t *testing.T) {
	g := newGoban(t)
	startClient(t, g, &fakeEngine{}, quick(), testToken)
	p := g.accept()
	p.send(protocol.Message{Type: protocol.Start, Stream: 1}) // no args
	if _, err := p.read(); websocket.CloseStatus(err) != protocol.CloseBadMessage {
		t.Errorf("after a bad message: %v; want close %d", err, protocol.CloseBadMessage)
	}
	// It connects again.
	g.accept()
}

func TestServerURL(t *testing.T) {
	cases := map[string]string{
		"https://go.lyabah.com":      "wss://go.lyabah.com/api/connector",
		"https://go.lyabah.com/":     "wss://go.lyabah.com/api/connector",
		"http://localhost:8080":      "ws://localhost:8080/api/connector",
		"https://example.com/goban/": "wss://example.com/goban/api/connector",
	}
	for server, want := range cases {
		if got, err := serverURL(server, "/api/connector", true); err != nil || got != want {
			t.Errorf("serverURL(%q) = %q, %v; want %q", server, got, err, want)
		}
	}
	for _, bad := range []string{"go.lyabah.com", "ftp://go.lyabah.com", "https://", "wss://go.lyabah.com"} {
		if got, err := serverURL(bad, "/api/connector", true); err == nil {
			t.Errorf("serverURL(%q) = %q; want an error", bad, got)
		}
	}
}

// hangEngine starts processes that ignore Kill and whose stdout never ends
// by itself, like a killed process whose child still holds the pipe.
type hangEngine struct {
	fakeEngine
	proc chan *hangProc
}

func (e *hangEngine) Start(ctx context.Context, args []string) (engine.Process, error) {
	if err := protocol.CheckProcessArgs(args); err != nil {
		return nil, err
	}
	p := &hangProc{closed: make(chan struct{}), waited: make(chan struct{})}
	e.event("start " + args[1])
	e.proc <- p
	return p, nil
}

// hangProc is a process that cannot be killed. Its stdout ends only when
// the connector closes it.
type hangProc struct {
	closeOnce sync.Once
	closed    chan struct{} // the connector closed stdout or stdin
	waited    chan struct{} // Wait has returned
}

func (p *hangProc) Stdin() io.WriteCloser { return hangStdin{p} }
func (p *hangProc) Stdout() io.Reader     { return hangStdout{p} }
func (p *hangProc) Stderr() string        { return "" }
func (p *hangProc) Kill() error           { return nil }
func (p *hangProc) Wait() error {
	<-p.closed
	close(p.waited)
	return errKilled
}
func (p *hangProc) release() { p.closeOnce.Do(func() { close(p.closed) }) }

type hangStdin struct{ p *hangProc }

func (s hangStdin) Write(b []byte) (int, error) { return len(b), nil }
func (s hangStdin) Close() error                { return nil }

type hangStdout struct{ p *hangProc }

func (s hangStdout) Read([]byte) (int, error) {
	<-s.p.closed
	return 0, io.ErrClosedPipe
}
func (s hangStdout) Close() error { s.p.release(); return nil }

// syncBuffer is a bytes.Buffer that many goroutines may write.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestStuckProcessDoesNotHoldTheReconnect(t *testing.T) {
	g := newGoban(t)
	e := &hangEngine{proc: make(chan *hangProc, 1)}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, Options{
			Server: g.url, Token: testToken, Engine: e, Version: "1.2.3", Mode: "command", MaxGames: 2,
			Log:    slog.New(slog.NewTextHandler(logs, nil)),
			Timing: Timing{Backoff: []time.Duration{10 * time.Millisecond}, StreamWait: 100 * time.Millisecond},
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errc:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})

	p := g.accept()
	p.send(protocol.Message{Type: protocol.Start, Stream: 1, Args: []string{"gtp", "game-1", "--user", "7"}})
	if m := p.recv(); m.Type != protocol.Started {
		t.Fatalf("after start: %+v", m)
	}
	proc := <-e.proc
	p.c.CloseNow()

	// The stuck process does not stop the next connection.
	g.accept()
	if !strings.Contains(logs.String(), "did not end") {
		t.Errorf("log = %q; want a warning that a process did not end", logs.String())
	}
	// The connector closed the pipes, so the stuck goroutines can end.
	select {
	case <-proc.waited:
	case <-time.After(5 * time.Second):
		t.Error("the pipes of the stuck process were not closed")
	}
}
