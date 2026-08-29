# fasthttp codebase audit — critical issues

Audit of `github.com/valyala/fasthttp` at commit `0d9439b` (branch
`claude/codebase-audit-critical-fr5n3v`), Go 1.25.0, linux/amd64.

Every finding below was reproduced against the code in this tree. Suggested
fixes are starting points, not applied patches — several involve API or
behaviour decisions that belong to the maintainers.

## Method

| Check | Result |
| --- | --- |
| `go build` / `go vet ./...` | clean |
| `go test -race -count=1 ./...` | core package **ok**; only environment-caused failures (see below) |
| `golangci-lint run` (repo config) | 0 issues |
| `golangci-lint` + `gosec` (disabled in repo config) | 14 hits, all reviewed; no true positives |
| Go fuzzing, 7 targets × 110s (`FuzzHeaderScanner`, `FuzzRequestReadLimitBody`, `FuzzResponseReadLimitBody`, `FuzzURIParse`, `FuzzCookieParse`, `FuzzVisitHeaderParams`, `FuzzURIUpdateBytes`) | no crashers |
| Targeted protocol probes (smuggling, framing, limits, lifecycle) | 8 findings below |

The `fasthttpproxy` and `tcplisten` test failures in this container are **not**
bugs: `fasthttpproxy` reads `HTTPS_PROXY`/`no_proxy`, which this sandbox sets
(re-running with those unset passes), and `tcplisten` needs socket options the
sandbox kernel refuses.

### Areas checked and found sound

Header/trailer size limits (431 at ~1 MB), CRLF sanitisation on every write path
(no request or response splitting via header names, values, status message,
cookies or `Location`), `normalizePath` dot-segment removal, `ParseByteRange`,
bare-LF header termination (rejected end to end), zstd decompression-bomb
sizing, `RequestCtx` user-value clearing between pooled requests, and
idempotency gating on client retries.

---

## 1. CRITICAL — Data race between a timed-out handler and the connection loop

**Where:** `server.go:513` (handler goroutine), `server.go:2643-2648` (ctx swap),
`server.go:2703`, `streaming.go:25`

With `StreamRequestBody: true`, a handler wrapped in `TimeoutHandler` keeps its
`*RequestCtx` — and therefore the `*requestStream` holding the connection's
`*bufio.Reader` — after the timeout fires. The server loop replaces `ctx` and
immediately reuses that same reader to parse the next request. Two goroutines
then read one `bufio.Reader` with no synchronisation.

The abandoned ctx's `bodyStream` is never released: the cleanup at the bottom of
the loop (`if ctx.Request.bodyStream != nil { releaseRequestStream(rs) }`) runs
against the *new* ctx, whose stream is nil.

**Reproduced:** the Go race detector reports 8 races (`bufio.(*Reader).Buffered`
in `serveConnCounted` vs. `bufio.(*Reader).fill` under
`requestStream.Read` → `parseChunkSize`), and the server then parses raw chunk
payload as a request line:

```
error when reading request headers: cannot find http request method in
"10\r\nAAAAAAAAAAAAAAAA\r\n"
```

**Impact:** memory-unsafe concurrent mutation of the reader's buffer state, and
request smuggling — one connection's body bytes are interpreted as the next
request. Reachable by any client that can make a streamed request outlive the
handler timeout.

**Suggested fix:** on timeout, detach the connection from the abandoned ctx —
take ownership of `br`/the request stream before swapping ctx, and force
`connectionClose = true` so the loop stops reading that connection. Sharing a
reader between the two goroutines cannot be made safe by locking alone, because
the abandoned handler may block indefinitely.

## 2. CRITICAL — `MaxRequestBodySize` is entirely bypassed under `StreamRequestBody`

**Where:** `http.go` `ContinueReadBodyStream`, `streaming.go` `requestStream`

`requestStream` has no size-limit field and never enforces one.
`ContinueReadBodyStream` swallows the limit in both directions:

- chunked (`contentLength == -1`) returns `errChunkedStream` and becomes a
  stream before any limit is consulted;
- a known `Content-Length` over the limit produces `ErrBodyTooLarge`, which is
  caught and converted into a stream with `return nil`.

