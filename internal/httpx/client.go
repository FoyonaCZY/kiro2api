// Package httpx 构建带代理的 HTTP 客户端。
package httpx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Proxy 代理配置。URL 为空表示直连。
type Proxy struct {
	URL      string
	Username string
	Password string
}

// Key 用作客户端缓存键。
func (p *Proxy) Key() string {
	if p == nil {
		return "direct"
	}
	return p.URL + "\x00" + p.Username + "\x00" + p.Password
}

// New 连接超时 30 秒，总超时 timeout。不读取环境变量代理，direct 就是直连。
func New(p *Proxy, timeout time.Duration) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:             nil,
		DialContext:       dialer.DialContext,
		ForceAttemptHTTP2: true,
		// 与 kiro.rs 一致：不自动加 Accept-Encoding，需要解压的地方自己处理。
		DisableCompression:    true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if p != nil && p.URL != "" {
		u, err := url.Parse(p.URL)
		if err != nil {
			return nil, fmt.Errorf("代理地址无效: %w", err)
		}
		switch strings.ToLower(u.Scheme) {
		case "socks5", "socks5h":
			var auth *proxy.Auth
			if p.Username != "" {
				auth = &proxy.Auth{User: p.Username, Password: p.Password}
			} else if u.User != nil {
				pw, _ := u.User.Password()
				auth = &proxy.Auth{User: u.User.Username(), Password: pw}
			}
			d, err := proxy.SOCKS5("tcp", u.Host, auth, dialer)
			if err != nil {
				return nil, err
			}
			cd, ok := d.(proxy.ContextDialer)
			if !ok {
				return nil, fmt.Errorf("socks 拨号器不支持 context")
			}
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return cd.DialContext(ctx, network, addr)
			}
		case "http", "https":
			pu := *u
			if p.Username != "" {
				pu.User = url.UserPassword(p.Username, p.Password)
			}
			tr.Proxy = http.ProxyURL(&pu)
		default:
			return nil, fmt.Errorf("代理协议只支持 socks5、socks5h、http、https")
		}
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// Pool 按代理缓存客户端，避免每次请求重建连接池。
type Pool struct {
	mu      sync.Mutex
	timeout time.Duration
	clients map[string]*http.Client
}

// NewPool 创建客户端池。
func NewPool(timeout time.Duration) *Pool {
	return &Pool{timeout: timeout, clients: map[string]*http.Client{}}
}

// Get 取或建客户端。
func (p *Pool) Get(px *Proxy) (*http.Client, error) {
	key := px.Key()
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[key]; ok {
		return c, nil
	}
	c, err := New(px, p.timeout)
	if err != nil {
		return nil, err
	}
	p.clients[key] = c
	return c, nil
}
