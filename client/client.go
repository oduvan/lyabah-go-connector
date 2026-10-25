// Package client is the connector's side of the connection to Goban: one
// outbound WebSocket that the connector keeps open and opens again when it
// drops.
//
// Goban sends the wrapper's commands over it. Each KataGo process is a
// numbered stream: its stdin and stdout travel as stdin and stdout
// messages. Short commands (put-sgf, list-sgf, …) are run messages with a
// result as the answer. When the connection drops, every process is
// killed: KataGo must not run with nobody listening.
//
// The token is a secret: it is never logged, and errors never show it.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/oduvan/lyabah-go-connector/engine"
	"github.com/oduvan/lyabah-go-connector/protocol"
)

// DefaultServer is Goban's address.
const DefaultServer = "https://go.lyabah.com"

var (
	// ErrUnauthorized means Goban does not take the token: it is not valid,
	// or the connector was removed. Trying again is useless; the connector
	// must be paired again.
	ErrUnauthorized = errors.New("the connector token is not valid")
	// ErrReplaced means a newer connection with the same token took this
	// one's place: the connector runs twice.
	ErrReplaced = errors.New("another connector with the same token connected")
)

// Timing holds the connection's waits. A zero field takes its default;
// tests make them short.
type Timing struct {
	// Ping is how often the connector asks Goban whether it is there, and
	// how long each answer may take (default 15 s).
	Ping time.Duration
	// Missed is how many answers in a row may be missing before the
	// connection counts as lost (default 2).
	Missed int
	// Backoff are the waits before the next try after a failed or lost
	// connection; the last one repeats (default 1, 2, 5, 10 s).
	Backoff []time.Duration
}

// DefaultTiming is the timing of the real connection.
func DefaultTiming() Timing {
	return Timing{
		Ping:    15 * time.Second,
		Missed:  2,
		Backoff: []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second},
	}
}

// withDefaults fills the zero fields of t.
func (t Timing) withDefaults() Timing {
	d := DefaultTiming()
	if t.Ping <= 0 {
		t.Ping = d.Ping
	}
	if t.Missed <= 0 {
		t.Missed = d.Missed
	}
	if len(t.Backoff) == 0 {
		t.Backoff = d.Backoff
	}
	return t
}

// Options configure Run.
type Options struct {
	// Server is Goban's address, like https://go.lyabah.com. Empty means
	// DefaultServer.
	Server string
	// Token is the connector token that pairing gave.
	Token string
	// Engine runs the commands.
	Engine engine.Engine
	// Version is the connector's version.
	Version string
	// Mode is "command" or "native", as the hello message says it.
	Mode string
	// MaxGames is how many games may play on this computer at once.
	MaxGames int
	// Log receives the log lines. Nil means slog.Default().
	Log *slog.Logger
	// Timing changes the waits; zero fields take the defaults.
	Timing Timing
}

// The waits that are not in Timing.
const (
	// handshakeTimeout bounds connecting, the hello and the welcome.
	handshakeTimeout = 15 * time.Second
	// prepareTimeout bounds cleanup and reading KataGo's version.
	prepareTimeout = 60 * time.Second
	// closeWait is how long a closed stream's process may take to end after
	// its stdin is closed, before it is killed.
	closeWait = 2 * time.Second
	// stillOffline is how often a connector that keeps failing to connect
	// says so again in the log.
	stillOffline = time.Hour
)

