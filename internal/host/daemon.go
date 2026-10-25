package host

import (
	"context"
	"fmt"
	"time"
)

func RunDaemon(ctx context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, socket, err := connectDaemonBus(child)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("connect session D-Bus: %w", err)
	}
	defer func() { socket.Close(); conn.Close() }()
	r := NewRuntime(c)
	publisher := startBusPublisher(conn, r, child, func() { socket.Close() })
	defer func() { cancel(); <-publisher.done }()
	if err = ExportBus(conn, r, child); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if err = socket.SetDeadline(time.Time{}); err != nil {
		return err
	}
	return runDaemonRuntime(ctx, child, cancel, r, publisher)
}

func runDaemonRuntime(ctx, child context.Context, cancel context.CancelFunc, r *Runtime, publisher *busPublisher) error {
	done := make(chan error, 1)
	go func() { done <- r.Run(child) }()
	var err error
	select {
	case err = <-done:
		cancel()
		<-publisher.done
		if err != nil {
			return err
		}
		return publisher.err
	case <-ctx.Done():
		cancel()
		<-publisher.done
		<-done
		return nil
	case <-publisher.done:
		cancel()
		<-done
		err = publisher.err
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
}
