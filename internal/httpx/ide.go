package httpx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

// IDERequest 按 Kiro IDE（Electron 里的 Node）发出的样子描述一次请求。
// Header 是完整的头列表（含 Host、Content-Length、Connection），大小写和顺序原样写到线上。
type IDERequest struct {
	Method string
	URL    string
	Header [][2]string
	Body   []byte
}

// Get 取第一个同名头（不区分大小写）。
func (r *IDERequest) Get(name string) string {
	for _, h := range r.Header {
		if strings.EqualFold(h[0], name) {
			return h[1]
		}
	}
	return ""
}

const ideIdleTimeout = 30 * time.Second

// nodeHelloSpec Kiro 1.4.26（Electron 44 / Node 24.21.0）的 ClientHello：
// 不带 ALPN 和 GREASE，扩展顺序固定，密钥交换先给 X25519MLKEM768。每次握手要新建一份。
func nodeHelloSpec() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS13,
		CipherSuites: []uint16{
			0x1301, 0x1302, 0x1303, 0xc02f, 0xc02b, 0xc030, 0xc02c, 0xc027, 0xcca9,
			0xcca8, 0xc009, 0xc013, 0xc00a, 0xc014, 0x009c, 0x009d, 0x002f, 0x0035,
		},
		CompressionMethods: []byte{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.ExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.X25519MLKEM768, utls.X25519, utls.CurveP256, utls.CurveP384}},
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&utls.SessionTicketExtension{},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				0x0403, 0x0804, 0x0401, 0x0503, 0x0805, 0x0501, 0x0806, 0x0601, 0x0201,
			}},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519MLKEM768}, {Group: utls.X25519}}},
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
			&utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13, utls.VersionTLS12}},
		},
	}
}

type ideConn struct {
	net.Conn
	br   *bufio.Reader
	key  string
	idle time.Time
}

type ideTransport struct {
	mu   sync.Mutex
	idle map[string][]*ideConn
	// dialTLS 测试时替换成明文连接。
	dialTLS func(ctx context.Context, px *Proxy, host, port string) (net.Conn, error)
}

var defaultIDE = &ideTransport{idle: map[string][]*ideConn{}, dialTLS: dialNodeTLS}

// UseIDEDialer 测试用：把 TLS 拨号换掉（例如直连本地明文假上游），返回恢复函数。
func UseIDEDialer(d func(ctx context.Context, px *Proxy, host, port string) (net.Conn, error)) func() {
	t := defaultIDE
	t.mu.Lock()
	prev := t.dialTLS
	t.dialTLS = d
	t.idle = map[string][]*ideConn{}
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		t.dialTLS = prev
		t.idle = map[string][]*ideConn{}
		t.mu.Unlock()
	}
}

// DoIDE 用和 IDE 一致的 TLS 指纹、HTTP/1.1 和 keep-alive 发请求。
// timeout 覆盖整个请求直到响应体读完或关闭。响应体不会自动解压。
func DoIDE(ctx context.Context, px *Proxy, req *IDERequest, timeout time.Duration) (*http.Response, error) {
	return defaultIDE.do(ctx, px, req, timeout)
}

func (t *ideTransport) do(ctx context.Context, px *Proxy, req *IDERequest, timeout time.Duration) (*http.Response, error) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("IDE 客户端只支持 https: %s", req.URL)
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	key := px.Key() + "\x00" + host + ":" + port
	for try := 0; ; try++ {
		c, reused, err := t.get(ctx, px, key, host, port)
		if err != nil {
			cancel()
			return nil, err
		}
		resp, err := t.roundTrip(ctx, c, u, req)
		if err == nil {
			resp.Body = newIDEBody(ctx, cancel, t, c, resp)
			return resp, nil
		}
		c.Close()
		// 复用的空闲连接可能已被服务端关掉，换新连接重发一次。
		if reused && try == 0 && ctx.Err() == nil {
			continue
		}
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
}

func (t *ideTransport) roundTrip(ctx context.Context, c *ideConn, u *url.URL, req *IDERequest) (*http.Response, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	var buf bytes.Buffer
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	fmt.Fprintf(&buf, "%s %s HTTP/1.1\r\n", method, u.RequestURI())
	for _, h := range req.Header {
		buf.WriteString(h[0])
		buf.WriteString(": ")
		buf.WriteString(h[1])
		buf.WriteString("\r\n")
	}
	buf.WriteString("\r\n")
	buf.Write(req.Body)
	if _, err := c.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(c.br, &http.Request{Method: method, URL: u})
	if err != nil {
		return nil, err
	}
	// 流式响应可能持续很久，读阶段只受 ctx 控制。
	_ = c.SetDeadline(time.Time{})
	return resp, nil
}

