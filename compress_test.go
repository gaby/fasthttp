package fasthttp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var compressTestcases = func() []string {
	a := make([]string, 0, 4)
	a = append(a,
		"",
		"foobar",
		"выфаодлодл одлфываыв sd2 k34",
	)
	bigS := createFixedBody(1e4)
	a = append(a, string(bigS))
	return a
}()

func TestGzipBytesSerial(t *testing.T) {
	t.Parallel()

	if err := testGzipBytes(); err != nil {
		t.Fatal(err)
	}
}

func TestGzipBytesConcurrent(t *testing.T) {
	t.Parallel()

	if err := testConcurrent(10, testGzipBytes); err != nil {
		t.Fatal(err)
	}
}

func TestDeflateBytesSerial(t *testing.T) {
	t.Parallel()

	if err := testDeflateBytes(); err != nil {
		t.Fatal(err)
	}
}

func TestDeflateBytesConcurrent(t *testing.T) {
	t.Parallel()

	if err := testConcurrent(10, testDeflateBytes); err != nil {
		t.Fatal(err)
	}
}

func testGzipBytes() error {
	for _, s := range compressTestcases {
		if err := testGzipBytesSingleCase(s); err != nil {
			return err
		}
	}
	return nil
}

func testDeflateBytes() error {
	for _, s := range compressTestcases {
		if err := testDeflateBytesSingleCase(s); err != nil {
			return err
		}
	}
	return nil
}

func testGzipBytesSingleCase(s string) error {
	prefix := []byte("foobar")
	gzippedS := AppendGzipBytes(prefix, []byte(s))
	if !bytes.Equal(gzippedS[:len(prefix)], prefix) {
		return fmt.Errorf("unexpected prefix when compressing %q: %q. Expecting %q", s, gzippedS[:len(prefix)], prefix)
	}

	gunzippedS, err := AppendGunzipBytes(prefix, gzippedS[len(prefix):])
	if err != nil {
		return fmt.Errorf("unexpected error when uncompressing %q: %w", s, err)
	}
	if !bytes.Equal(gunzippedS[:len(prefix)], prefix) {
		return fmt.Errorf("unexpected prefix when uncompressing %q: %q. Expecting %q", s, gunzippedS[:len(prefix)], prefix)
	}
	gunzippedS = gunzippedS[len(prefix):]
	if string(gunzippedS) != s {
		return fmt.Errorf("unexpected uncompressed string %q. Expecting %q", gunzippedS, s)
	}
	return nil
}

func testDeflateBytesSingleCase(s string) error {
	prefix := []byte("foobar")
	deflatedS := AppendDeflateBytes(prefix, []byte(s))
	if !bytes.Equal(deflatedS[:len(prefix)], prefix) {
		return fmt.Errorf("unexpected prefix when compressing %q: %q. Expecting %q", s, deflatedS[:len(prefix)], prefix)
	}

	inflatedS, err := AppendInflateBytes(prefix, deflatedS[len(prefix):])
	if err != nil {
		return fmt.Errorf("unexpected error when uncompressing %q: %w", s, err)
	}
	if !bytes.Equal(inflatedS[:len(prefix)], prefix) {
		return fmt.Errorf("unexpected prefix when uncompressing %q: %q. Expecting %q", s, inflatedS[:len(prefix)], prefix)
	}
	inflatedS = inflatedS[len(prefix):]
	if string(inflatedS) != s {
		return fmt.Errorf("unexpected uncompressed string %q. Expecting %q", inflatedS, s)
	}
	return nil
}

func TestGzipCompressSerial(t *testing.T) {
	t.Parallel()

	if err := testGzipCompress(); err != nil {
		t.Fatal(err)
	}
}

func TestGzipCompressConcurrent(t *testing.T) {
	t.Parallel()

	if err := testConcurrent(10, testGzipCompress); err != nil {
		t.Fatal(err)
	}
}

func TestFlateCompressSerial(t *testing.T) {
	t.Parallel()

	if err := testFlateCompress(); err != nil {
		t.Fatal(err)
	}
}

func TestFlateCompressConcurrent(t *testing.T) {
	t.Parallel()

	if err := testConcurrent(10, testFlateCompress); err != nil {
		t.Fatal(err)
	}
}

func testGzipCompress() error {
	for _, s := range compressTestcases {
		if err := testGzipCompressSingleCase(s); err != nil {
			return err
		}
	}
	return nil
}

func testFlateCompress() error {
	for _, s := range compressTestcases {
		if err := testFlateCompressSingleCase(s); err != nil {
			return err
		}
	}
	return nil
}

