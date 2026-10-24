package ffmpeg

import (
	"context"
	"fmt"
	"net"
	"sync"
)

// Loopback is a TCP listener on 127.0.0.1 through which ffmpeg reads or
// writes one extra stream (tcp://127.0.0.1:port), since extra pipe file
// descriptors do not work on Windows (research R4). It accepts one
// connection.
type Loopback struct {
	ln   net.Listener
	once sync.Once
}

// Listen opens a loopback listener on a free port.
func Listen() (*Loopback, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("loopback listener: %w", err)
	}
	return &Loopback{ln: ln}, nil
}

// URL is the address to give ffmpeg.
func (l *Loopback) URL() string {
	return "tcp://" + l.ln.Addr().String()
}

// Accept waits for ffmpeg to connect; it fails when ctx ends or the
// listener is closed.
func (l *Loopback) Accept(ctx context.Context) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := l.ln.Accept()
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		l.Close()
		if r := <-ch; r.c != nil {
			r.c.Close()
		}
		return nil, ctx.Err()
	}
}

// Close closes the listener (not an accepted connection).
func (l *Loopback) Close() error {
	var err error
	l.once.Do(func() { err = l.ln.Close() })
	return err
}