**Reproduced** (`MaxRequestBodySize: 1000`, handler calls `ctx.PostBody()`):

| Request | `StreamRequestBody: false` | `StreamRequestBody: true` |
| --- | --- | --- |
| chunked, 50 000 B | `400 Bad Request` | `200 OK`, handler read **50 000** |
| `Content-Length: 50000` | `400 Bad Request` | `200 OK`, handler read **50 000** |

**Impact:** unbounded memory DoS. `MaxRequestBodySize` is documented as "The
server rejects requests with bodies exceeding this limit", and the natural
handler idioms (`ctx.PostBody()`, `ctx.Request.Body()`, `ctx.PostForm()`) all
buffer the whole stream. Any fasthttp server with `StreamRequestBody` enabled
has no request-body cap at all.

**Suggested fix:** carry `maxBodySize` into `requestStream` and return
`ErrBodyTooLarge` once `totalBytesRead` exceeds it. If the intent is that
streaming turns the limit into a "call the handler early" threshold, then the
`MaxRequestBodySize` doc needs to say so and a separate hard ceiling is still
required.

## 3. HIGH — HEAD + `TimeoutHandler` sends a response body

**Where:** `server.go:2643-2652`

```go
timeoutResponse = ctx.timeoutResponse
if timeoutResponse != nil {
    ctx = s.acquireCtx(c)          // fresh ctx: Request is empty
    timeoutResponse.CopyTo(&ctx.Response)
}
if ctx.IsHead() {                  // tests the *new* empty request
    ctx.Response.SkipBody = true
}
```

`ctx.IsHead()` runs after the swap, against a blank request, so `SkipBody` is
never set for a timed-out HEAD.

**Reproduced** — `HEAD / HTTP/1.1` against `TimeoutHandler(...)`:

```
HTTP/1.1 408 Request Timeout
Content-Length: 10

timeout!!!
```

The same handler without the timeout correctly sends headers only. The
connection stays keep-alive (no `Connection: close`).

**Impact:** RFC 9110 §9.3.2 violation and response desync — a client or
intermediary reads the headers, expects no content, and parses `timeout!!!` as
the start of the next response. `ctx.Request.Header.IsHTTP11()` a few lines
later reads the blank request too, so HTTP/1.0 keep-alive responses also lose
their `Connection: keep-alive`.

**Suggested fix:** capture `isHead` (and the HTTP version) from the real request
before the ctx swap.

## 4. HIGH — `MaxConnsPerIP` collapses every IPv6 peer into one bucket

**Where:** `peripconn.go` `getUint32IP` / `getConnIP4`

`getConnIP4` returns `ipAddr.IP.To4()`, which is `nil` for a real IPv6 address;
`getUint32IP` then returns `0`. The counter map is keyed `uint32`, so it cannot
represent an IPv6 address at all.

**Reproduced:**

```
1.2.3.4        -> perIP bucket 16909060
1.2.3.5        -> perIP bucket 16909061
2001:db8::1    -> perIP bucket 0
2001:db8::2    -> perIP bucket 0
fe80::1        -> perIP bucket 0
::1            -> perIP bucket 0
```

**Impact:** on any IPv6-reachable server, `MaxConnsPerIP` gives no per-client
protection — and worse, it is a shared quota: one IPv6 client opening
`MaxConnsPerIP` connections locks out **every** other IPv6 client with
`429 Too Many Requests`. Bucket `0` is also shared with non-TCP connections.

**Suggested fix:** key the counter by the full address (e.g. `map[string]int`
over `netip.Addr.As16()`, or `netip.Addr` directly), and consider a configurable
IPv6 prefix width so a single /64 cannot trivially evade the limit.

## 5. HIGH — Request timeouts are not enforced across a custom `Dial`

**Where:** `client.go` `callDialFunc`, `dialAddr`, `dialHostHard`

```go
if dial != nil {
    return dial(addr)   // the timeout argument is dropped
}
```

`DoTimeout` documents unconditionally that "ErrTimeout is returned if the
response wasn't returned during the given timeout", but when `Dial` is set and
`DialTimeout` is not, the deadline is never applied to connection
establishment. `dialHostHard` acknowledges this in a comment, yet the exported
doc does not.