func testGzipCompressSingleCase(s string) error {
	var buf bytes.Buffer
	zw := acquireStacklessGzipWriter(&buf, CompressDefaultCompression)
	if _, err := zw.Write([]byte(s)); err != nil {
		return fmt.Errorf("unexpected error: %w. s=%q", err, s)
	}
	_ = releaseStacklessGzipWriter(zw, CompressDefaultCompression)

	zr, err := acquireGzipReader(&buf)
	if err != nil {
		return fmt.Errorf("unexpected error: %w. s=%q", err, s)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		return fmt.Errorf("unexpected error: %w. s=%q", err, s)
	}
	if string(body) != s {
		return fmt.Errorf("unexpected string after decompression: %q. Expecting %q", body, s)
	}
	releaseGzipReader(zr)
	return nil
}

func testFlateCompressSingleCase(s string) error {
	var buf bytes.Buffer
	zw := acquireStacklessDeflateWriter(&buf, CompressDefaultCompression)
	if _, err := zw.Write([]byte(s)); err != nil {
		return fmt.Errorf("unexpected error: %w. s=%q", err, s)
	}
	_ = releaseStacklessDeflateWriter(zw, CompressDefaultCompression)

	zr, err := acquireFlateReader(&buf)
	if err != nil {
		return fmt.Errorf("unexpected error: %w. s=%q", err, s)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		return fmt.Errorf("unexpected error: %w. s=%q", err, s)
	}
	if string(body) != s {
		return fmt.Errorf("unexpected string after decompression: %q. Expecting %q", body, s)
	}
	releaseFlateReader(zr)
	return nil
}

func testConcurrent(concurrency int, f func() error) error {
	ch := make(chan error, concurrency)
	for i := range concurrency {
		go func(idx int) {
			err := f()
			if err != nil {
				ch <- fmt.Errorf("error in goroutine %d: %w", idx, err)
				return
			}
			ch <- nil
		}(i)
	}
	for range concurrency {
		select {
		case err := <-ch:
			if err != nil {
				return err
			}
		case <-time.After(testTimeout(time.Second)):
			return errors.New("timeout")
		}
	}
	return nil
}

var withLimitCodecs = []struct {
	name      string
	compress  func(dst, src []byte) []byte
	withLimit func(dst, src []byte, maxBodySize int) ([]byte, error)
}{
	{"gzip", AppendGzipBytes, AppendGunzipBytesWithLimit},
	{"deflate", AppendDeflateBytes, AppendInflateBytesWithLimit},
	{"brotli", AppendBrotliBytes, AppendUnbrotliBytesWithLimit},
	{"zstd", AppendZstdBytes, AppendUnzstdBytesWithLimit},
}

func TestAppendUncompressBytesWithLimit(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("hello, world! "), 1000)
	prefix := []byte("prefix:")

	for _, codec := range withLimitCodecs {
		t.Run(codec.name, func(t *testing.T) {
			t.Parallel()

			compressed := codec.compress(nil, data)

			// Without a limit, or with one that isn't reached, the data is
			// appended to dst whole.
			for _, maxBodySize := range []int{0, -1, len(data), len(data) + 1, 10 * len(data), math.MaxInt - 1, math.MaxInt} {
				got, err := codec.withLimit(nil, compressed, maxBodySize)
				if err != nil {
					t.Fatalf("unexpected error with the limit %d: %v", maxBodySize, err)
				}
				if !bytes.Equal(got, data) {
					t.Fatalf("unexpected data with the limit %d: %d bytes. Expecting %d bytes", maxBodySize, len(got), len(data))
				}

				got, err = codec.withLimit(append([]byte(nil), prefix...), compressed, maxBodySize)
				if err != nil {
					t.Fatalf("unexpected error with the limit %d and a prefix: %v", maxBodySize, err)
				}
				if !bytes.Equal(got, append(append([]byte(nil), prefix...), data...)) {
					t.Fatalf("unexpected data with the limit %d and a prefix: %d bytes", maxBodySize, len(got))
				}
			}

			// One byte over the limit is too much, and so is far over it. The
			// prefix of dst doesn't count towards the limit and stays in the
			// result, and no more than a byte over the limit is decoded.
			for _, maxBodySize := range []int{len(data) - 1, len(data) / 2, 1} {
				got, err := codec.withLimit(append([]byte(nil), prefix...), compressed, maxBodySize)
				if !errors.Is(err, ErrBodyTooLarge) {
					t.Fatalf("unexpected error %v with the limit %d. Expecting %v", err, maxBodySize, ErrBodyTooLarge)
				}
				if !bytes.HasPrefix(got, prefix) {
					t.Fatalf("the result lost dst with the limit %d: %q", maxBodySize, got[:min(len(got), 20)])
				}
				if extra := len(got) - len(prefix); extra > maxBodySize+1 {
					t.Fatalf("%d bytes were appended with the limit %d", extra, maxBodySize)
				}
			}

			// Data that isn't compressed fails the way it always did.
			if _, err := codec.withLimit(nil, []byte("not compressed data"), len(data)); err == nil || errors.Is(err, ErrBodyTooLarge) {
				t.Fatalf("unexpected error %v for data that isn't %s. Expecting a decoding error", err, codec.name)
			}
		})
	}
}

