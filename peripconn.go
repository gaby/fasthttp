package fasthttp

import (
	"crypto/tls"
	"net"
	"sync"
)

type perIPConnCounter struct {
	m    map[uint32]int
	lock sync.Mutex
}

func (cc *perIPConnCounter) Register(ip uint32) int {
	cc.lock.Lock()
	if cc.m == nil {
		cc.m = make(map[uint32]int)
	}
	n := cc.m[ip] + 1
	cc.m[ip] = n
	cc.lock.Unlock()
	return n
}

func (cc *perIPConnCounter) Unregister(ip uint32) {
	cc.lock.Lock()
	defer cc.lock.Unlock()
	if cc.m == nil {
		// developer safeguard
		panic("BUG: perIPConnCounter.Register() wasn't called")
	}
	// Drop the entry, otherwise the map keeps a key per distinct client IP forever.
	if n := cc.m[ip] - 1; n > 0 {
		cc.m[ip] = n
	} else {
		delete(cc.m, ip)
	}
}

// perIPConnState is the per-IP bookkeeping shared by the plain and TLS
// wrappers. The embedded connection is never cleared: Close can race a serving
// goroutine that is still writing through the wrapper, and the promoted methods
// read the embedded field without the lock, so clearing it would both race and
// turn those calls into nil dereferences.
//
// The wrappers are also never pooled. A recycled wrapper can be handed to a new
// connection while a stale reference to it still exists, and a late Close on
// that reference would then close an unrelated connection and unregister the
// wrong IP.
type perIPConnState struct {
	counter *perIPConnCounter

	ip     uint32
	lock   sync.Mutex
	closed bool
}

// markClosed reports whether this call is the one that closed the wrapper, so
// the peer's slot is released exactly once.
func (s *perIPConnState) markClosed() bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.closed {
		return false
	}
	s.closed = true
	return true
}

type perIPConn struct {
	net.Conn

	perIPConnState
}

type perIPTLSConn struct {
	*tls.Conn

	perIPConnState
}

func acquirePerIPConn(conn net.Conn, ip uint32, counter *perIPConnCounter) net.Conn {
	if tlsConn, ok := conn.(*tls.Conn); ok {
		return &perIPTLSConn{
			Conn:           tlsConn,
			perIPConnState: perIPConnState{counter: counter, ip: ip},
		}
	}

	return &perIPConn{
		Conn:           conn,
		perIPConnState: perIPConnState{counter: counter, ip: ip},
	}
}

func (c *perIPConn) Close() error {
	if !c.markClosed() {
		return nil
	}
	err := c.Conn.Close()
	c.counter.Unregister(c.ip)
	return err
}

func (c *perIPTLSConn) Close() error {
	if !c.markClosed() {
		return nil
	}
	err := c.Conn.Close()
	c.counter.Unregister(c.ip)
	return err
}

func getUint32IP(c net.Conn) uint32 {
	ip := getConnIP4(c)

	if len(ip) != 4 {
		return 0
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func getConnIP4(c net.Conn) net.IP {
	addr := c.RemoteAddr()
	ipAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return net.IPv4zero
	}
	return ipAddr.IP.To4()
}
