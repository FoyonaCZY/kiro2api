package kiro

import (
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

// readMaybeCompressed 手动设置了 Accept-Encoding 时 Go 不会自动解压，这里按响应头解压。
func readMaybeCompressed(resp *http.Response) ([]byte, error) {
	r, err := DecodeBody(resp)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// DecodeBody 按 Content-Encoding 包一层解压 reader。
func DecodeBody(resp *http.Response) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip":
		return gzip.NewReader(resp.Body)
	case "deflate":
		return flate.NewReader(resp.Body), nil
	case "br":
		return brotli.NewReader(resp.Body), nil
	}
	return resp.Body, nil
}
