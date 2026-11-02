package proxyman

import "testing"

// TestPaths按出站策略展开 验证渠道级 proxy_policy 的路径展开顺序：
//   - auto（默认/未知值）：直连 → 个人代理（历史行为）
//   - proxy_first：个人代理在前，直连兜底
//   - proxy_only：仅代理，直连不参与；无任何代理时返回空（渠道不可用）
//
// 公共代理路径依赖 store 查询，此处用 allowPublic=false 覆盖策略分支，
// 公共代理的启用与排序由 e2e（cmd/keyway/proxy_test.go）覆盖
func TestPaths按出站策略展开(t *testing.T) {
	const personal = "socks5://127.0.0.1:7890"
	m := New(nil, "secret")
	cases := []struct {
		name   string
		policy string
		want   []string
	}{
		{"auto 直连优先", ProxyPolicyAuto, []string{"direct", "personal"}},
		{"空策略按 auto", "", []string{"direct", "personal"}},
		{"未知值回退 auto", "bogus", []string{"direct", "personal"}},
		{"proxy_first 代理在前直连兜底", ProxyPolicyProxyFirst, []string{"personal", "direct"}},
		{"proxy_only 无直连", ProxyPolicyProxyOnly, []string{"personal"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := m.Paths(personal, false, tc.policy)
			if len(paths) != len(tc.want) {
				t.Fatalf("路径数 = %d， want %d（%v）", len(paths), len(tc.want), paths)
			}
			for i, p := range paths {
				if p.Via != tc.want[i] {
					t.Fatalf("路径[%d].Via = %s，want %s", i, p.Via, tc.want[i])
				}
			}
		})
	}

	t.Run("proxy_only 无任何代理返回空", func(t *testing.T) {
		if paths := m.Paths("", false, ProxyPolicyProxyOnly); len(paths) != 0 {
			t.Fatalf("应返回空路径集合，得到 %v", paths)
		}
	})

	t.Run("proxy_first 无代理时退化为仅直连", func(t *testing.T) {
		paths := m.Paths("", false, ProxyPolicyProxyFirst)
		if len(paths) != 1 || paths[0].Via != "direct" {
			t.Fatalf("无代理应仅剩直连，得到 %v", paths)
		}
	})
}

func TestNormalizeProxyPolicy(t *testing.T) {
	if NormalizeProxyPolicy(ProxyPolicyProxyFirst) != ProxyPolicyProxyFirst ||
		NormalizeProxyPolicy(ProxyPolicyProxyOnly) != ProxyPolicyProxyOnly {
		t.Fatal("合法值不应被改写")
	}
	for _, in := range []string{"", "Auto", "PROXY_ONLY", "direct"} {
		if got := NormalizeProxyPolicy(in); got != ProxyPolicyAuto {
			t.Fatalf("NormalizeProxyPolicy(%q) = %s，want auto", in, got)
		}
	}
}