**Reproduced:** `Client.DoTimeout(req, resp, 2*time.Second)` with a custom
`Dial` that blocks was still parked in `dialHostHard` when the test was killed
at 25 s — over 12× the requested timeout, with no error returned.

**Impact:** unbounded goroutine accumulation in any client using a custom dialer
(proxy dialers, service-mesh dialers, test harnesses). A slow or black-holed
upstream pins caller goroutines indefinitely despite an explicit timeout.

**Suggested fix:** enforce the deadline around `callDialFunc` regardless of
which dial hook is set — e.g. run the dial in a goroutine bounded by the
remaining request deadline and close the connection if it lands late — or at
minimum document the gap on `DoTimeout` and `Client.Dial`.

## 6. MEDIUM — Chunk extensions are read without bound and are not counted against `MaxRequestBodySize`

**Where:** `http.go` `parseChunkSize`

The extension loop (`if inExt { continue }`) consumes bytes until CR with no
length cap, and those bytes never reach the body accounting.

**Reproduced:** a single chunk carrying a **16 MB** extension against a server
with `MaxRequestBodySize: 1000` is fully consumed and answered `200 OK`.

**Impact:** a client can push unlimited data through a server that has been
configured with a small body cap, and hold the connection open doing it. It is
CPU and bandwidth rather than heap (bytes are discarded as they are read), but
it defeats the configured limit. Go's `net/http` caps the whole chunk-size line
at 4 KB.

**Suggested fix:** cap the chunk-size line (size + extensions) at a few KB and
fail with `ErrBrokenChunk` beyond it.

## 7. MEDIUM — `Shutdown()` blocks indefinitely on a single stalled connection

**Where:** `server.go` `ShutdownWithContext`, `closeIdleConns`

`closeIdleConns` only closes connections whose stored idle timestamp is
non-zero. A connection parked mid-request has `idleConnTime == 0`, so it is
never closed, `s.open` never drains, and the loop spins forever.

**Reproduced:** one client that sends `GET / HTTP/1.1\r\nHost: a\r\n` and then
stalls keeps `Shutdown()` blocked past a 3 s bound (it does not return).

**Impact:** a single unauthenticated client prevents graceful shutdown
indefinitely on a server without `ReadTimeout`. `ShutdownWithContext` is the
escape hatch, but `Shutdown()` is the obvious API and its doc does not mention
the hazard.

**Suggested fix:** give `Shutdown()` a bounded grace period after which
in-flight connections are closed, or document that `ReadTimeout` is required for
`Shutdown()` to terminate.

## 8. MEDIUM — `Content-Length` + `Transfer-Encoding` is accepted rather than rejected

**Where:** `header.go` `RequestHeader.parseHeaders`

fasthttp resolves the conflict correctly for itself — `Transfer-Encoding` wins
in either header order and the `Content-Length` is dropped — but it serves the
request instead of failing it.

**Reproduced:** `Content-Length: 6` + `Transfer-Encoding: chunked` with body
`0\r\n\r\nGET /evil HTTP/1.1...` yields **two** `200 OK` responses; the smuggled
request is executed.

**Impact:** RFC 9112 §6.1 says such a message "ought to be handled as an error".
Behind any intermediary that prefers `Content-Length` (or that forwards the
`Content-Length` unchanged), this is a classic CL.TE desync. Go's `net/http`
rejects the combination outright.

**Suggested fix:** return an error and set `connectionClose` when both framing
headers are present on a request.

---

## Also noted (latent, not reproduced)

`perIPConn.Close()` / `perIPTLSConn.Close()` return the wrapper to a `sync.Pool`
after nulling `c.Conn`. The nil check makes an immediate second `Close()` a
no-op, but if the wrapper is re-acquired by a new connection first, a late
`Close()` on a stale reference would close an unrelated client's connection,
decrement the wrong per-IP counter, and double-`Put` the wrapper. `Shutdown`'s
`closeIdleConns` and the serving goroutine can both close the same wrapper; the
window is narrow because the listener is already closed during shutdown, and I
could not construct a reproduction. Worth a guard (an explicit `closed` flag, or
not pooling the wrapper) rather than relying on the timing.
