package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"os/exec"
	"sync"
	"time"
)

type Runtime struct {
	config         Config
	mu             sync.Mutex
	engine         *Engine
	operationError string
	notify         func(State, []Event)
	lastPublished  string
	gate           func(Endpoint) (Binding, error)
	command        func(context.Context, Endpoint, Binding, string) *exec.Cmd
	reconnect      map[string]chan struct{}
}

func NewRuntime(c Config) *Runtime {
	r := &Runtime{config: c, engine: NewEngine(c), gate: GateEndpoint, reconnect: map[string]chan struct{}{}}
	r.command = func(ctx context.Context, e Endpoint, b Binding, op string) *exec.Cmd {
		return exec.CommandContext(ctx, "ssh", SSHArgs(c, e, b, op)...)
	}
	for _, e := range c.Endpoints {
		r.reconnect[e.Name] = make(chan struct{}, 1)
	}
	return r
}
func (r *Runtime) stateLocked() State {
	s := r.engine.State()
	if r.operationError != "" {
		if s.LastError != "" {
			s.LastError += "; "
		}
		s.LastError += r.operationError
	}
	return s
}
func (r *Runtime) publishLocked(events []Event) {
	s := r.stateLocked()
	b, _ := json.Marshal(s)
	v := string(b)
	if r.notify != nil && (v != r.lastPublished || len(events) > 0) {
		r.notify(s, events)
	}
	r.lastPublished = v
}
func (r *Runtime) apply(f func(*Engine) []Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := f(r.engine)
	r.publishLocked(events)
}
func (r *Runtime) Snapshot() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev := r.engine.Tick(time.Now())
	r.publishLocked(ev)
	return r.stateLocked()
}
func (r *Runtime) Run(ctx context.Context) error {
	if err := r.config.Validate(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, e := range r.config.Endpoints {
		wg.Add(1)
		go func(e Endpoint) { defer wg.Done(); r.worker(ctx, e) }(e)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case now := <-ticker.C:
			r.apply(func(e *Engine) []Event { return e.Tick(now) })
		}
	}
}
func (r *Runtime) worker(ctx context.Context, ep Endpoint) {
	var generation uint64
	backoff := r.config.RetryInterval.Duration
	for ctx.Err() == nil {
		generation++
		g := generation
		r.apply(func(e *Engine) []Event { return e.Begin(ep.Name, g, time.Now()) })
		binding, err := r.gate(ep)
		hadFrame := false
		restartNow := false
		if err == nil {
			child, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			var frameMu sync.Mutex
			go func() {
				done <- RunStream(child, r.command(child, ep, binding, "stream"), r.config.IdleTimeout.Duration, func(s protocol.Snapshot) {
					frameMu.Lock()
					hadFrame = true
					frameMu.Unlock()
					r.apply(func(e *Engine) []Event { return e.Frame(ep.Name, g, s, time.Now()) })
				})
			}()
			check := time.NewTicker(500 * time.Millisecond)
			active := true
			finished := false
			for active {
				select {
				case err = <-done:
					finished = true
					active = false
				case <-ctx.Done():
					err = ctx.Err()
					active = false
				case <-r.reconnect[ep.Name]:
					err = errors.New("reconnect requested")
					restartNow = true
					active = false
				case <-check.C:
					current, gateErr := r.gate(ep)
					if gateErr != nil {
						err = gateErr
						active = false
					} else if current != binding {
						err = errors.New("local interface binding changed")
						active = false
					}
				}
			}
			r.apply(func(e *Engine) []Event { return e.Fail(ep.Name, g, err.Error(), time.Now()) })
			cancel()
			if !finished {
				<-done
			}
			check.Stop()
			frameMu.Lock()
			if hadFrame {
				backoff = r.config.RetryInterval.Duration
			}
			frameMu.Unlock()
		} else {
			r.apply(func(e *Engine) []Event { return e.Fail(ep.Name, g, err.Error(), time.Now()) })
		}
		if ctx.Err() != nil {
			return
		}
		if restartNow {
			backoff = r.config.RetryInterval.Duration
			continue
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.reconnect[ep.Name]:
			timer.Stop()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}
func (r *Runtime) Reconnect() {
	r.mu.Lock()
	var events []Event
	now := time.Now()
	for name, l := range r.engine.links {
		events = append(events, r.engine.Fail(name, l.generation, "reconnect requested", now)...)
	}
	r.publishLocked(events)
	r.mu.Unlock()
	for _, ch := range r.reconnect {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (r *Runtime) RPC(ctx context.Context, req protocol.Request) error {
	if err := ValidateRequest(req); err != nil {
		return err
	}
	s := r.Snapshot()
	if !s.Connected {
		return errors.New("device disconnected: no fresh authenticated transport")
	}
	var ep Endpoint
	for _, v := range r.config.Endpoints {
		if v.Name == s.Transport {
			ep = v
			break
		}
	}
	b, err := r.gate(ep)
	if err == nil {
		bounded, cancel := context.WithTimeout(ctx, r.config.RPCTimeout.Duration)
		defer cancel()
		err = RunRPC(bounded, r.command(bounded, ep, b, "rpc"), req)
	}
	r.mu.Lock()
	r.operationError = ""
	if err != nil {
		r.operationError = fmt.Sprintf("%s RPC: %v", ep.Name, err)
	}
	r.publishLocked(nil)
	r.mu.Unlock()
	return err
}