func (t *ideTransport) get(ctx context.Context, px *Proxy, key, host, port string) (*ideConn, bool, error) {
	t.mu.Lock()
	for list := t.idle[key]; len(list) > 0; list = t.idle[key] {
		c := list[len(list)-1]
		t.idle[key] = list[:len(list)-1]
		if time.Since(c.idle) < ideIdleTimeout {
			t.mu.Unlock()
			return c, true, nil
		}
		c.Close()
	}
	dial := t.dialTLS
	t.mu.Unlock()
	raw, err := dial(ctx, px, host, port)
	if err != nil {
		return nil, false, err
	}
	return &ideConn{Conn: raw, br: bufio.NewReaderSize(raw, 32<<10), key: key}, false, nil
}

func (t *ideTransport) put(c *ideConn) {
	c.idle = time.Now()
	t.mu.Lock()
	t.idle[c.key] = append(t.idle[c.key], c)
	t.mu.Unlock()
}

type ideBody struct {
	rc     io.ReadCloser
	t      *ideTransport
	c      *ideConn
	resp   *http.Response
	cancel context.CancelFunc
	stop   func() bool

	mu     sync.Mutex
	eof    bool
	closed bool
	killed bool
}

func newIDEBody(ctx context.Context, cancel context.CancelFunc, t *ideTransport, c *ideConn, resp *http.Response) *ideBody {
	b := &ideBody{rc: resp.Body, t: t, c: c, resp: resp, cancel: cancel, eof: resp.ContentLength == 0}
	b.stop = context.AfterFunc(ctx, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.closed {
			b.killed = true
			c.Close()
		}
	})
	return b
}

func (b *ideBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err == io.EOF {
		b.mu.Lock()
		b.eof = true
		b.mu.Unlock()
	}
	return n, err
}

func (b *ideBody) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	reuse := b.eof && !b.killed && !b.resp.Close
	b.mu.Unlock()
	b.stop()
	err := b.rc.Close()
	if reuse && b.c.br.Buffered() == 0 {
		b.t.put(b.c)
	} else {
		b.c.Close()
	}
	b.cancel()
	return err
}

// dialNodeTLS 经代理（或直连）建 TCP，再用 Node 的 ClientHello 握手。
func dialNodeTLS(ctx context.Context, px *Proxy, host, port string) (net.Conn, error) {
	raw, err := DialVia(ctx, px, net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	uc := utls.UClient(raw, &utls.Config{ServerName: host}, utls.HelloCustom)
	if err := uc.ApplyPreset(nodeHelloSpec()); err != nil {
		raw.Close()
		return nil, err
	}
	if err := uc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return uc, nil
}

func proxyAuth(px *Proxy, u *url.URL) (string, string, bool) {
	if px.Username != "" {
		return px.Username, px.Password, true
	}
	if u.User != nil {
		pw, _ := u.User.Password()
		return u.User.Username(), pw, true
	}
	return "", "", false
}

// DialVia 直连，或经 socks5 / http(s) CONNECT 代理连到 addr。
func DialVia(ctx context.Context, px *Proxy, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if px == nil || px.URL == "" {
		return d.DialContext(ctx, "tcp", addr)
	}
	u, err := url.Parse(px.URL)
	if err != nil {
		return nil, fmt.Errorf("代理地址无效: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if user, pw, ok := proxyAuth(px, u); ok {
			auth = &proxy.Auth{User: user, Password: pw}
		}
		sd, err := proxy.SOCKS5("tcp", u.Host, auth, d)
		if err != nil {
			return nil, err
		}
		cd, ok := sd.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("socks 拨号器不支持 context")
		}
		return cd.DialContext(ctx, "tcp", addr)
	case "http", "https":
		phost := u.Host
		if u.Port() == "" {
			if strings.EqualFold(u.Scheme, "https") {
				phost = net.JoinHostPort(u.Hostname(), "443")
			} else {
				phost = net.JoinHostPort(u.Hostname(), "80")
			}
		}
		conn, err := d.DialContext(ctx, "tcp", phost)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(u.Scheme, "https") {
			tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname()})
			if err := tc.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			conn = tc
		}
		if dl, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(dl)
		}
		req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
		if user, pw, ok := proxyAuth(px, u); ok {
			req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pw)) + "\r\n"
		}
		req += "\r\n"
		if _, err := io.WriteString(conn, req); err != nil {
			conn.Close()
			return nil, err
		}
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err != nil {
			conn.Close()
			return nil, err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			conn.Close()
			return nil, fmt.Errorf("代理 CONNECT 失败: %s", resp.Status)
		}
		if br.Buffered() > 0 {
			conn.Close()
			return nil, errors.New("代理 CONNECT 响应后有多余数据")
		}
		_ = conn.SetDeadline(time.Time{})
		return conn, nil
	}
	return nil, errors.New("代理协议只支持 socks5、socks5h、http、https")
}
