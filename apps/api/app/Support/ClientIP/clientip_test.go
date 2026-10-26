package clientip

import "testing"

func TestResolveDirectRemoteNoTrust(t *testing.T) {
	r := NewResolver(Config{})
	// 零值配置 fail-closed：对端是公网、不信任任何代理，忽略伪造 XFF。
	got := r.Resolve("203.0.113.50", func(h string) string {
		if h == headerXForwardedFor {
			return "1.2.3.4"
		}
		return ""
	})
	if got != "203.0.113.50" {
		t.Fatalf("expected remote IP, got %q", got)
	}
}

// TestResolveIgnoresForwardedHeadersWhenTrustProxyDisabled 覆盖 D3：
// 总开关关闭时，即使对端落在私网/loopback，也不读任何转发头。
func TestResolveIgnoresForwardedHeadersWhenTrustProxyDisabled(t *testing.T) {
	r := NewResolver(Config{TrustPrivate: true, TrustLoopback: true})
	got := r.Resolve("10.0.0.1", func(h string) string {
		switch h {
		case "CF-Connecting-IP":
			return "198.51.100.7"
		case "X-Real-IP":
			return "198.51.100.8"
		case headerXForwardedFor:
			return "198.51.100.9, 10.0.0.1"
		}
		return ""
	})
	if got != "10.0.0.1" {
		t.Fatalf("expected remote IP with trust proxy disabled, got %q", got)
	}
}

func TestResolveXFFStripTrustedProxies(t *testing.T) {
	// 只有显式列出的代理才在链条上被剥掉；私网整类不算代理。
	r := NewResolver(Config{
		TrustProxy: true,
		Proxies:    []string{"10.0.0.0/8", "172.18.0.0/16"},
	})
	// Docker/Nuxt 对端是私网，XFF = client, edge, nuxt
	got := r.Resolve("172.18.0.5", func(h string) string {
		if h == headerXForwardedFor {
			return "203.0.113.9, 10.0.0.2, 172.18.0.5"
		}
		return ""
	})
	if got != "203.0.113.9" {
		t.Fatalf("expected client from XFF, got %q", got)
	}
}

func TestResolveCFConnectingIPPreferred(t *testing.T) {
	// CDN 头只在对端属于显式 Proxies 时采信。
	r := NewResolver(Config{TrustProxy: true, Proxies: []string{"10.0.0.1"}})
	got := r.Resolve("10.0.0.1", func(h string) string {
		switch h {
		case "CF-Connecting-IP":
			return "198.51.100.7"
		case headerXForwardedFor:
			return "203.0.113.1, 10.0.0.1"
		}
		return ""
	})
	if got != "198.51.100.7" {
		t.Fatalf("expected CF-Connecting-IP, got %q", got)
	}
}

// TestResolveCDNHeaderFromExplicitProxyCIDR 覆盖显式 CIDR 这条门控分支：
// exact 匹配之外，CIDR 内的对端同样算显式代理。
func TestResolveCDNHeaderFromExplicitProxyCIDR(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, Proxies: []string{"10.0.0.0/8"}})
	got := r.Resolve("10.9.8.7", func(h string) string {
		if h == "True-Client-IP" {
			return "198.51.100.7"
		}
		return ""
	})
	if got != "198.51.100.7" {
		t.Fatalf("expected True-Client-IP from explicit CIDR peer, got %q", got)
	}
}

// TestResolveIgnoresCDNHeadersFromImplicitTrustedPeer 覆盖 P2-1：
// 对端只是「整类可信」（私网/loopback）时，CF / True-Client-IP 可被任意客户端写入，
// 必须忽略并落回 X-Real-IP。WebSocket 直连 :18080（TRUST_PROXY_LOOPBACK）走的就是这条路径。
func TestResolveIgnoresCDNHeadersFromImplicitTrustedPeer(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, TrustPrivate: true})
	got := r.Resolve("10.0.0.1", func(h string) string {
		switch h {
		case "CF-Connecting-IP":
			return "1.1.1.1"
		case "True-Client-IP":
			return "1.1.1.2"
		case headerXRealIP:
			return "198.51.100.20"
		}
		return ""
	})
	if got != "198.51.100.20" {
		t.Fatalf("expected X-Real-IP fallback, got %q", got)
	}
}

// TestResolveIgnoresCDNHeaderWithoutFallback 确保被门控的头不会退化成「采信」，而是回退对端。
func TestResolveIgnoresCDNHeaderWithoutFallback(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, TrustPrivate: true})
	got := r.Resolve("10.0.0.1", func(h string) string {
		if h == "CF-Connecting-IP" {
			return "1.1.1.1"
		}
		return ""
	})
	if got != "10.0.0.1" {
		t.Fatalf("expected peer fallback, got %q", got)
	}
}

// TestResolveSkipsProxyAddrInSingleValueHeader 覆盖单值头的「不是代理地址」条件：
// X-Real-IP 里写的是代理自身地址时应跳过，继续看 XFF 链。
func TestResolveSkipsProxyAddrInSingleValueHeader(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, Proxies: []string{"10.0.0.1"}})
	got := r.Resolve("10.0.0.1", func(h string) string {
		switch h {
		case headerXRealIP:
			return "10.0.0.1" // 代理自身地址，不能当客户端
		case headerXForwardedFor:
			return "198.51.100.20, 10.0.0.1"
		}
		return ""
	})
	if got != "198.51.100.20" {
		t.Fatalf("expected XFF client after skipping proxy address, got %q", got)
	}
}

