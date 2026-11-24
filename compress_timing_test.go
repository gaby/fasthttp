package fasthttp

import (
	"strconv"
	"strings"
	"testing"
)

// compressBenchBody is about 6 KB of JSON shaped like an API listing:
// records with ids, names, prices, timestamps and free text, varied enough
// not to compress like one string repeated.
var compressBenchBody = func() []byte {
	words := strings.Fields("alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike " +
		"november oscar papa quebec romeo sierra tango uniform victor whiskey xray yankee zulu widget gadget " +
		"sprocket bracket flange gasket bearing spindle rotor stator housing coupling valve nozzle")
	x := uint32(1)
	next := func() int {
		x = x*1664525 + 1013904223
		return int(x >> 8)
	}
	b := []byte{'['}
	for i := 0; len(b) < 6000; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"id":"`...)
		b = strconv.AppendInt(b, int64(next()), 16)
		b = append(b, `","name":"`...)
		b = append(b, words[next()%len(words)]...)
		b = append(b, ' ')
		b = append(b, words[next()%len(words)]...)
		b = append(b, `","price":`...)
		b = strconv.AppendInt(b, int64(next()%100000), 10)
		b = append(b, `,"created":"2026-`...)
		b = strconv.AppendInt(b, int64(1+next()%12), 10)
		b = append(b, '-')
		b = strconv.AppendInt(b, int64(1+next()%28), 10)
		b = append(b, `","description":"`...)
		for j := 3 + next()%8; j > 0; j-- {
			b = append(b, words[next()%len(words)]...)
			b = append(b, ' ')
		}
		b = append(b, `"}`...)
	}
	return append(b, ']')
}()

func benchmarkCompress(b *testing.B, appendCompressed func(dst, src []byte, level int) []byte) {
	for _, lc := range []struct {
		name  string
		level int
	}{
		{"speed", CompressBestSpeed},
		{"default", CompressDefaultCompression},
	} {
		b.Run(lc.name+"/serial", func(b *testing.B) {
			b.SetBytes(int64(len(compressBenchBody)))
			b.ReportAllocs()
			var dst []byte
			for b.Loop() {
				dst = appendCompressed(dst[:0], compressBenchBody, lc.level)
			}
		})
		b.Run(lc.name+"/parallel", func(b *testing.B) {
			b.SetBytes(int64(len(compressBenchBody)))
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				var dst []byte
				for pb.Next() {
					dst = appendCompressed(dst[:0], compressBenchBody, lc.level)
				}
			})
		})
	}
}

func BenchmarkAppendGzipBytesLevel(b *testing.B) {
	benchmarkCompress(b, AppendGzipBytesLevel)
}

func BenchmarkAppendDeflateBytesLevel(b *testing.B) {
	benchmarkCompress(b, AppendDeflateBytesLevel)
}