// A small input can expand to something huge. The limit has to be what stops
// it, not what is left of the memory.
func TestAppendUncompressBytesWithLimitBomb(t *testing.T) {
	t.Parallel()

	data := make([]byte, 8<<20)
	const maxBodySize = 64 << 10

	for _, codec := range withLimitCodecs {
		t.Run(codec.name, func(t *testing.T) {
			t.Parallel()

			compressed := codec.compress(nil, data)
			if len(compressed) > len(data)/100 {
				t.Fatalf("the test data should compress well: %d bytes", len(compressed))
			}
			got, err := codec.withLimit(nil, compressed, maxBodySize)
			if !errors.Is(err, ErrBodyTooLarge) {
				t.Fatalf("unexpected error %v. Expecting %v", err, ErrBodyTooLarge)
			}
			if len(got) > maxBodySize+1 {
				t.Fatalf("%d bytes were decoded with the limit %d", len(got), maxBodySize)
			}
		})
	}
}

func TestCompressSlots(t *testing.T) {
	t.Parallel()

	s := compressSlots{limit: 2}
	s.once.Do(func() {})
	if !s.tryAcquire() || !s.tryAcquire() {
		t.Fatal("cannot take a free slot")
	}
	if s.tryAcquire() {
		t.Fatal("took a slot beyond the limit")
	}
	s.release()
	if !s.tryAcquire() {
		t.Fatal("cannot take a released slot")
	}
	if n := s.inUse.Load(); n != 2 {
		t.Fatalf("%d slots in use, want 2", n)
	}
}

var takeInlineCompressSlotsMu sync.Mutex

// takeInlineCompressSlots holds every inline compression slot until the
// returned function is called, so that compressions in the meantime go to
// the stackless workers. It waits for slots other tests hold.
func takeInlineCompressSlots() (release func()) {
	takeInlineCompressSlotsMu.Lock()
	inlineCompressSlots.init()
	held := int64(0)
	for held < inlineCompressSlots.limit {
		if inlineCompressSlots.tryAcquire() {
			held++
		} else {
			runtime.Gosched()
		}
	}
	return func() {
		for range held {
			inlineCompressSlots.release()
		}
		takeInlineCompressSlotsMu.Unlock()
	}
}

func TestCompressInlineMatchesStackless(t *testing.T) {
	t.Parallel()

	for _, level := range []int{
		CompressHuffmanOnly, CompressNoCompression, CompressBestSpeed, 2, 3, 4, 5,
		CompressDefaultCompression, 7, 8, CompressBestCompression,
	} {
		for _, s := range compressTestcases {
			src := []byte(s)

			var inlineGz, workerGz byteSliceWriter
			writeGzipNow(&inlineGz, src, level)
			stacklessWriteGzip(&compressCtx{w: &workerGz, p: src, level: level})
			if !bytes.Equal(inlineGz.b, workerGz.b) {
				t.Fatalf("level %d: gzip inline and on a worker differ for %d bytes", level, len(src))
			}

			var inlineDf, workerDf byteSliceWriter
			writeDeflateNow(&inlineDf, src, level)
			stacklessWriteDeflate(&compressCtx{w: &workerDf, p: src, level: level})
			if !bytes.Equal(inlineDf.b, workerDf.b) {
				t.Fatalf("level %d: deflate inline and on a worker differ for %d bytes", level, len(src))
			}
		}
	}
	for level := CompressBrotliNoCompression; level <= CompressBrotliBestCompression; level++ {
		for _, s := range compressTestcases {
			src := []byte(s)

			var inlineBr, workerBr byteSliceWriter
			writeBrotliNow(&inlineBr, src, level)
			stacklessWriteBrotli(&compressCtx{w: &workerBr, p: src, level: level})
			if !bytes.Equal(inlineBr.b, workerBr.b) {
				t.Fatalf("level %d: brotli inline and on a worker differ for %d bytes", level, len(src))
			}
		}
	}
}

