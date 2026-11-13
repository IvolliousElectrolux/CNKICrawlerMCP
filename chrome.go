package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
)

// chromeDialTLS 用 Chrome 131 的握手, 但只协商 HTTP/1.1.
// 知网会按 TLS 指纹把 Go 自带的客户端转到点选验证, 浏览器则不会.
func chromeDialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	raw, err := (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	uconn := utls.UClient(raw, &utls.Config{ServerName: host}, utls.HelloCustom)
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_131)
	if err != nil {
		raw.Close()
		return nil, err
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	if err = uconn.ApplyPreset(&spec); err != nil {
		raw.Close()
		return nil, err
	}
	if err = uconn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return uconn, nil
}

func newChromeTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{
			DialTLSContext:    chromeDialTLS,
			ForceAttemptHTTP2: false,
			TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
		}
	}
	cloned := base.Clone()
	cloned.DialTLSContext = chromeDialTLS
	cloned.ForceAttemptHTTP2 = false
	cloned.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return cloned
}
