package fasthttp

import (
	"net"
	"net/netip"
	"testing"
)

var _ tlsConn = &perIPTLSConn{}

func TestPerIPConnCounter(t *testing.T) {
	t.Parallel()

	var cc perIPConnCounter

	ip1 := netip.MustParseAddr("1.2.3.4")
	ip2 := netip.MustParseAddr("2001:db8::1")

	for i := 1; i < 100; i++ {
		if n := cc.Register(ip1); n != i {
			t.Fatalf("Unexpected counter value=%d. Expected %d", n, i)
		}
	}

	n := cc.Register(ip2)
	if n != 1 {
		t.Fatalf("Unexpected counter value=%d. Expected 1", n)
	}

	cc.Unregister(ip1)
	if n := cc.Register(ip1); n != 99 {
		t.Fatalf("Unexpected counter value=%d. Expected 99", n)
	}

	for i := 1; i < 100; i++ {
		cc.Unregister(ip1)
	}
	cc.Unregister(ip2)

	n = cc.Register(ip1)
	if n != 1 {
		t.Fatalf("Unexpected counter value=%d. Expected 1", n)
	}
	cc.Unregister(ip1)

	if len(cc.m) != 0 {
		t.Fatalf("Unexpected counter map size=%d. Expected 0", len(cc.m))
	}
}

func TestPerIPConnCounterIPv6(t *testing.T) {
	t.Parallel()

	var cc perIPConnCounter

	// Distinct IPv6 peers must not share a bucket, otherwise one client
	// exhausts MaxConnsPerIP for every other IPv6 client.
	a := netip.MustParseAddr("2001:db8::1")
	b := netip.MustParseAddr("2001:db8::2")
	if n := cc.Register(a); n != 1 {
		t.Fatalf("Unexpected counter value=%d. Expected 1", n)
	}
	if n := cc.Register(b); n != 1 {
		t.Fatalf("Unexpected counter value=%d. Expected 1", n)
	}
	if n := cc.Register(a); n != 2 {
		t.Fatalf("Unexpected counter value=%d. Expected 2", n)
	}
	cc.Unregister(a)
	cc.Unregister(a)
	cc.Unregister(b)
	if len(cc.m) != 0 {
		t.Fatalf("Unexpected counter map size=%d. Expected 0", len(cc.m))
	}
}

type perIPTestConn struct {
	net.Conn

	addr net.Addr
}

func (c *perIPTestConn) RemoteAddr() net.Addr { return c.addr }

func TestGetConnIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.ParseIP("1.2.3.4")}, "1.2.3.4"},
		{&net.TCPAddr{IP: net.ParseIP("::ffff:1.2.3.4")}, "1.2.3.4"},
		{&net.TCPAddr{IP: net.ParseIP("2001:db8::1")}, "2001:db8::1"},
		{&net.TCPAddr{IP: net.ParseIP("fe80::1")}, "fe80::1"},
		{&net.UnixAddr{Name: "/tmp/x"}, "invalid IP"},
	}

	for _, test := range tests {
		got := getConnIP(&perIPTestConn{addr: test.addr})
		if got.String() != test.want {
			t.Fatalf("getConnIP(%v) = %v. Expecting %v", test.addr, got, test.want)
		}
	}
}
