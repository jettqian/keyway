package relay

import (
	"net/http"
	"testing"
)

// TestCopyForwardHeaders 指纹伪装（FR-R11）：mask 开启时仅改写 OS 指纹头
// （x-stainless-os/arch、sec-ch-ua-platform）为 Windows 形态，其余头原样透传；
// 关闭时全部原样。真实 key 实测（2026-10-16）：拦截面为 x-stainless-os，
// 剥离 SDK 头/改写 UA 均会触发上游 preflight 400，故必须保留原头仅改值。
func TestCopyForwardHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("User-Agent", "claude-cli/2.1.301 (external, cli; ubuntu 22.04; x64) linux/x64")
	src.Set("X-Stainless-Os", "Linux")
	src.Set("X-Stainless-Arch", "x64")
	src.Set("X-Stainless-Lang", "js")
	src.Set("X-Stainless-Runtime", "node")
	src.Set("Sec-Ch-Ua-Platform", "\"Linux\"")
	src.Set("Accept", "application/json")
	src.Set("Authorization", "Bearer client-token")
	src.Set("X-Keep-Me", "yes")

	t.Run("关闭时原样转发", func(t *testing.T) {
		dst := http.Header{}
		copyForwardHeaders(dst, src, false)
		if got := dst.Get("User-Agent"); got != src.Get("User-Agent") {
			t.Fatalf("UA 应原样透传，得到 %q", got)
		}
		if dst.Get("X-Stainless-Os") != "Linux" || dst.Get("Sec-Ch-Ua-Platform") != "\"Linux\"" {
			t.Fatalf("SDK/浏览器指纹头应原样透传: %v", dst)
		}
		if dst.Get("X-Keep-Me") != "yes" || dst.Get("Accept") != "application/json" {
			t.Fatalf("普通头应透传: %v", dst)
		}
		if dst.Get("Authorization") != "" {
			t.Fatalf("认证头不应透传: %v", dst)
		}
	})

	t.Run("开启时改写OS指纹", func(t *testing.T) {
		dst := http.Header{}
		copyForwardHeaders(dst, src, true)
		if got := dst.Get("X-Stainless-Os"); got != "Windows" {
			t.Fatalf("x-stainless-os 应改写为 Windows，得到 %q", got)
		}
		if got := dst.Get("X-Stainless-Arch"); got != "x64" {
			t.Fatalf("x-stainless-arch 应改写为 x64，得到 %q", got)
		}
		if got := dst.Get("Sec-Ch-Ua-Platform"); got != `"Windows"` {
			t.Fatalf("sec-ch-ua-platform 应改写为 Windows，得到 %q", got)
		}
		// UA 与其余 SDK 遥测头必须保留：剥离会破坏客户端特征（上游 preflight 400）
		if got := dst.Get("User-Agent"); got != src.Get("User-Agent") {
			t.Fatalf("UA 应原样保留，得到 %q", got)
		}
		if dst.Get("X-Stainless-Lang") != "js" || dst.Get("X-Stainless-Runtime") != "node" {
			t.Fatalf("其余 x-stainless-* 应原样保留: %v", dst)
		}
		if dst.Get("X-Keep-Me") != "yes" {
			t.Fatalf("无关普通头应保留: %v", dst)
		}
		if dst.Get("Authorization") != "" {
			t.Fatalf("认证头不应透传: %v", dst)
		}
	})

	t.Run("开启但客户端无指纹头时原样", func(t *testing.T) {
		plain := http.Header{}
		plain.Set("User-Agent", "codex_cli_rs/0.46.0 (Ubuntu 22.04.5 LTS; x86_64)")
		plain.Set("Accept", "application/json")
		dst := http.Header{}
		copyForwardHeaders(dst, plain, true)
		if dst.Get("User-Agent") != plain.Get("User-Agent") || dst.Get("Accept") != "application/json" {
			t.Fatalf("无 SDK 指纹头的客户端应原样透传: %v", dst)
		}
		if dst.Get("X-Stainless-Os") != "" {
			t.Fatalf("不应凭空捏造 x-stainless-os: %v", dst)
		}
	})
}