func TestCompressWithoutInlineSlot(t *testing.T) {
	t.Parallel()

	src := createFixedBody(1e4)
	var wantGz, wantDf, wantBr byteSliceWriter
	writeGzipNow(&wantGz, src, CompressDefaultCompression)
	writeDeflateNow(&wantDf, src, CompressDefaultCompression)
	writeBrotliNow(&wantBr, src, CompressBrotliDefaultCompression)

	release := takeInlineCompressSlots()
	defer release()

	// With every slot taken the compressions queue for the stackless
	// workers, and the writers that may block take their stackless writer.
	if gz := AppendGzipBytesLevel(nil, src, CompressDefaultCompression); !bytes.Equal(gz, wantGz.b) {
		t.Fatal("unexpected gzip output from a stackless worker")
	}
	if df := AppendDeflateBytesLevel(nil, src, CompressDefaultCompression); !bytes.Equal(df, wantDf.b) {
		t.Fatal("unexpected deflate output from a stackless worker")
	}
	if br := AppendBrotliBytesLevel(nil, src, CompressBrotliDefaultCompression); !bytes.Equal(br, wantBr.b) {
		t.Fatal("unexpected brotli output from a stackless worker")
	}
	var buf bytes.Buffer
	if _, err := WriteGzipLevel(&buf, src, CompressDefaultCompression); err != nil || !bytes.Equal(buf.Bytes(), wantGz.b) {
		t.Fatalf("unexpected gzip output to a bytes.Buffer from a stackless worker: %v", err)
	}
	if n := inlineCompressSlots.inUse.Load(); n != inlineCompressSlots.limit {
		t.Fatalf("%d inline slots in use while the test holds all %d", n, inlineCompressSlots.limit)
	}
}

func TestInlineCompressSlotsBound(t *testing.T) {
	t.Parallel()

	inlineCompressSlots.init()
	limit := inlineCompressSlots.limit
	src := createFixedBody(1e4)

	stop := make(chan struct{})
	sampled := make(chan int64)
	go func() {
		var maxInUse int64
		for {
			select {
			case <-stop:
				sampled <- maxInUse
				return
			default:
			}
			maxInUse = max(maxInUse, inlineCompressSlots.inUse.Load())
			runtime.Gosched()
		}
	}()

	var wg sync.WaitGroup
	var failures atomic.Int64
	for range 4 * max(limit, 2) {
		wg.Go(func() {
			for range 20 {
				gz := AppendGzipBytesLevel(nil, src, CompressBestSpeed)
				if got, err := AppendGunzipBytes(nil, gz); err != nil || !bytes.Equal(got, src) {
					failures.Add(1)
				}
				df := AppendDeflateBytesLevel(nil, src, CompressBestSpeed)
				if got, err := AppendInflateBytes(nil, df); err != nil || !bytes.Equal(got, src) {
					failures.Add(1)
				}
				br := AppendBrotliBytesLevel(nil, src, CompressBrotliBestSpeed)
				if got, err := AppendUnbrotliBytes(nil, br); err != nil || !bytes.Equal(got, src) {
					failures.Add(1)
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	if maxInUse := <-sampled; maxInUse > limit {
		t.Fatalf("%d inline compressions at once, want at most %d", maxInUse, limit)
	}
	if n := failures.Load(); n > 0 {
		t.Fatalf("%d compressions don't decompress to their input", n)
	}
}

func TestRunStacklessRunsRefusedWork(t *testing.T) {
	t.Parallel()

	src := createFixedBody(1e4)
	var want byteSliceWriter
	writeGzipNow(&want, src, CompressDefaultCompression)

	// A wrapper that took the work leaves nothing for the caller to do.
	var taken byteSliceWriter
	ctx := &compressCtx{w: &taken, p: src, level: CompressDefaultCompression}
	runStackless(func(any) bool { return true }, nonblockingWriteGzip, ctx)
	if len(taken.b) != 0 {
		t.Fatal("work a stackless worker took ran on the caller too")
	}

	// One that refused it, as stackless.NewFunc does when its queue is full,
	// leaves the caller to compress.
	var refused byteSliceWriter
	ctx = &compressCtx{w: &refused, p: src, level: CompressDefaultCompression}
	runStackless(func(any) bool { return false }, nonblockingWriteGzip, ctx)
	if !bytes.Equal(refused.b, want.b) {
		t.Fatal("work the stackless workers refused wasn't compressed on the caller")
	}
}
