// Package clientip 从 HTTP 请求解析真实客户端 IP。
//
// 设计目标：在 CDN + 反向代理（Caddy/Nginx）+ Docker + Nuxt 反代多层链路下
// 仍能拿到真实 IP，同时防止客户端伪造 X-Forwarded-For。
//
// 规则：
//  1. 总开关 TrustProxy 关闭时不读任何转发头（零值即 fail-closed）；
//  2. 仅当 TCP 对端本身是可信代理（显式 Proxies / 私网 / loopback / link-local）时，才读取转发头；
//  3. 单值头分两组门控：CDN 头（CF-Connecting-IP / True-Client-IP）只在对端属于显式 Proxies 时采信；
//     X-Real-IP 在对端可信时采信（官方 Caddyfile 会覆盖它）；候选值都不能是代理地址；
//  4. 再处理 X-Forwarded-For：从右往左跳过代理地址，取第一个非代理地址；
//  5. 全部失败则回退 TCP 对端。
//
// CDN 头单独收紧的原因：它们没有中间代理替我们覆盖，任何客户端都能直接写。
// 打开 TRUST_PROXY_LOOPBACK 时（如 WebSocket 直连 :18080），对端是 loopback 但并非
// 我们的边缘代理，若只看「对端可信」就会被伪造。CDN 场景应由 Caddy 的
// trusted_proxies + {client_ip} 把真实客户端写进 X-Real-IP / X-Forwarded-For。
//
// 关键区分：判断「TCP 对端」与判断「链条目」用的是两套集合。
// 链条目判定（isProxyAddr）故意不含 RFC1918 私网：链上的私网地址更可能是内网客户端，
// 而不是代理；把私网当代理剥掉，会让内网客户端回退成服务端跳数地址。
//
// 业务代码应统一走 FromCtx，不要散落 c.IP()。
package clientip

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v3"
)

// cdnClientHeaders 是 CDN 写入的客户端 IP 头。它们没有中间代理替我们覆盖，
// 任何客户端都能直接写，因此只在对端落在显式 Proxies 里时采信。
var cdnClientHeaders = []string{
	"CF-Connecting-IP", // Cloudflare
	"True-Client-IP",   // Akamai / 部分企业 CDN
}

const (
	// headerXRealIP 由官方 Caddyfile 覆盖写，对端可信即可采信。
	headerXRealIP       = "X-Real-IP"
	headerXForwardedFor = "X-Forwarded-For"
)

// Config 控制哪些来源的转发头可信。
type Config struct {
	// TrustProxy 是总开关：关闭时不读任何转发头，只返回 TCP 对端。
	// 零值即 fail-closed，避免漏配时把可伪造的头当客户端 IP。
	TrustProxy bool
	// Proxies 是显式信任的代理 IP 或 CIDR（如 "10.0.0.1", "172.18.0.0/16"）。
	Proxies []string
	// TrustPrivate 信任 RFC1918 / ULA 私网（Docker 网桥常用）。
	TrustPrivate bool
	// TrustLoopback 信任 127.0.0.0/8 与 ::1。
	TrustLoopback bool
	// TrustLinkLocal 信任链路本地地址（一般关闭）。
	TrustLinkLocal bool
}

// Resolver 根据配置解析客户端 IP。
type Resolver struct {
	exact      map[netip.Addr]struct{}
	nets       []netip.Prefix
	trustProxy bool
	private    bool
	loopback   bool
	linkLocal  bool
}

var (
	mu       sync.RWMutex
	defaultR = NewResolver(Config{TrustProxy: true, TrustPrivate: true, TrustLoopback: true})
)

// Configure 设置进程级默认解析器（bootstrap / NewApp 时调用一次即可）。
func Configure(cfg Config) {
	mu.Lock()
	defaultR = NewResolver(cfg)
	mu.Unlock()
}

// Default 返回当前默认解析器。
func Default() *Resolver {
	mu.RLock()
	defer mu.RUnlock()
	return defaultR
}

// NewResolver 编译信任列表；非法 CIDR/IP 会被静默忽略。
func NewResolver(cfg Config) *Resolver {
	r := &Resolver{
		exact:      make(map[netip.Addr]struct{}),
		trustProxy: cfg.TrustProxy,
		private:    cfg.TrustPrivate,
		loopback:   cfg.TrustLoopback,
		linkLocal:  cfg.TrustLinkLocal,
	}
	for _, raw := range cfg.Proxies {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				continue
			}
			r.nets = append(r.nets, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			continue
		}
		r.exact[addr] = struct{}{}
	}
	return r
}

// FromCtx 使用默认解析器从 Fiber 上下文取真实客户端 IP。
func FromCtx(c fiber.Ctx) string {
	return Default().FromCtx(c)
}

// FromCtx 从 Fiber 上下文解析真实客户端 IP。
func (r *Resolver) FromCtx(c fiber.Ctx) string {
	if c == nil {
		return ""
	}
	remote := remoteIPString(c)
	return r.Resolve(remote, func(name string) string {
		return c.Get(name)
	})
}

