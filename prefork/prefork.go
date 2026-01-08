// Package prefork provides a way to prefork a fasthttp server.
package prefork

import (
	"errors"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"

	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/reuseport"
)

const (
	preforkChildEnvVariable = "FASTHTTP_PREFORK_CHILD"
	defaultNetwork          = "tcp4"
)

var (
	defaultLogger = Logger(log.New(os.Stderr, "", log.LstdFlags))
	// ErrOverRecovery is returned when the times of starting over child prefork processes exceed
	// the threshold.
	ErrOverRecovery = errors.New("exceeding the value of RecoverThreshold")

	// ErrOnlyReuseportOnWindows is returned when Reuseport is false.
	ErrOnlyReuseportOnWindows = errors.New("windows only supports Reuseport = true")
)

// Logger is used for logging formatted messages.
type Logger interface {
	// Printf must have the same semantics as log.Printf.
	Printf(format string, args ...any)
}

// Prefork implements fasthttp server prefork.
//
// Preforks master process (with all cores) between several child processes
// increases performance significantly, because Go doesn't have to share
// and manage memory between cores.
//
// WARNING: using prefork prevents the use of any global state!
// Things like in-memory caches won't work.
type Prefork struct {
	// By default standard logger from log package is used.
	Logger Logger

	ln net.Listener
	pc net.PacketConn

	ServeFunc         func(ln net.Listener) error
	ServeTLSFunc      func(ln net.Listener, certFile, keyFile string) error
	ServeTLSEmbedFunc func(ln net.Listener, certData, keyData []byte) error
	ServePacketFunc   func(pc net.PacketConn) error

	// The network must be "tcp", "tcp4" or "tcp6" for TCP,
	// or "udp", "udp4" or "udp6" for UDP.
	//
	// By default is "tcp4"
	Network string

	files []*os.File

	// Child prefork processes may exit with failure and will be started over until the times reach
	// the value of RecoverThreshold, then it will return and terminate the server.
	RecoverThreshold int

	// Flag to use a listener with reuseport, if not a file Listener will be used
	// See: https://www.nginx.com/blog/socket-sharding-nginx-release-1-9-1/
	//
	// It's disabled by default
	Reuseport bool
}

// IsChild checks if the current thread/process is a child.
func IsChild() bool {
	return os.Getenv(preforkChildEnvVariable) == "1"
}

// New wraps the fasthttp server to run with preforked processes.
func New(s *fasthttp.Server) *Prefork {
	return &Prefork{
		Network:           defaultNetwork,
		RecoverThreshold:  runtime.GOMAXPROCS(0) / 2,
		Logger:            s.Logger,
		ServeFunc:         s.Serve,
		ServeTLSFunc:      s.ServeTLS,
		ServeTLSEmbedFunc: s.ServeTLSEmbed,
	}
}

func (p *Prefork) logger() Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return defaultLogger
}

func (p *Prefork) listen(addr string) (net.Listener, error) {
	runtime.GOMAXPROCS(1)

	if p.Network == "" {
		p.Network = defaultNetwork
	}

	if p.Reuseport {
		return reuseport.Listen(p.Network, addr)
	}

	return net.FileListener(os.NewFile(3, ""))
}

func (p *Prefork) listenPacket(addr string) (net.PacketConn, error) {
	runtime.GOMAXPROCS(1)

	if p.Network == "" {
		p.Network = "udp4"
	}

	if p.Reuseport {
		return reuseport.ListenPacket(p.Network, addr)
	}

	return net.FilePacketConn(os.NewFile(3, ""))
}

func (p *Prefork) setTCPListenerFiles(addr string) error {
	if p.Network == "" {
		p.Network = defaultNetwork
	}

	tcpAddr, err := net.ResolveTCPAddr(p.Network, addr)
	if err != nil {
		return err
	}

	tcplistener, err := net.ListenTCP(p.Network, tcpAddr)
	if err != nil {
		return err
	}

	p.ln = tcplistener

	fl, err := tcplistener.File()
	if err != nil {
		return err
	}

	p.files = []*os.File{fl}

	return nil
}

func (p *Prefork) setUDPPacketConnFiles(addr string) error {
	if p.Network == "" {
		p.Network = "udp4"
	}

	udpAddr, err := net.ResolveUDPAddr(p.Network, addr)
	if err != nil {
		return err
	}

	udpconn, err := net.ListenUDP(p.Network, udpAddr)
	if err != nil {
		return err
	}

	p.pc = udpconn

	fl, err := udpconn.File()
	if err != nil {
		return err
	}

	p.files = []*os.File{fl}

	return nil
}

