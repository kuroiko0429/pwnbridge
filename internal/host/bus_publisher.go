package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

const busNotificationCapacity = 64
const busPublicationTimeout = 2 * time.Second

var errBusNotificationOverflow = errors.New("D-Bus notification queue overflow")

type busNotification struct {
	state  State
	events []Event
}

type busPublisher struct {
	queue   chan busNotification
	failure chan error
	done    chan struct{}
	// Written before done closes; readers must wait for done.
	err error
}

// startBusPublisher installs a nonblocking callback. Runtime.mu serializes
// enqueue order; exactly one publisher sends each PropertiesChanged/Event batch.
// interrupt MUST close the underlying socket without going through dbus.Close:
// godbus v5.2.4 Close takes outputHandler.closed.lck before transport.Close,
// while a blocked SendMessage holds that lock for reading.
func startBusPublisher(conn *dbus.Conn, r *Runtime, ctx context.Context, interrupt func()) *busPublisher {
	p := &busPublisher{
		queue:   make(chan busNotification, busNotificationCapacity),
		failure: make(chan error, 1),
		done:    make(chan struct{}),
	}
	r.mu.Lock()
	r.notify = func(s State, events []Event) {
		select {
		case <-p.done:
			return
		default:
		}
		select {
		case p.queue <- busNotification{s, append([]Event(nil), events...)}:
		default:
			// Never coalesce/drop connection events and carry on. The supervisor
			// tears down the bus and returns an error so systemd can restart us.
			select {
			case p.failure <- errBusNotificationOverflow:
			default:
			}
		}
	}
	r.lastPublished = ""
	r.mu.Unlock()
	go p.run(conn, ctx, interrupt)
	return p
}

func emitBusNotification(conn *dbus.Conn, n busNotification) error {
	if err := conn.Emit(BusPath, propertiesInterface+".PropertiesChanged", BusInterface, Properties(n.state), []string{}); err != nil {
		return fmt.Errorf("D-Bus PropertiesChanged: %w", err)
	}
	for _, event := range n.events {
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if err = conn.Emit(BusPath, BusInterface+".Event", string(raw)); err != nil {
			return fmt.Errorf("D-Bus Event: %w", err)
		}
	}
	return nil
}

func (p *busPublisher) run(conn *dbus.Conn, ctx context.Context, interrupt func()) {
	var sending <-chan error
	defer func() {
		interrupt()
		// Raw socket close releases blocked Emit (and any blocked RPC replies)
		// before godbus Close tries to acquire the output-handler write lock.
		conn.Close()
		if sending != nil {
			<-sending
		}
		close(p.done)
	}()
	for {
		// Prefer shutdown over draining a backlog.
		if ctx.Err() != nil {
			return
		}
		// Overflow is terminal, not a request to drain the remaining backlog.
		select {
		case err := <-p.failure:
			p.err = err
			return
		default:
		}
		select {
		case <-ctx.Done():
			return
		case err := <-p.failure:
			p.err = err
			return
		case <-conn.Context().Done():
			p.err = fmt.Errorf("session D-Bus disconnected: %w", conn.Context().Err())
			return
		case n := <-p.queue:
			result := make(chan error, 1)
			sending = result
			go func() { result <- emitBusNotification(conn, n) }()
			timer := time.NewTimer(busPublicationTimeout)
			select {
			case <-ctx.Done():
			case err := <-p.failure:
				p.err = err
			case <-conn.Context().Done():
				p.err = fmt.Errorf("session D-Bus disconnected: %w", conn.Context().Err())
			case <-timer.C:
				p.err = errors.New("D-Bus notification publication timed out")
			case err := <-result:
				sending = nil
				timer.Stop()
				if err != nil {
					p.err = err
					return
				}
				continue
			}
			timer.Stop()
			return
		}
	}
}
