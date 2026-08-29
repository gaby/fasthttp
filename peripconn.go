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

// perIPConn and perIPTLSConn are allocated per connection rather than pooled:
// a wrapper returned to a pool can be handed to a new connection while a stale
// reference to it still exists, and a late Close() on that reference would then
// close an unrelated connection and unregister the wrong IP.
type perIPConn struct {
	net.Conn

	perIPConnCounter *perIPConnCounter

	ip   netip.Addr
	lock sync.Mutex
}

type perIPTLSConn struct {
	*tls.Conn

	perIPConnCounter *perIPConnCounter

	ip   netip.Addr
	lock sync.Mutex
}

func acquirePerIPConn(conn net.Conn, ip netip.Addr, counter *perIPConnCounter) net.Conn {
	if tlsConn, ok := conn.(*tls.Conn); ok {
		return &perIPTLSConn{
			perIPConnCounter: counter,
			Conn:             tlsConn,
			ip:               ip,
		}
	}

	return &perIPConn{
		perIPConnCounter: counter,
		Conn:             conn,
		ip:               ip,
	}
}

func (c *perIPConn) Close() error {
	c.lock.Lock()
	cc := c.Conn
	c.Conn = nil
	c.lock.Unlock()

	if cc == nil {
		return nil
	}

	err := cc.Close()
	c.perIPConnCounter.Unregister(c.ip)
	return err
}

func (c *perIPTLSConn) Close() error {
	c.lock.Lock()
	cc := c.Conn
	c.Conn = nil
	c.lock.Unlock()

	if cc == nil {
		return nil
	}

	err := cc.Close()
	c.perIPConnCounter.Unregister(c.ip)
	return err
}

// getConnIP returns the peer's IP address, keyed so that every distinct client
// gets its own counter. IPv4-mapped IPv6 addresses are unmapped so they share a
// bucket with the same IPv4 peer. Non-TCP connections share the zero Addr.
func getConnIP(c net.Conn) netip.Addr {
	addr, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return netip.Addr{}
	}
	ip, ok := netip.AddrFromSlice(addr.IP)
	if !ok {
		return netip.Addr{}
	}
	return ip.Unmap()
}
