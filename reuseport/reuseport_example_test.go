package reuseport_test

import (
	"fmt"
	"log"

	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/reuseport"
)

func ExampleListen() {
	ln, err := reuseport.Listen("tcp4", "localhost:12345")
	if err != nil {
		log.Fatalf("error in reuseport listener: %v", err)
	}

	if err = fasthttp.Serve(ln, requestHandler); err != nil {
		log.Fatalf("error in fasthttp Server: %v", err)
	}
}

func ExampleListenPacket() {
	pc, err := reuseport.ListenPacket("udp4", "localhost:12346")
	if err != nil {
		log.Fatalf("error in reuseport packet conn: %v", err)
	}

	buf := make([]byte, 1024)
	n, addr, err := pc.ReadFrom(buf)
	if err != nil {
		pc.Close()
		log.Fatalf("error reading from packet conn: %v", err)
	}

	_, err = pc.WriteTo(buf[:n], addr)
	if err != nil {
		pc.Close()
		log.Fatalf("error writing to packet conn: %v", err)
	}

	pc.Close()
}

func requestHandler(ctx *fasthttp.RequestCtx) {
	fmt.Fprintf(ctx, "Hello, world!")
}
