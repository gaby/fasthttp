package fasthttp

import (
	"crypto/tls"
	"net"
	"net/netip"
	"sync"
)

type perIPConnCounter struct {
	m    map[netip.Addr]int
	lock sync.Mutex
}

func (cc *perIPConnCounter) Register(ip netip.Addr) int {
	cc.lock.Lock()
	if cc.m == nil {
		cc.m = make(map[netip.Addr]int)
	}
	n := cc.m[ip] + 1
	cc.m[ip] = n
	cc.lock.Unlock()
	return n
}

func (cc *perIPConnCounter) Unregister(ip netip.Addr) {
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
// wrappers. It never clears the embedded connection: Close can race a serving
// goroutine that is still writing through the wrapper, and nil-ing the conn
// would turn every promoted method into a nil dereference.
type perIPConnState struct {
	counter *perIPConnCounter

	ip     netip.Addr
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

// perIPConn and perIPTLSConn are allocated per connection rather than pooled:
// a wrapper returned to a pool can be handed to a new connection while a stale
// reference to it still exists, and a late Close() on that reference would then
// close an unrelated connection and unregister the wrong IP.
type perIPConn struct {
	net.Conn

	perIPConnState
}

type perIPTLSConn struct {
	*tls.Conn

	perIPConnState
}

func acquirePerIPConn(conn net.Conn, ip netip.Addr, counter *perIPConnCounter) net.Conn {
	// Assigned field by field: perIPConnState holds a mutex, so it must not be
	// copied by value into the wrapper.
	if tlsConn, ok := conn.(*tls.Conn); ok {
		c := &perIPTLSConn{Conn: tlsConn}
		c.counter = counter
		c.ip = ip
		return c
	}

	c := &perIPConn{Conn: conn}
	c.counter = counter
	c.ip = ip
	return c
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

// getConnIP returns the peer's IP address, keyed so that every distinct client
// gets its own counter. IPv4-mapped IPv6 addresses are unmapped so they share a
// bucket with the same IPv4 peer, and the zone is kept so link-local peers on
// different interfaces stay distinct. The zero Addr is returned for a peer with
// no usable IP; wrapPerIPConn leaves those connections uncounted.
func getConnIP(c net.Conn) netip.Addr {
	addr, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return netip.Addr{}
	}
	return addr.AddrPort().Addr().Unmap()
}
