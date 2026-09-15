package proxyman

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"keyway/internal/crypto"
	"keyway/internal/store"
)

// Path 出站路径：直连 / 个人代理 / 公共代理
type Path struct {
	ProxyURL string // "" = 直连
	Via      string // direct | personal | proxy:{id}
	ProxyID  int64  // 公共代理 ID（公共路径时非零）
}

// Manager 公共代理池管理：解密缓存 + 按用户流量统计（仅统计，DESIGN §5.4 FR-P2）
type Manager struct {
	store  *store.Store
	secret string

	mu     sync.Mutex
	loadAt time.Time
	urls   map[int64]string // proxyID → 解密后 URL

	cmu      sync.Mutex
	counters map[string]int64 // "day|userID|proxyID" → bytes
}

func New(st *store.Store, secret string) *Manager {
	return &Manager{store: st, secret: secret, counters: map[string]int64{}}
}

// Reload 管理端变更后调用，使缓存立即失效
func (m *Manager) Reload() {
	m.mu.Lock()
	m.loadAt = time.Time{}
	m.mu.Unlock()
}

// list 公共代理（缓存 30s）
func (m *Manager) list() map[int64]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.urls != nil && time.Since(m.loadAt) < 30*time.Second {
		return m.urls
	}
	var proxies []store.Proxy
	m.store.DB().Where("enabled = 1").Order("id").Find(&proxies)
	urls := map[int64]string{}
	for i := range proxies {
		if b, err := crypto.Decrypt(m.secret, "proxy", proxies[i].URLEnc); err == nil {
			urls[proxies[i].ID] = string(b)
		}
	}
	m.urls = urls
	m.loadAt = time.Now()
	return urls
}

// Paths 路径集合：直连 → 个人代理 → 公共代理（总上限 4，DESIGN §5.10）
func (m *Manager) Paths(personalProxyURL string, allowPublic bool) []Path {
	paths := []Path{{ProxyURL: "", Via: "direct"}}
	if personalProxyURL != "" {
		paths = append(paths, Path{ProxyURL: personalProxyURL, Via: "personal"})
	}
	if allowPublic {
		urls := m.list()
		ids := make([]int64, 0, len(urls))
		for id := range urls {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			u := urls[id]
			if len(paths) >= 4 {
				break
			}
			paths = append(paths, Path{ProxyURL: u, Via: fmt.Sprintf("proxy:%d", id), ProxyID: id})
		}
	}
	return paths
}

// Record 记录公共代理出站流量（内存累计，批量落库）
func (m *Manager) Record(userID, proxyID, bytes int64) {
	if proxyID == 0 || bytes <= 0 {
		return
	}
	key := time.Now().Format("2006-01-02") + "|" + fmt.Sprint(userID) + "|" + fmt.Sprint(proxyID)
	m.cmu.Lock()
	m.counters[key] += bytes
	m.cmu.Unlock()
}

// Start 流量统计落库循环（30s 批量 UPSERT proxy_usage）
func (m *Manager) Start(stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			m.Flush()
			return
		case <-ticker.C:
			m.Flush()
		}
	}
}

// Flush 立即把累计流量写入 proxy_usage
func (m *Manager) Flush() {
	m.cmu.Lock()
	snapshot := m.counters
	m.counters = map[string]int64{}
	m.cmu.Unlock()
	for key, bytes := range snapshot {
		parts := strings.Split(key, "|")
		if len(parts) != 3 || bytes <= 0 {
			continue
		}
		day := parts[0]
		userID, err1 := strconv.ParseInt(parts[1], 10, 64)
		proxyID, err2 := strconv.ParseInt(parts[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		m.store.DB().Exec(`
			INSERT INTO proxy_usage (user_id, proxy_id, day, bytes) VALUES (?, ?, ?, ?)
			ON CONFLICT(user_id, proxy_id, day) DO UPDATE SET bytes = bytes + excluded.bytes
		`, userID, proxyID, day, bytes)
	}
}