func (p *Prefork) doCommand() (*exec.Cmd, error) {
	// #nosec G204
	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), preforkChildEnvVariable+"=1")
	cmd.ExtraFiles = p.files
	err := cmd.Start()
	return cmd, err
}

// manageChildProcesses handles spawning and monitoring of child prefork processes.
func (p *Prefork) manageChildProcesses() error {
	type procSig struct {
		err error
		pid int
	}

	goMaxProcs := runtime.GOMAXPROCS(0)
	sigCh := make(chan procSig, goMaxProcs)
	childProcs := make(map[int]*exec.Cmd)

	defer func() {
		for _, proc := range childProcs {
			_ = proc.Process.Kill()
		}
	}()

	for range goMaxProcs {
		cmd, err := p.doCommand()
		if err != nil {
			p.logger().Printf("failed to start a child prefork process, error: %v\n", err)
			return err
		}

		childProcs[cmd.Process.Pid] = cmd
		go func() {
			sigCh <- procSig{pid: cmd.Process.Pid, err: cmd.Wait()}
		}()
	}

	var exitedProcs int
	for sig := range sigCh {
		delete(childProcs, sig.pid)

		p.logger().Printf("one of the child prefork processes exited with "+
			"error: %v", sig.err)

		exitedProcs++
		if exitedProcs > p.RecoverThreshold {
			p.logger().Printf("child prefork processes exit too many times, "+
				"which exceeds the value of RecoverThreshold(%d), "+
				"exiting the master process.\n", exitedProcs)
			return ErrOverRecovery
		}

		cmd, err := p.doCommand()
		if err != nil {
			return err
		}
		childProcs[cmd.Process.Pid] = cmd
		go func() {
			sigCh <- procSig{pid: cmd.Process.Pid, err: cmd.Wait()}
		}()
	}

	return nil
}

func (p *Prefork) prefork(addr string) (err error) {
	if !p.Reuseport {
		if runtime.GOOS == "windows" {
			return ErrOnlyReuseportOnWindows
		}

		if err = p.setTCPListenerFiles(addr); err != nil {
			return err
		}

		// defer for closing the net.Listener opened by setTCPListenerFiles.
		defer func() {
			e := p.ln.Close()
			if err == nil {
				err = e
			}
		}()
	}

	return p.manageChildProcesses()
}

func (p *Prefork) preforkPacket(addr string) (err error) {
	if !p.Reuseport {
		if runtime.GOOS == "windows" {
			return ErrOnlyReuseportOnWindows
		}

		if err = p.setUDPPacketConnFiles(addr); err != nil {
			return err
		}

		// defer for closing the net.PacketConn opened by setUDPPacketConnFiles.
		defer func() {
			e := p.pc.Close()
			if err == nil {
				err = e
			}
		}()
	}

	return p.manageChildProcesses()
}

// ListenAndServe serves HTTP requests from the given TCP addr.
func (p *Prefork) ListenAndServe(addr string) error {
	if IsChild() {
		ln, err := p.listen(addr)
		if err != nil {
			return err
		}

		p.ln = ln

		return p.ServeFunc(ln)
	}

	return p.prefork(addr)
}

// ListenAndServeTLS serves HTTPS requests from the given TCP addr.
//
// certFile and keyFile are paths to TLS certificate and key files.
func (p *Prefork) ListenAndServeTLS(addr, certKey, certFile string) error {
	if IsChild() {
		ln, err := p.listen(addr)
		if err != nil {
			return err
		}

		p.ln = ln

		return p.ServeTLSFunc(ln, certFile, certKey)
	}

	return p.prefork(addr)
}

// ListenAndServeTLSEmbed serves HTTPS requests from the given TCP addr.
//
// certData and keyData must contain valid TLS certificate and key data.
func (p *Prefork) ListenAndServeTLSEmbed(addr string, certData, keyData []byte) error {
	if IsChild() {
		ln, err := p.listen(addr)
		if err != nil {
			return err
		}

		p.ln = ln

		return p.ServeTLSEmbedFunc(ln, certData, keyData)
	}

	return p.prefork(addr)
}

// ListenAndServePacket serves UDP requests from the given UDP addr.
func (p *Prefork) ListenAndServePacket(addr string) error {
	if IsChild() {
		pc, err := p.listenPacket(addr)
		if err != nil {
			return err
		}

		p.pc = pc

		return p.ServePacketFunc(pc)
	}

	return p.preforkPacket(addr)
}
