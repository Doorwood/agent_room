// Package client implements the asynchronous shared-room line client.
package client

import (
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
type realClock struct{}
type realTimer struct{ *time.Timer }

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }
func (t realTimer) C() <-chan time.Time          { return t.Timer.C }

type Deps struct {
	Launcher Launcher
	Clock    Clock
	Random   io.Reader
	Cursors  CursorStore
}
type Client struct{ deps Deps }

func New(deps Deps) *Client {
	if deps.Clock == nil {
		deps.Clock = realClock{}
	}
	if deps.Random == nil {
		deps.Random = rand.Reader
	}
	if deps.Cursors == nil {
		deps.Cursors = FileCursors{}
	}
	return &Client{deps: deps}
}

type received struct {
	env protocol.Envelope
	err error
}

var errProbe = errors.New("identity probe complete")
var errInputEOF = errors.New("input closed")

// A full queue rejects newly pasted lines without preventing the scanner from
// finding /quit. The state loop reports an aggregate count, including on exit.
type lineInput struct {
	lines    chan string
	overflow chan struct{}
	rejected atomic.Uint64
}

func (input *lineInput) report(out io.Writer) {
	if count := input.rejected.Swap(0); count > 0 {
		fmt.Fprintf(out, "input queue full: rejected %d input lines; retry them after connecting\n", count)
	}
}

// Run owns input; closing it must interrupt Read. Output and diagnostics are
// written only by the state loop. /quit cancels even an in-flight SSH launch.
func (c *Client) Run(parent context.Context, target string, input io.ReadCloser, output, diagnostics io.Writer) error {
	if err := ValidateTarget(target); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer input.Close()
	queue := &lineInput{lines: make(chan string, 64), overflow: make(chan struct{}, 1)}
	inputErrors := make(chan error, 1)
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer close(queue.lines)
		s := bufio.NewScanner(input)
		s.Buffer(make([]byte, 4096), 1<<20)
		for s.Scan() {
			line := s.Text()
			cmd, e := ParseCommand(line)
			if e == nil && cmd.Kind == CommandQuit {
				cancel()
				return
			}
			select {
			case queue.lines <- line:
			case <-ctx.Done():
				return
			default:
				queue.rejected.Add(1)
				select {
				case queue.overflow <- struct{}{}:
				default:
				}
			}
		}
		if err := s.Err(); err != nil {
			inputErrors <- err
		}
	}()
	defer func() { cancel(); input.Close(); <-inputDone; queue.report(diagnostics) }()
	cursor, err := c.deps.Cursors.Load(target)
	if err != nil {
		return err
	}
	probe := cursor.RoomID != ""
	p := projection{partial: make(map[itemKey]string)}
	pending := make(map[string]protocol.Envelope)
	var order []string
	launcher := c.deps.Launcher
	if launcher == nil {
		launcher = SSHLauncher{Stderr: diagnostics}
	}
	backoff := 250 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return nil
		}
		err = c.session(ctx, launcher, target, queue, output, diagnostics, &cursor, &probe, &p, pending, &order)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errProbe) {
			continue
		}
		if errors.Is(err, errInputEOF) {
			select {
			case err := <-inputErrors:
				return err
			default:
			}
			return nil
		}
		if errors.Is(err, errFatal) {
			return err
		}
		fmt.Fprintln(diagnostics, SafeText(fmt.Sprintf("connection lost: %v", err)))
		delay, e := c.jitter(backoff)
		if e != nil {
			return e
		}
		timer := c.deps.Clock.NewTimer(delay)
	backoffWait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C():
				break backoffWait
			case <-queue.overflow:
				queue.report(diagnostics)
			}
		}
		if backoff < 10*time.Second {
			backoff *= 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
		}
	}
}

var errFatal = errors.New("client stopped")

