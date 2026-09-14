package httpx

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Pool 按代理 URL 复用的 HTTP 客户端池（"" = 直连）
type Pool struct {
	mu      sync.Mutex
	clients map[string]*http.Client
}

func NewPool() *Pool {
	return &Pool{clients: map[string]*http.Client{}}
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
		DialContext:           dialer.DialContext,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	if proxyURL != "" {
		u, err := ParseProxyURL(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(u)
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
	switch parsed.Scheme {
	case "http", "https", "socks5":
		return parsed, nil
	}
	return nil, fmt.Errorf("不支持的代理协议: %s", parsed.Scheme)
}
