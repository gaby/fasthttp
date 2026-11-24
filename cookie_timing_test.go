package fasthttp

import (
	"testing"
)

func BenchmarkCookieParseMin(b *testing.B) {
	var c Cookie
	s := []byte("xxx=yyy")
	for i := 0; i < b.N; i++ {
		if err := c.ParseBytes(s); err != nil {
			b.Fatalf("unexpected error when parsing cookies: %v", err)
		}
	}
}

func BenchmarkCookieParseNoExpires(b *testing.B) {
	var c Cookie
	s := []byte("xxx=yyy; domain=foobar.com; path=/a/b")
	for i := 0; i < b.N; i++ {
		if err := c.ParseBytes(s); err != nil {
			b.Fatalf("unexpected error when parsing cookies: %v", err)
		}
	}
}

func BenchmarkCookieParseFull(b *testing.B) {
	var c Cookie
	s := []byte("xxx=yyy; expires=Tue, 10 Nov 2009 23:00:00 GMT; domain=foobar.com; path=/a/b")
	for i := 0; i < b.N; i++ {
		if err := c.ParseBytes(s); err != nil {
			b.Fatalf("unexpected error when parsing cookies: %v", err)
		}
	}
}

// browserRequestCookies is a Cookie header as a browser sends it to a site
// running analytics and consent tools: about 1 KB in eighteen cookies, with
// the session and CSRF cookies a server reads among them.
var browserRequestCookies = []byte("_ga=GA1.1.1234567890.1700000000; _ga_ABCDEF1234=GS1.1.1700000000.5.1.1700000500.0.0.0; " +
	"_gid=GA1.2.987654321.1700000000; _fbp=fb.1.1700000000000.1234567890; " +
	"_gcl_au=1.1.1234567890.1700000000; OptanonAlertBoxClosed=2024-01-01T00:00:00.000Z; " +
	"OptanonConsent=isGpcEnabled=0&datestamp=Mon+Jan+01+2024+00%3A00%3A00+GMT%2B0000&version=202310.1.0&browserGpcFlag=0" +
	"&isIABGlobal=false&hosts=&landingPath=NotLandingPage&groups=C0001%3A1%2CC0002%3A1%2CC0003%3A1%2CC0004%3A1; " +
	"theme=dark; lang=en-US; tz=Europe%2FBerlin; ab_test_bucket=variant_b; feature_flags=new_nav,beta_search,dark_mode; " +
	"_hjSessionUser_123456=eyJpZCI6IjEyMzQ1Njc4LTkwYWItY2RlZi0xMjM0LTU2Nzg5MGFiY2RlZiIsImNyZWF0ZWQiOjE3MDAw" +
	"MDAwMDAwMDAsImV4aXN0aW5nIjp0cnVlfQ==; " +
	"_hjSession_123456=eyJpZCI6ImFiY2RlZjEyLTM0NTYtNzg5MC1hYmNkLWVmMTIzNDU2Nzg5MCIsImMiOjE3MDAwMDAwMDAw" +
	"MDAsInMiOjAsInIiOjAsInNiIjowfQ==; " +
	"csrf_=Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6; " +
	"session_id=3f2a9c4e-8b7d-4e1f-9a6c-2d5b8e7f1a3c")

func BenchmarkParseRequestCookies(b *testing.B) {
	for _, bc := range []struct {
		name   string
		header []byte
	}{
		{"short", []byte("session=abc123def456; theme=dark; _ga=GA1.1.123456789.1700000000")},
		{"browser", browserRequestCookies},
	} {
		b.Run(bc.name, func(b *testing.B) {
			var cookies []argsKV
			b.SetBytes(int64(len(bc.header)))
			b.ReportAllocs()
			for b.Loop() {
				cookies = parseRequestCookies(cookies[:0], bc.header)
			}
			if len(cookies) == 0 {
				b.Fatal("no cookies parsed")
			}
		})
	}
}
