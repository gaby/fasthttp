package fasthttp

import (
	"bufio"
	"bytes"
	"io"
	"sync"

	"github.com/valyala/bytebufferpool"
)

type bodyStreamHeader interface {
	ContentLength() int
	ReadTrailer(r *bufio.Reader) error
}

type requestStream struct {
	header          bodyStreamHeader
	prefetchedBytes *bytes.Reader
	reader          *bufio.Reader
	// maxBodySize caps how much of this stream may be buffered into memory by
	// Request.Body and friends. Reading the stream directly is not capped: that
	// is the documented way to handle bodies larger than the limit. 0 means no
	// limit.
	maxBodySize    int
	totalBytesRead int
	chunkLeft      int
	// chunkedDone records that the terminating chunk was consumed.
	chunkedDone bool
}

// atEnd reports whether the whole body has been read. A connection whose
// request body was left partly unread can no longer be framed.
func (rs *requestStream) atEnd() bool {
	if rs.header.ContentLength() == -1 {
		return rs.chunkedDone
	}
	return rs.totalBytesRead >= rs.header.ContentLength()
}

func (rs *requestStream) Read(p []byte) (int, error) {
	var (
		n   int
		err error
	)
	if rs.header.ContentLength() == -1 {
		if rs.chunkLeft == 0 {
			chunkSize, err := parseChunkSize(rs.reader)
			if err != nil {
				return 0, err
			}
			if chunkSize == 0 {
				err = rs.header.ReadTrailer(rs.reader)
				if err != nil && err != io.EOF {
					return 0, err
				}
				rs.chunkedDone = true
				return 0, io.EOF
			}
			rs.chunkLeft = chunkSize
		}
		bytesToRead := min(rs.chunkLeft, len(p))
		n, err = rs.reader.Read(p[:bytesToRead])
		rs.totalBytesRead += n
		rs.chunkLeft -= n
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		if err == nil && rs.chunkLeft == 0 {
			err = readCrLf(rs.reader)
		}
		return n, err
	}
	if rs.totalBytesRead == rs.header.ContentLength() {
		return 0, io.EOF
	}
	prefetchedSize := int(rs.prefetchedBytes.Size())
	if prefetchedSize > rs.totalBytesRead {
		left := prefetchedSize - rs.totalBytesRead
		if len(p) > left {
			p = p[:left]
		}
		n, err := rs.prefetchedBytes.Read(p)
		rs.totalBytesRead += n
		if n == rs.header.ContentLength() {
			return n, io.EOF
		}
		return n, err
	}
	left := rs.header.ContentLength() - rs.totalBytesRead
	if left > 0 && len(p) > left {
		p = p[:left]
	}
	n, err = rs.reader.Read(p)
	rs.totalBytesRead += n
	if err != nil {
		return n, err
	}

	if rs.totalBytesRead == rs.header.ContentLength() {
		err = io.EOF
	}
	return n, err
}

func acquireRequestStream(b *bytebufferpool.ByteBuffer, r *bufio.Reader, h bodyStreamHeader, maxBodySize int) *requestStream {
	rs := requestStreamPool.Get().(*requestStream) //nolint:forcetypeassert
	rs.prefetchedBytes = bytes.NewReader(b.B)
	rs.reader = r
	rs.header = h
	rs.maxBodySize = maxBodySize
	return rs
}

func releaseRequestStream(rs *requestStream) {
	rs.prefetchedBytes = nil
	rs.maxBodySize = 0
	rs.totalBytesRead = 0
	rs.chunkLeft = 0
	rs.chunkedDone = false
	rs.reader = nil
	rs.header = nil
	requestStreamPool.Put(rs)
}

// bodyStreamBufferLimit reports how much of r may be buffered into memory,
// 0 if unlimited.
func bodyStreamBufferLimit(r io.Reader) int {
	if rs, ok := r.(*requestStream); ok {
		return rs.maxBodySize
	}
	return 0
}

var requestStreamPool = sync.Pool{
	New: func() any {
		return &requestStream{}
	},
}