func fatal(err error) error { return fmt.Errorf("%w: %v", errFatal, err) }
func (c *Client) jitter(base time.Duration) (time.Duration, error) {
	var b [8]byte
	if _, err := io.ReadFull(c.deps.Random, b[:]); err != nil {
		return 0, err
	}
	return base/2 + time.Duration(binary.BigEndian.Uint64(b[:])%uint64(base/2+1)), nil
}
func (c *Client) session(ctx context.Context, launcher Launcher, target string, queue *lineInput, out, diag io.Writer, cursor *Cursor, probe *bool, p *projection, pending map[string]protocol.Envelope, order *[]string) error {
	conn, err := launcher.Start(ctx, target)
	if err != nil {
		return err
	}
	defer conn.Close()
	sessionCtx, stop := context.WithCancel(ctx)
	defer stop()
	frames := make(chan received, 1)
	writes := make(chan protocol.Envelope, 128)
	writeErr := make(chan error, 1)
	readerDone := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		r := protocol.NewReader(conn.Reader, protocol.MaxFrameBytes)
		for {
			e, err := r.Read()
			select {
			case frames <- received{e, err}:
			case <-sessionCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(writerDone)
		w := protocol.NewWriter(conn.Writer)
		for {
			select {
			case <-sessionCtx.Done():
				return
			case e := <-writes:
				if err := w.Write(e); err != nil {
					writeErr <- err
					return
				}
			}
		}
	}()
	defer func() { stop(); conn.Close(); <-readerDone; <-writerDone }()
	send := func(e protocol.Envelope) error {
		select {
		case writes <- e:
			return nil
		default:
			return errors.New("outbound queue full")
		}
	}
	seq := cursor.LastAppliedSeq
	if *probe {
		seq = 0
	}
	hello, err := request(c.deps.Random, "hello", protocol.Hello{MinVersion: 1, MaxVersion: 1, LastAppliedSeq: seq})
	if err != nil {
		return fatal(err)
	}
	if err = send(hello); err != nil {
		return err
	}
	welcomed, ready := false, false
	lastFrame := c.deps.Clock.Now()
	lastSend := lastFrame
	timer := c.deps.Clock.NewTimer(20 * time.Second)
	defer func() { timer.Stop() }()
	for {
		var input <-chan string
		if ready {
			input = queue.lines
		}
		select {
		case <-queue.overflow:
			queue.report(diag)
		case <-ctx.Done():
			return ctx.Err()
		case err := <-writeErr:
			return err
		case <-timer.C():
			now := c.deps.Clock.Now()
			if now.Sub(lastFrame) >= 60*time.Second {
				return errors.New("peer liveness timeout")
			}
			if ready && now.Sub(lastSend) >= 20*time.Second {
				e, err := request(c.deps.Random, "heartbeat", protocol.Heartbeat{UnixMilli: now.UnixMilli()})
				if err != nil {
					return fatal(err)
				}
				if err = send(e); err != nil {
					return err
				}
				lastSend = now
			}
			timer = c.deps.Clock.NewTimer(20 * time.Second)
		case line, ok := <-input:
			if !ok {
				return errInputEOF
			}
			cmd, err := ParseCommand(line)
			if err != nil {
				fmt.Fprintln(diag, err)
				continue
			}
			env, mutation, err := cmd.Envelope(c.deps.Random, p.active)
			if err != nil {
				fmt.Fprintln(diag, SafeText(err.Error()))
				continue
			}
			if mutation {
				pending[env.ID] = env
				*order = append(*order, env.ID)
			}
			if err = send(env); err != nil {
				return err
			}
			lastSend = c.deps.Clock.Now()
		case rr := <-frames:
			if rr.err != nil {
				return rr.err
			}
			e := rr.env
			lastFrame = c.deps.Clock.Now()
			if !welcomed {
				if e.Kind != protocol.KindResponse || e.Method != "welcome" || e.ID != hello.ID {
					if cursor.RoomID != "" {
						*probe = true
					}
					return errors.New("expected welcome")
				}
				w, err := protocol.DecodeBody[protocol.Welcome](e.Body)
				if err != nil {
					return fatal(err)
				}
				if !w.FullOwnerAccess {
					return fatal(errors.New("server did not confirm fullOwnerAccess"))
				}
				if cursor.RoomID != "" && cursor.RoomID != w.RoomID {
					if len(pending) > 0 {
						return fatal(errors.New("room changed with unacknowledged mutations"))
					}
					cursor.LastAppliedSeq = 0
					cursor.RoomID = w.RoomID
					cursor.Target = target
					*probe = false
					p.snapshot(protocol.RuntimeSnapshot{})
					if err = c.deps.Cursors.Save(*cursor); err != nil {
						return fatal(err)
					}
					// This server replayed using the previous room's cursor.
					// Restart before accepting any of those replay frames.
					return errProbe
				}
				if cursor.LastAppliedSeq > w.LatestSeq {
					cursor.LastAppliedSeq = 0
				}
				cursor.RoomID = w.RoomID
				cursor.Target = target
				p.active = w.ActiveTurnID
				if *probe {
					*probe = false
					return errProbe
				}
				if err = c.deps.Cursors.Save(*cursor); err != nil {
					return fatal(err)
				}
				if _, err = fmt.Fprintf(out, "Room: %s (%s)\nProject: %s\nExecution owner: %s\nActive turn: %s\n%s\n", SafeText(w.RoomName), SafeText(w.RoomID), SafeText(w.ProjectRoot), SafeText(w.ExecutionOwner), SafeText(w.ActiveTurnID), SafeText(fmt.Sprintf("ALL AGENT ACTIONS RUN WITH %s'S FULL RUNTIME AUTHORITY; TRANSCRIPT ATTRIBUTION IS NOT TAMPER-PROOF.", w.ExecutionOwner))); err != nil {
					return fatal(err)
				}
				welcomed = true
				continue
			}
			if e.Kind == protocol.KindResponse || e.Kind == protocol.KindError {
				delete(pending, e.ID)
				if e.Method != "heartbeat" && e.Method != "ack" {
					fmt.Fprintf(out, "[%s] %s\n", SafeText(e.Method), SafeText(string(e.Body)))
				}
				continue
			}
			if e.Kind != protocol.KindEvent {
				return errors.New("unexpected frame kind")
			}
			if e.Method == "runtime-snapshot" {
				s, err := protocol.DecodeBody[protocol.RuntimeSnapshot](e.Body)
				if err != nil {
					return err
				}
				p.snapshot(s)
				if p.truncated {
					fmt.Fprintln(out, "[stream truncated; completed output follows]")
				}
				for _, v := range s.LiveItems {
					fmt.Fprintf(out, "[partial %s] %s\n", SafeText(v.ItemID), SafeText(v.Partial))
				}
				if !ready {
					ready = true
					kept := (*order)[:0]
					for _, id := range *order {
						if env, ok := pending[id]; ok {
							kept = append(kept, id)
							if err = send(env); err != nil {
								return err
							}
						}
					}
					*order = kept
				}
				continue
			}
			if e.Seq != nil {
				if *e.Seq <= cursor.LastAppliedSeq {
					continue
				}
				var d room.DurableEvent
				err := json.Unmarshal(e.Body, &d)
				if err != nil {
					return err
				}
				if uint64(d.Seq) != *e.Seq {
					return errors.New("durable sequence mismatch")
				}
				if err = p.durable(d, out); err != nil {
					return fatal(err)
				}
				cursor.LastAppliedSeq = *e.Seq
				if err = c.deps.Cursors.Save(*cursor); err != nil {
					return fatal(err)
				}
				ack, err := request(c.deps.Random, "ack", protocol.Ack{Seq: *e.Seq})
				if err != nil {
					return fatal(err)
				}
				if ready {
					if err = send(ack); err != nil {
						return err
					}
				}
				continue
			}
			if !ready {
				return errors.New("transient before snapshot")
			}
			t, err := protocol.DecodeBody[room.TransientEvent](e.Body)
			if err != nil {
				return err
			}
			if err = p.transient(t, out); err != nil {
				return fatal(err)
			}
		}
	}
}

// Stop marks a transport authorization error as terminal instead of retryable.
func Stop(err error) error { return fatal(err) }