func TestResolveXRealIP(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, TrustLoopback: true})
	got := r.Resolve("127.0.0.1", func(h string) string {
		if h == "X-Real-IP" {
			return "198.51.100.20"
		}
		return ""
	})
	if got != "198.51.100.20" {
		t.Fatalf("expected X-Real-IP, got %q", got)
	}
}

func TestResolveSpoofedHeaderFromUntrustedRemote(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, TrustPrivate: true})
	// 公网客户端伪造 CF / XFF，对端不在信任列表 → 忽略头。
	got := r.Resolve("203.0.113.80", func(h string) string {
		switch h {
		case "CF-Connecting-IP":
			return "1.1.1.1"
		case headerXForwardedFor:
			return "8.8.8.8"
		}
		return ""
	})
	if got != "203.0.113.80" {
		t.Fatalf("expected ignore spoofed headers, got %q", got)
	}
}

func TestResolveExplicitProxyCIDR(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, Proxies: []string{"203.0.113.0/24"}})
	got := r.Resolve("203.0.113.10", func(h string) string {
		if h == headerXForwardedFor {
			return "198.51.100.1, 203.0.113.10"
		}
		return ""
	})
	if got != "198.51.100.1" {
		t.Fatalf("expected client behind trusted CIDR, got %q", got)
	}
}

// TestResolvePrivateClientBehindTrustedPrivatePeer 覆盖 D2：
// 可信私网对端 + 链上单个内网客户端时，必须返回该内网客户端，
// 而不是把它当代理剥掉后回退成对端地址。
func TestResolvePrivateClientBehindTrustedPrivatePeer(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, TrustPrivate: true})
	got := r.Resolve("172.18.0.5", func(h string) string {
		if h == headerXForwardedFor {
			return "192.168.160.20"
		}
		return ""
	})
	if got != "192.168.160.20" {
		t.Fatalf("expected private LAN client, got %q", got)
	}
}

// TestResolvePrivateChainEntryNotStrippedByTrustPrivate 明确 TrustPrivate 只作用于对端：
// 链条目是私网且非显式代理时，取最右侧那个私网地址，不继续往左剥。
func TestResolvePrivateChainEntryNotStrippedByTrustPrivate(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, TrustPrivate: true})
	got := r.Resolve("10.0.0.1", func(h string) string {
		if h == headerXForwardedFor {
			return "192.168.1.10, 10.0.0.2"
		}
		return ""
	})
	if got != "10.0.0.2" {
		t.Fatalf("expected rightmost private chain entry, got %q", got)
	}
}

// TestResolvePrivateChainEntryReturnedAsClient 重写旧用例：整条链都是私网时，
// 新语义返回链条目（最右侧非代理地址），而不是回退成对端地址。
func TestResolvePrivateChainEntryReturnedAsClient(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, TrustPrivate: true})
	got := r.Resolve("10.0.0.1", func(h string) string {
		if h == headerXForwardedFor {
			return "10.0.0.3, 10.0.0.2"
		}
		return ""
	})
	if got != "10.0.0.2" {
		t.Fatalf("expected rightmost chain entry as client, got %q", got)
	}
}

func TestResolveIPv6(t *testing.T) {
	r := NewResolver(Config{TrustProxy: true, TrustLoopback: true})
	got := r.Resolve("::1", func(h string) string {
		if h == headerXForwardedFor {
			return "2001:db8::1, ::1"
		}
		return ""
	})
	if got != "2001:db8::1" {
		t.Fatalf("expected IPv6 client, got %q", got)
	}
}

func TestMaskIPv4AndIPv6(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":         "1.2.3.*",
		"192.168.1.1":     "192.168.1.*",
		"2001:db8:1:2::3": "2001:db8:1:*",
		"":                "",
		"not-an-ip":       "",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize("  2001:0db8::0001  "); got != "2001:db8::1" {
		t.Fatalf("Normalize IPv6 = %q", got)
	}
	// 4-in-6 必须归一成 IPv4 文本，否则同一地址会有两种落库形式。
	if got := Normalize("::ffff:1.2.3.4"); got != "1.2.3.4" {
		t.Fatalf("Normalize 4-in-6 = %q, want 1.2.3.4", got)
	}
	if got := Normalize("not"); got != "" {
		t.Fatalf("Normalize invalid = %q", got)
	}
}

func TestConfigureDefault(t *testing.T) {
	// 恢复默认，避免污染其他测试包顺序。
	t.Cleanup(func() {
		Configure(Config{TrustProxy: true, TrustPrivate: true, TrustLoopback: true})
	})
	Configure(Config{TrustProxy: true, Proxies: []string{"10.0.0.0/8"}})
	got := FromCtx(nil)
	if got != "" {
		t.Fatalf("nil ctx should be empty, got %q", got)
	}
	r := Default()
	out := r.Resolve("10.1.2.3", func(h string) string {
		if h == "X-Real-IP" {
			return "203.0.113.1"
		}
		return ""
	})
	if out != "203.0.113.1" {
		t.Fatalf("configured default: got %q", out)
	}
}