// Run keeps the connection to Goban until ctx ends, and then returns
// ctx's error. It returns ErrUnauthorized when Goban refuses the token and
// ErrReplaced when another connection took this one's place; in both cases
// trying again does not help.
func Run(ctx context.Context, o Options) error {
	if o.Server == "" {
		o.Server = DefaultServer
	}
	wsURL, err := serverURL(o.Server, "/api/connector", true)
	if err != nil {
		return err
	}
	if o.Token == "" {
		return errors.New("there is no connector token: pair the connector first")
	}
	if o.Engine == nil {
		return errors.New("there is no engine")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	r := &runner{o: o, url: wsURL, timing: o.Timing.withDefaults(), log: o.Log, needPrepare: true}
	return r.loop(ctx)
}

// serverURL is the address of path on server. With ws it is a WebSocket
// address: https becomes wss and http becomes ws.
func serverURL(server, path string, ws bool) (string, error) {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("the server %q is not an http or https address", server)
	}
	if ws {
		u.Scheme = map[string]string{"https": "wss", "http": "ws"}[u.Scheme]
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath, u.RawQuery, u.Fragment, u.User = "", "", "", nil
	return u.String(), nil
}

// userAgent is the User-Agent of the connector's requests.
func userAgent(version string) string { return "goban-connector/" + version }

// runner is one Run.
type runner struct {
	o      Options
	url    string
	timing Timing
	log    *slog.Logger

	// needPrepare says that cleanup must run before the next connection:
	// at the start, and after each connection on which processes could run.
	needPrepare bool
	// katago is the first line of KataGo's version, for the hello.
	katago string
}

// loop connects, keeps the connection and connects again, until ctx ends.
func (r *runner) loop(ctx context.Context) error {
	step := 0
	// A computer that is away from the network for hours must not fill the
	// log: the first failure of a run of failures is a warning, the rest
	// are debug lines, with a reminder every stillOffline.
	var warnedAt time.Time
	for {
		wasOnline, err := r.attempt(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrReplaced) {
			return err
		}
		if wasOnline {
			step = 0
			warnedAt = time.Time{}
			r.log.Warn("The connection to Goban was lost", "reason", err.Error())
		} else {
			level := slog.LevelDebug
			if warnedAt.IsZero() || time.Since(warnedAt) >= stillOffline {
				level = slog.LevelWarn
				warnedAt = time.Now()
			}
			r.log.Log(ctx, level, "Could not connect to Goban", "reason", err.Error())
		}

		d := r.timing.Backoff[min(step, len(r.timing.Backoff)-1)]
		step++
		if sleep(ctx, d) != nil {
			return ctx.Err()
		}
	}
}

// sleep waits for d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// attempt makes one connection and keeps it until it is lost. wasOnline
// says whether Goban welcomed it; err says why it ended.
func (r *runner) attempt(ctx context.Context) (wasOnline bool, err error) {
	if r.needPrepare {
		if err := r.prepare(ctx); err != nil {
			return false, err
		}
		r.needPrepare = false
	}

	ws, welcome, err := r.connect(ctx)
	if err != nil {
		return false, err
	}
	defer ws.CloseNow()
	// From here on processes may start, so the next connection needs a
	// cleanup first.
	r.needPrepare = true
	r.log.Info("Connected to Goban", "name", welcome.Name, "connectorId", welcome.ConnectorID)

	c := newConn(ctx, r, ws)
	return true, c.serve(ctx)
}

// prepare runs cleanup and reads KataGo's version. A failed cleanup fails
// the connection: what is left behind may still hold the computer's
// memory, so no process may start. A failed version does not; the hello
// then has no KataGo version.
func (r *runner) prepare(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	err := r.o.Engine.Cleanup(cctx)
	cancel()
	if err != nil {
		return fmt.Errorf("cleanup failed: %w", err)
	}

	vctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	r.katago, err = r.o.Engine.Version(vctx)
	cancel()
	if err != nil && ctx.Err() == nil {
		r.log.Warn("Could not read the KataGo version", "err", err)
	}
	return nil
}

// connect opens the WebSocket, sends hello and waits for welcome.
func (r *runner) connect(ctx context.Context) (*websocket.Conn, protocol.Message, error) {
	var welcome protocol.Message
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+r.o.Token)
	header.Set("User-Agent", userAgent(r.o.Version))
	ws, resp, err := websocket.Dial(hctx, r.url, &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, welcome, ErrUnauthorized
		}
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, welcome, fmt.Errorf("Goban answered %s", resp.Status)
		}
		return nil, welcome, err
	}
	ws.SetReadLimit(protocol.MaxMessage)

	fail := func(err error) (*websocket.Conn, protocol.Message, error) {
		ws.CloseNow()
		return nil, welcome, err
	}
	hello := protocol.Message{
		Type:     protocol.Hello,
		Protocol: protocol.Version,
		Version:  r.o.Version,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Mode:     r.o.Mode,
		Katago:   r.katago,
		MaxGames: r.o.MaxGames,
	}
	if err := write(hctx, ws, hello); err != nil {
		return fail(closeReason(err))
	}
	_, data, err := ws.Read(hctx)
	if err != nil {
		return fail(closeReason(err))
	}
	if err := json.Unmarshal(data, &welcome); err != nil || welcome.Type != protocol.Welcome {
		ws.Close(protocol.CloseBadMessage, "expected welcome")
		return nil, welcome, errors.New("Goban did not answer the hello with a welcome")
	}
	return ws, welcome, nil
}

// closeReason turns a closed connection's close code into the error Run
// returns for it.
func closeReason(err error) error {
	switch websocket.CloseStatus(err) {
	case protocol.CloseBadToken, protocol.CloseRemoved:
		return ErrUnauthorized
	case protocol.CloseReplaced:
		return ErrReplaced
	}
	return err
}

// write sends one message.
func write(ctx context.Context, ws *websocket.Conn, m protocol.Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, data)
}

// errBadMessage is why the connector closes a connection on which Goban
// sent a message that is not valid.
var errBadMessage = errors.New("Goban sent a message that is not valid")

// errMissedPings is why the connection is lost when Goban stops answering.
var errMissedPings = errors.New("Goban did not answer the pings")

