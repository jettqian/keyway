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

	socksproxy "golang.org/x/net/proxy"
)

// UpstreamEndpoint 在渠道 base_url 后拼出上游端点：
// OpenAI/Anthropic 兼容站的对话端点位于 /v1 之下（/v1/chat/completions、
// /v1/responses、/v1/messages）；base_url 已以 /v1 结尾时直接拼接，避免 /v1/v1。
// 传入的 path 不带 /v1 前缀（如 "/chat/completions"、"/responses"、"/messages"）
func UpstreamEndpoint(baseURL, path string) string {
	b := strings.TrimSuffix(baseURL, "/")
	if strings.HasSuffix(b, "/v1") {
		return b + path
	}
	return b + "/v1" + path
}

// Pool 按代理 URL 复用的 HTTP 客户端池（"" = 直连）
type Pool struct {
	mu                    sync.Mutex
	clients               map[string]*http.Client
	responseHeaderTimeout time.Duration // ≤0 = 不限制
}

// NewPool 创建客户端池；responseHeaderTimeoutSec 限制上游响应头等待
// （默认场景见 config.KEYWAY_RESPONSE_HEADER_TIMEOUT_S），0 = 不限制
func NewPool(responseHeaderTimeoutSec int) *Pool {
	return &Pool{
		clients:               map[string]*http.Client{},
		responseHeaderTimeout: time.Duration(responseHeaderTimeoutSec) * time.Second,
	}
}

// Get 获取（或创建）指定代理的客户端
func (p *Pool) Get(proxyURL string) (*http.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cl, ok := p.clients[proxyURL]; ok {
		return cl, nil
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	// 仅限制响应头等待；流式 body 阶段不受影响（长流式不被切断）
	if p.responseHeaderTimeout > 0 {
		transport.ResponseHeaderTimeout = p.responseHeaderTimeout
	}
	if proxyURL != "" {
		u, err := ParseProxyURL(proxyURL)
		if err != nil {
			return nil, err
		}
		if u.Scheme == "socks5" {
			var auth *socksproxy.Auth
			if u.User != nil {
				password, _ := u.User.Password()
				auth = &socksproxy.Auth{User: u.User.Username(), Password: password}
			}
			dialer, err := socksproxy.SOCKS5("tcp", u.Host, auth, dialer)
			if err != nil {
				return nil, fmt.Errorf("创建 SOCKS5 代理失败: %w", err)
			}
			transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
				return dialer.Dial(network, address)
			}
		} else {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	cl := &http.Client{Transport: transport}
	p.clients[proxyURL] = cl
	return cl, nil
}

// ParseProxyURL 校验代理地址（http/https/socks5）
func ParseProxyURL(u string) (*url.URL, error) {
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("代理地址无效: %w", err)
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("代理地址缺少主机名")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5":
		return parsed, nil
	}
	return nil, fmt.Errorf("不支持的代理协议: %s", parsed.Scheme)
}