// Resolve 纯函数入口，便于单测：remote 为 TCP 对端，getHeader 读取请求头。
func (r *Resolver) Resolve(remote string, getHeader func(string) string) string {
	if r == nil {
		r = Default()
	}
	remoteAddr, remoteOK := parseAddr(remote)
	if !remoteOK {
		return ""
	}

	// 总开关关闭：转发头一概不读，避免漏配时把可伪造的头当客户端 IP。
	if !r.trustProxy {
		return remoteAddr.String()
	}

	// 对端不是可信代理：绝不采信可伪造的转发头。
	if !r.isTrustedPeer(remoteAddr) {
		return remoteAddr.String()
	}

	if getHeader != nil {
		// 1) CDN 头：只有对端本身是显式配置的代理才采信。
		//    这两个头可由任意客户端写入，信任「整类私网/loopback」不足以采信它们。
		if r.isExplicitProxy(remoteAddr) {
			for _, name := range cdnClientHeaders {
				if ip, ok := parseAddr(getHeader(name)); ok && !r.isProxyAddr(ip) {
					return ip.String()
				}
			}
		}

		// 2) X-Real-IP：官方 Caddyfile 会覆盖成真实客户端，对端可信即可采信。
		if ip, ok := parseAddr(getHeader(headerXRealIP)); ok && !r.isProxyAddr(ip) {
			return ip.String()
		}

		// 3) X-Forwarded-For：从右往左跳过代理地址。
		if client, ok := r.clientFromXFF(getHeader(headerXForwardedFor)); ok {
			return client.String()
		}
	}

	return remoteAddr.String()
}

// clientFromXFF 按「右→左」跳过代理地址，返回链中最右侧的非代理地址。
// 例：client, proxy1, proxy2（最右为最近一跳）→ 跳过 proxy2/proxy1 → client。
func (r *Resolver) clientFromXFF(raw string) (netip.Addr, bool) {
	parts := strings.Split(raw, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip, ok := parseAddr(parts[i])
		if !ok {
			continue
		}
		if r.isProxyAddr(ip) {
			continue
		}
		return ip, true
	}
	return netip.Addr{}, false
}

// isTrustedPeer 判断 TCP 对端是否是可信代理：显式 Proxies ∪ 私网 ∪ loopback ∪ link-local。
// 只用于对端，因为只有对端地址无法被请求方伪造。
func (r *Resolver) isTrustedPeer(addr netip.Addr) bool {
	if r == nil {
		return false
	}
	if r.isExplicitProxy(addr) {
		return true
	}
	if r.loopback && addr.IsLoopback() {
		return true
	}
	if r.private && addr.IsPrivate() {
		return true
	}
	if r.linkLocal && (addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()) {
		return true
	}
	return false
}

// isProxyAddr 判断转发链条目本身是否是代理地址：显式 Proxies ∪ loopback ∪ link-local。
// 故意不含 RFC1918：链上的私网地址更可能是内网客户端而不是代理，
// 剥掉它会让内网客户端回退成服务端跳数地址。
func (r *Resolver) isProxyAddr(addr netip.Addr) bool {
	if r == nil {
		return false
	}
	if r.isExplicitProxy(addr) {
		return true
	}
	if r.loopback && addr.IsLoopback() {
		return true
	}
	if r.linkLocal && (addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()) {
		return true
	}
	return false
}

// isExplicitProxy 只匹配显式 Proxies（exact 或 CIDR），不含私网/loopback/link-local 整类。
func (r *Resolver) isExplicitProxy(addr netip.Addr) bool {
	if r == nil {
		return false
	}
	if _, ok := r.exact[addr]; ok {
		return true
	}
	for _, prefix := range r.nets {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Normalize 把任意 IP 文本规范为 netip 字符串形式；非法则返回空串。
// Unmap 让 IPv4-mapped IPv6（::ffff:1.2.3.4）与 1.2.3.4 在库内只有一种文本，
// 否则同一地址会以两种形式落库。parseAddr 与信任判定保持原样，避免改变 exact 匹配行为。
func Normalize(ip string) string {
	addr, ok := parseAddr(ip)
	if !ok {
		return ""
	}
	return addr.Unmap().String()
}

// Mask 生成展示用脱敏前缀。
// IPv4：保留前 3 段（1.2.3.*）；IPv6：保留 /48 前缀（2001:db8:1:*）。
// 非法输入返回空串。
func Mask(ip string) string {
	addr, ok := parseAddr(ip)
	if !ok {
		return ""
	}
	if v4 := addr.AsSlice(); addr.Is4() || addr.Is4In6() {
		if len(v4) == 16 {
			// IPv4-mapped IPv6：取末 4 字节
			v4 = v4[12:]
		}
		if len(v4) != 4 {
			return ""
		}
		return fmt.Sprintf("%d.%d.%d.*", v4[0], v4[1], v4[2])
	}
	// IPv6：展示 /48 网络前缀 + :*
	prefix, err := addr.Prefix(48)
	if err != nil {
		return ""
	}
	// 取前 3 个 hextet（6 字节）
	b := prefix.Addr().As16()
	return fmt.Sprintf("%x:%x:%x:*",
		uint16(b[0])<<8|uint16(b[1]),
		uint16(b[2])<<8|uint16(b[3]),
		uint16(b[4])<<8|uint16(b[5]),
	)
}

func parseAddr(raw string) (netip.Addr, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Addr{}, false
	}
	// 去掉可能附带的端口（少见，但 XFF 偶发 host:port）
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	// 方括号 IPv6
	raw = strings.Trim(raw, "[]")
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}

func remoteIPString(c fiber.Ctx) string {
	// 直接取 TCP 对端，避免在配置 TrustProxy 前被 Fiber 的 c.IP() 混入未校验头。
	if ip := c.RequestCtx().RemoteIP(); ip != nil {
		return ip.String()
	}
	// 回退：部分测试 mock 可能只填 c.IP()
	return strings.TrimSpace(c.IP())
}
