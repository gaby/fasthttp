package fasthttp

import (
	"net"
	"testing"
)

var _ tlsConn = &perIPTLSConn{}

func TestPerIPConnCounter(t *testing.T) {
	t.Parallel()

	var cc perIPConnCounter

	for i := 1; i < 100; i++ {
		if n := cc.Register(123); n != i {
			t.Fatalf("Unexpected counter value=%d. Expected %d", n, i)
		}
	}

	n := cc.Register(456)
	if n != 1 {
		t.Fatalf("Unexpected counter value=%d. Expected 1", n)
	}

	cc.Unregister(123)
	if n := cc.Register(123); n != 99 {
		t.Fatalf("Unexpected counter value=%d. Expected 99", n)
	}

	for i := 1; i < 100; i++ {
		cc.Unregister(123)
	}
	cc.Unregister(456)

	n = cc.Register(123)
	if n != 1 {
		t.Fatalf("Unexpected counter value=%d. Expected 1", n)
	}
	cc.Unregister(123)

	if len(cc.m) != 0 {
		t.Fatalf("Unexpected counter map size=%d. Expected 0", len(cc.m))
	}
}

type closeRecordingConn struct {
	net.Conn

	closes int
}

func (c *closeRecordingConn) Close() error {
	c.closes++
	return nil
}

func (c *closeRecordingConn) Write(b []byte) (int, error) { return len(b), nil }

func TestPerIPConnCloseKeepsConnUsable(t *testing.T) {
	t.Parallel()

	var counter perIPConnCounter
	const ip = 123
	counter.Register(ip)

	inner := &closeRecordingConn{}
	c := acquirePerIPConn(inner, ip, &counter)

	if err := c.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A serving goroutine can still be writing through the wrapper when Close
	// runs; the promoted method must reach the real conn rather than a nil one.
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatalf("unexpected error writing after Close: %v", err)
	}

	// Close is idempotent and releases the peer's slot exactly once.
	if err := c.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.closes != 1 {
		t.Fatalf("inner conn closed %d times. Expecting 1", inner.closes)
	}
	if n := len(counter.m); n != 0 {
		t.Fatalf("unexpected counter map size=%d. Expecting 0", n)
	}
}
