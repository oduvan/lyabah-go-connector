package client

import (
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oduvan/lyabah-go-connector/engine"
	"github.com/oduvan/lyabah-go-connector/protocol"
)

// maxChunk is the most stdout one stdout message carries.
const maxChunk = 64 << 10

// streams is the table of a connection's processes, by stream number.
type streams struct {
	c *conn

	mu sync.Mutex
	m  map[uint32]*stream
}

func newStreams(c *conn) *streams {
	return &streams{c: c, m: map[uint32]*stream{}}
}

// stream is one process. Its stdin goes through a queue, so that a process
// that does not read its stdin never holds up the connection.
type stream struct {
	id uint32

	mu      sync.Mutex
	queue   []string      // stdin not yet written
	closing bool          // close came: close stdin after the queue
	wake    chan struct{} // a signal for the stdin writer
	proc    engine.Process
	killed  bool          // kill came: a process that starts later is killed at once
	timer   *time.Timer   // kills the process closeWait after close
	ended   chan struct{} // closed when the process has ended
}

// start starts a process for stream id. It returns false when that stream
// runs already.
func (t *streams) start(id uint32, args []string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.m[id]; ok {
		return false
	}
	s := &stream{id: id, wake: make(chan struct{}, 1), ended: make(chan struct{})}
	t.m[id] = s
	t.c.wg.Add(1)
	go t.run(s, args)
	return true
}

// get gives the stream id, or nil when it does not run. Goban may still
// send stdin or close for a stream that just ended; those are left alone.
func (t *streams) get(id uint32) *stream {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[id]
}

func (t *streams) remove(id uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, id)
}

// write queues data for stream id's stdin.
func (t *streams) write(id uint32, data string) {
	if s := t.get(id); s != nil {
		s.mu.Lock()
		if !s.closing {
			s.queue = append(s.queue, data)
		}
		s.mu.Unlock()
		s.signal()
	}
}

// close closes stream id's stdin once the queue is written, and kills the
// process if it has not ended closeWait later.
func (t *streams) close(id uint32) {
	s := t.get(id)
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closing {
		s.closing = true
		s.timer = time.AfterFunc(closeWait, s.kill)
	}
	s.mu.Unlock()
	s.signal()
}

// killAll kills every process of the connection.
func (t *streams) killAll() {
	t.mu.Lock()
	all := make([]*stream, 0, len(t.m))
	for _, s := range t.m {
		all = append(all, s)
	}
	t.mu.Unlock()
	for _, s := range all {
		s.kill()
	}
}

// closePipes closes the pipes of every process that is still in the table.
// It is for processes that did not end after a kill: with the pipes closed
// the goroutines that read and write them can end.
func (t *streams) closePipes() {
	t.mu.Lock()
	all := make([]*stream, 0, len(t.m))
	for _, s := range t.m {
		all = append(all, s)
	}
	t.mu.Unlock()
	for _, s := range all {
		s.mu.Lock()
		p := s.proc
		s.mu.Unlock()
		if p == nil {
			continue
		}
		p.Stdin().Close()
		if c, ok := p.Stdout().(io.Closer); ok {
			c.Close()
		}
	}
}

func (s *stream) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// kill kills the process, or makes sure it is killed as soon as it has
// started.
func (s *stream) kill() {
	s.mu.Lock()
	s.killed = true
	p := s.proc
	s.mu.Unlock()
	if p != nil {
		p.Kill()
	}
}

// run starts the process and serves it until it ends. Then it sends ended,
// the stream's last message.
func (t *streams) run(s *stream, args []string) {
	defer t.c.wg.Done()
	c := t.c
	p, err := c.r.o.Engine.Start(c.ctx, args)
	if err != nil {
		t.remove(s.id)
		c.send(protocol.Message{Type: protocol.Ended, Stream: s.id, Error: err.Error()})
		return
	}
	s.mu.Lock()
	s.proc = p
	killed := s.killed
	s.mu.Unlock()
	if killed {
		p.Kill()
	}
	c.send(protocol.Message{Type: protocol.Started, Stream: s.id})

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		s.feed(p)
	}()
	t.copyStdout(s.id, p.Stdout())
	werr := p.Wait()
	close(s.ended)
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()

	t.remove(s.id)
	ended := protocol.Message{Type: protocol.Ended, Stream: s.id, Stderr: p.Stderr()}
	if werr != nil {
		ended.Error = werr.Error()
	}
	c.send(ended)
}

// feed writes the queue to the process's stdin, and closes it after close.
// It returns when stdin is closed or the process has ended.
func (s *stream) feed(p engine.Process) {
	broken := false // a write failed: the rest is dropped
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			data := s.queue[0]
			s.queue[0] = ""
			s.queue = s.queue[1:]
			s.mu.Unlock()
			if !broken {
				if _, err := io.WriteString(p.Stdin(), data); err != nil {
					broken = true
				}
			}
			continue
		}
		closing := s.closing
		s.mu.Unlock()
		if closing {
			p.Stdin().Close()
			return
		}
		select {
		case <-s.wake:
		case <-s.ended:
			return
		}
	}
}

// copyStdout sends what r gives as stdout messages, until r ends. A
// message ends at a whole UTF-8 character, because its data is JSON text.
func (t *streams) copyStdout(id uint32, r io.Reader) {
	buf := make([]byte, maxChunk)
	held := 0 // bytes of an unfinished character at the start of buf
	for {
		n, err := r.Read(buf[held:])
		n += held
		k := n
		if err == nil {
			k = wholeChars(buf[:n])
		}
		if k > 0 {
			// When the connection has ended this sends nothing, but the
			// reading goes on until the killed process's stdout ends.
			t.c.send(protocol.Message{Type: protocol.Stdout, Stream: id, Data: string(buf[:k])})
		}
		held = copy(buf, buf[k:n])
		if err != nil {
			return
		}
	}
}

// wholeChars is how much of b ends at a whole UTF-8 character: all of b,
// less an unfinished character at its end.
func wholeChars(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return len(b)
			}
			return i
		}
	}
	return len(b)
}