// conn is one welcomed connection.
type conn struct {
	r  *runner
	ws *websocket.Conn

	// ctx lasts as long as the connection. The processes are started with
	// it, so they end with the connection.
	ctx    context.Context
	cancel context.CancelFunc
	// ioCtx is for the WebSocket's reads and writes. It is not tied to
	// Run's ctx, because when a read's context ends the library drops the
	// connection without a close message.
	ioCtx    context.Context
	ioCancel context.CancelFunc

	out     chan protocol.Message // to the writer
	streams *streams
	wg      sync.WaitGroup // every goroutine of the connection

	mu     sync.Mutex
	reason error // why the connection ends, when this side ended it
}

func newConn(ctx context.Context, r *runner, ws *websocket.Conn) *conn {
	c := &conn{r: r, ws: ws, out: make(chan protocol.Message)}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.ioCtx, c.ioCancel = context.WithCancel(context.Background())
	c.streams = newStreams(c)
	return c
}

// serve reads messages until the connection ends. Then it kills every
// process and waits until all of them and every other goroutine of the
// connection have ended.
func (c *conn) serve(ctx context.Context) error {
	c.wg.Add(2)
	go c.writer()
	go c.pinger()
	// When Run's ctx ends, close the connection the normal way.
	stop := context.AfterFunc(ctx, func() {
		c.ws.Close(websocket.StatusNormalClosure, "the connector stops")
	})

	err := c.readLoop()

	stop()
	c.cancel()
	c.ioCancel()
	c.ws.CloseNow()
	c.streams.killAll()
	c.wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reason != nil {
		return c.reason
	}
	return closeReason(err)
}

// end ends the connection for reason, at once.
func (c *conn) end(reason error) {
	c.mu.Lock()
	if c.reason == nil {
		c.reason = reason
	}
	c.mu.Unlock()
	c.ws.CloseNow()
}

// readLoop reads and handles messages until the connection ends.
func (c *conn) readLoop() error {
	for {
		typ, data, err := c.ws.Read(c.ioCtx)
		if err != nil {
			return err
		}
		var m protocol.Message
		if typ != websocket.MessageText || json.Unmarshal(data, &m) != nil || m.Check() != nil || !c.handle(m) {
			c.mu.Lock()
			if c.reason == nil {
				c.reason = errBadMessage
			}
			c.mu.Unlock()
			c.ws.Close(protocol.CloseBadMessage, "bad message")
			return errBadMessage
		}
	}
}

// handle acts on one checked message. It returns false for a message
// Goban must not send.
func (c *conn) handle(m protocol.Message) bool {
	switch m.Type {
	case protocol.Start:
		return c.streams.start(m.Stream, m.Args)
	case protocol.Stdin:
		c.streams.write(m.Stream, m.Data)
	case protocol.Close:
		c.streams.close(m.Stream)
	case protocol.Run:
		c.wg.Add(1)
		go c.run(m)
	default:
		return false
	}
	return true
}

// maxResult is the most a short command's result may hold. Even with every
// byte escaped in the JSON, the message stays below protocol.MaxMessage.
const maxResult = 512 << 10

// run runs one short command and sends its result.
func (c *conn) run(m protocol.Message) {
	defer c.wg.Done()
	out, err := c.r.o.Engine.Run(c.ctx, m.Args, []byte(m.Stdin))
	res := protocol.Message{Type: protocol.Result, ID: m.ID}
	switch {
	case err != nil:
		res.Error = err.Error()
	case len(out) > maxResult:
		res.Error = fmt.Sprintf("the command printed more than %d bytes", maxResult)
	default:
		res.Stdout = string(out)
	}
	c.send(res)
}

// send hands m to the writer. It returns false when the connection has
// ended.
func (c *conn) send(m protocol.Message) bool {
	select {
	case c.out <- m:
		return true
	case <-c.ctx.Done():
		return false
	}
}

// writer owns the WebSocket's writes: it sends the messages in the order
// they come.
func (c *conn) writer() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case m := <-c.out:
			if err := write(c.ioCtx, c.ws, m); err != nil {
				c.end(fmt.Errorf("could not send to Goban: %w", err))
				return
			}
		}
	}
}

// pinger asks Goban every Timing.Ping whether it is there, and ends the
// connection when Timing.Missed answers in a row do not come.
func (c *conn) pinger() {
	defer c.wg.Done()
	t := time.NewTicker(c.r.timing.Ping)
	defer t.Stop()
	missed := 0
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(c.ioCtx, c.r.timing.Ping)
		err := c.ws.Ping(pctx)
		cancel()
		if err == nil {
			missed = 0
			continue
		}
		missed++
		if missed >= c.r.timing.Missed {
			c.end(errMissedPings)
			return
		}
	}
}
