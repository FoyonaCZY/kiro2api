package httpx

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// helloShape ClientHello 里和指纹有关的部分：跳过 random、session id、SNI 主机名和密钥数据。
type helloShape struct {
	Ciphers     string
	Compression string
	Exts        []string
}

func parseHello(t *testing.T, b []byte) helloShape {
	t.Helper()
	if len(b) < 9 || b[0] != 0x16 || b[5] != 0x01 {
		t.Fatalf("不是 ClientHello: % x", b[:min(len(b), 9)])
	}
	p := 9 + 2 + 32
	p += 1 + int(b[p])
	cl := int(binary.BigEndian.Uint16(b[p:]))
	s := helloShape{Ciphers: hex.EncodeToString(b[p+2 : p+2+cl])}
	p += 2 + cl
	s.Compression = hex.EncodeToString(b[p+1 : p+1+int(b[p])])
	p += 1 + int(b[p])
	end := p + 2 + int(binary.BigEndian.Uint16(b[p:]))
	p += 2
	for p < end {
		typ := binary.BigEndian.Uint16(b[p:])
		l := int(binary.BigEndian.Uint16(b[p+2:]))
		d := b[p+4 : p+4+l]
		desc := fmt.Sprintf("%04x", typ)
		switch typ {
		case 0x0000: // SNI：只比较存在与否
		case 0x0033: // key_share：只比较组和长度
			var groups []string
			for q := 2; q < len(d); {
				gl := int(binary.BigEndian.Uint16(d[q+2:]))
				groups = append(groups, fmt.Sprintf("%04x/%d", binary.BigEndian.Uint16(d[q:]), gl))
				q += 4 + gl
			}
			desc += ":" + strings.Join(groups, ",")
		default:
			desc += ":" + hex.EncodeToString(d)
		}
		s.Exts = append(s.Exts, desc)
		p += 4 + l
	}
	return s
}

func readRecord(t *testing.T, r io.Reader) []byte {
	t.Helper()
	head := make([]byte, 5)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint16(head[3:]))
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatal(err)
	}
	return append(head, body...)
}

func TestNodeClientHelloMatchesKiro(t *testing.T) {
	raw, err := os.ReadFile("testdata/kiro_1.4.26_clienthello.hex")
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		got <- readRecord(t, c)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	go func() { _, _ = dialNodeTLS(ctx, nil, "localhost", port) }()
	var hello []byte
	select {
	case hello = <-got:
	case <-ctx.Done():
		t.Fatal("没有收到 ClientHello")
	}
	if g, w := parseHello(t, hello), parseHello(t, want); !reflect.DeepEqual(g, w) {
		t.Fatalf("ClientHello 和 Kiro 不一致\n got: %+v\nwant: %+v", g, w)
	}
}

// 明文假服务器，检查请求按原样写出并能复用连接。
func TestDoIDEWritesHeadersVerbatimAndReuses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	heads := make(chan string, 4)
	accepted := make(chan struct{}, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					req, err := http.ReadRequest(br)
					if err != nil {
						return
					}
					body, _ := io.ReadAll(req.Body)
					var sb strings.Builder
					fmt.Fprintf(&sb, "%s %s|", req.Method, req.RequestURI)
					_ = req.Header.WriteSubset(&sb, nil)
					heads <- sb.String() + "|" + string(body)
					io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
				}
			}(c)
		}
	}()
	tr := &ideTransport{idle: map[string][]*ideConn{}, dialTLS: func(ctx context.Context, _ *Proxy, _, _ string) (net.Conn, error) {
		return net.Dial("tcp", ln.Addr().String())
	}}
	req := &IDERequest{Method: "POST", URL: "https://example.test/v1/responses?a=1", Body: []byte("{}"),
		Header: [][2]string{{"Content-Type", "application/json"}, {"x-amzn-kiro-origin", "AI_EDITOR"}, {"Content-Length", "2"}, {"Host", "example.test"}, {"Connection", "keep-alive"}}}
	for i := 0; i < 2; i++ {
		resp, err := tr.do(context.Background(), nil, req, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != "ok" {
			t.Fatalf("body %q", b)
		}
		h := <-heads
		if !strings.HasPrefix(h, "POST /v1/responses?a=1|") || !strings.Contains(h, "X-Amzn-Kiro-Origin: AI_EDITOR") || !strings.HasSuffix(h, "|{}") {
			t.Fatalf("请求不对: %s", h)
		}
	}
	if len(accepted) != 1 {
		t.Fatalf("应复用同一条连接，实际建了 %d 条", len(accepted))
	}
}
