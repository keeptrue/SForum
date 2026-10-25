package extensionsruntime

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
	pluginv2 "github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	"golang.org/x/net/http/httpguts"
)

var ErrProtocolV2RequestAuthorityInvalid = errors.New("protocol v2 request authority is invalid")

type ProtocolV2RequestAuthorityMode string

const (
	ProtocolV2RequestAuthorityFiltered ProtocolV2RequestAuthorityMode = "filtered"
	ProtocolV2RequestAuthorityRaw      ProtocolV2RequestAuthorityMode = "raw"
)

type ProtocolV2RequestGuardKind string

const (
	ProtocolV2RequestGuardHost       ProtocolV2RequestGuardKind = "host"
	ProtocolV2RequestGuardCustom     ProtocolV2RequestGuardKind = "custom"
	ProtocolV2RequestGuardRawRequest ProtocolV2RequestGuardKind = "raw_request"
)

type ProtocolV2RequestAuthority struct {
	Mode      ProtocolV2RequestAuthorityMode
	GuardKind ProtocolV2RequestGuardKind
}

func validateProtocolV2RequestAuthority(authority ProtocolV2RequestAuthority) error {
	switch authority.Mode {
	case ProtocolV2RequestAuthorityFiltered, ProtocolV2RequestAuthorityRaw:
	default:
		return ErrProtocolV2RequestAuthorityInvalid
	}
	switch authority.GuardKind {
	case ProtocolV2RequestGuardHost, ProtocolV2RequestGuardCustom, ProtocolV2RequestGuardRawRequest:
	default:
		return ErrProtocolV2RequestAuthorityInvalid
	}
	if authority.Mode == ProtocolV2RequestAuthorityRaw && authority.GuardKind != ProtocolV2RequestGuardRawRequest ||
		authority.Mode != ProtocolV2RequestAuthorityRaw && authority.GuardKind == ProtocolV2RequestGuardRawRequest {
		return ErrProtocolV2RequestAuthorityInvalid
	}
	return nil
}

func (c *protocolV2Client) validateFrozenRouteAuthority(
	route extensions.ManifestRoute,
	authority ProtocolV2RequestAuthority,
) error {
	if err := validateProtocolV2RequestAuthority(authority); err != nil {
		return err
	}
	expected, err := c.frozenRouteAuthority(route)
	if err != nil || authority != expected {
		return ErrProtocolV2RequestAuthorityInvalid
	}
	return nil
}

func (c *protocolV2Client) frozenRouteAuthority(
	route extensions.ManifestRoute,
) (ProtocolV2RequestAuthority, error) {
	return ProtocolV2RouteRequestAuthority(route, c.guards)
}

// ProtocolV2RouteRequestAuthority 返回一条清单路由在 Protocol V2 线路上冻结的请求权威。
//
// 宿主侧所有 V2 入口（声明路由派发、legacy 代理前缀回落）必须用这一份实现推导：
// 运行时会用同一份清单重算并逐字段比对，推导结果不一致的调用只会被
// ErrProtocolV2RequestAuthorityInvalid 拒绝，而不是被悄悄降级成另一种权威。
func ProtocolV2RouteRequestAuthority(
	route extensions.ManifestRoute,
	guards []extensions.ManifestGuard,
) (ProtocolV2RequestAuthority, error) {
	switch route.Guard {
	case extensionmanifest.GuardCorePublic, extensionmanifest.GuardCoreLogin,
		extensionmanifest.GuardCorePermission, extensionmanifest.GuardCoreGuest,
		extensionmanifest.GuardCoreInherit:
		return ProtocolV2RequestAuthority{
			Mode: ProtocolV2RequestAuthorityFiltered, GuardKind: ProtocolV2RequestGuardHost,
		}, nil
	case extensionmanifest.GuardCoreRaw:
		// 决定 R1（2026-10-16）：该 Host 守卫不再授予 raw 权限（此前零守卫代码即可拿到
		// 凭证转发）。清单校验已拒绝它；运行时再 fail closed，避免旧制品继续生效。
		return ProtocolV2RequestAuthority{}, ErrProtocolV2RequestAuthorityInvalid
	}
	var matched *extensions.ManifestGuard
	for index := range guards {
		candidate := &guards[index]
		if candidate.ID != route.Guard {
			continue
		}
		if matched != nil {
			return ProtocolV2RequestAuthority{}, ErrProtocolV2RequestAuthorityInvalid
		}
		matched = candidate
	}
	if matched == nil {
		return ProtocolV2RequestAuthority{}, ErrProtocolV2RequestAuthorityInvalid
	}
	switch matched.Kind {
	case "custom":
		return ProtocolV2RequestAuthority{
			Mode: ProtocolV2RequestAuthorityFiltered, GuardKind: ProtocolV2RequestGuardCustom,
		}, nil
	case "raw_request":
		return ProtocolV2RequestAuthority{
			Mode: ProtocolV2RequestAuthorityRaw, GuardKind: ProtocolV2RequestGuardRawRequest,
		}, nil
	default:
		return ProtocolV2RequestAuthority{}, ErrProtocolV2RequestAuthorityInvalid
	}
}

func protocolV2AuthorizedRequestHeaders(
	source http.Header,
	authority ProtocolV2RequestAuthority,
) (http.Header, error) {
	if err := validateProtocolV2RequestAuthority(authority); err != nil {
		return nil, err
	}
	connectionHeaders := protocolV2ConnectionHeaderTokens(source)
	result := make(http.Header)
	for name, values := range source {
		canonical := strings.ToLower(strings.TrimSpace(name))
		if !httpguts.ValidHeaderFieldName(name) {
			return nil, ErrProtocolV2RequestAuthorityInvalid
		}
		if strings.HasPrefix(canonical, "x-sforum-") || protocolV2RequestHeaderBlocked(canonical, connectionHeaders) {
			continue
		}
		if protocolV2RequestCredentialHeader(canonical) && authority.Mode != ProtocolV2RequestAuthorityRaw {
			continue
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return nil, ErrProtocolV2RequestAuthorityInvalid
			}
			result.Add(name, value)
		}
	}
	return result, nil
}

func protocolV2RequestCredentialHeader(canonical string) bool {
	switch canonical {
	case "cookie", "authorization", "x-api-key", "x-auth-token":
		return true
	default:
		return false
	}
}

func protocolV2RequestHeaderBlocked(canonical string, connectionHeaders map[string]struct{}) bool {
	if _, blocked := connectionHeaders[canonical]; blocked {
		return true
	}
	switch canonical {
	case "", "host", "content-length", "proxy-authorization", "x-csrf-token",
		"connection", "keep-alive", "proxy-authenticate", "proxy-connection",
		"te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func protocolV2ConnectionHeaderTokens(headers http.Header) map[string]struct{} {
	blocked := make(map[string]struct{})
	for name, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(name), "Connection") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				if canonical := strings.ToLower(strings.TrimSpace(token)); canonical != "" {
					blocked[canonical] = struct{}{}
				}
			}
		}
	}
	return blocked
}

func protocolV2WireRequestAuthority(
	authority ProtocolV2RequestAuthority,
) (pluginv2.RouteRequestAuthorityMode, pluginv2.RouteGuardKind, error) {
	if err := validateProtocolV2RequestAuthority(authority); err != nil {
		return pluginv2.RouteRequestAuthorityMode_ROUTE_REQUEST_AUTHORITY_MODE_UNSPECIFIED,
			pluginv2.RouteGuardKind_ROUTE_GUARD_KIND_UNSPECIFIED, err
	}
	mode := pluginv2.RouteRequestAuthorityMode_ROUTE_REQUEST_AUTHORITY_MODE_FILTERED
	if authority.Mode == ProtocolV2RequestAuthorityRaw {
		mode = pluginv2.RouteRequestAuthorityMode_ROUTE_REQUEST_AUTHORITY_MODE_RAW
	}
	var guardKind pluginv2.RouteGuardKind
	switch authority.GuardKind {
	case ProtocolV2RequestGuardHost:
		guardKind = pluginv2.RouteGuardKind_ROUTE_GUARD_KIND_HOST
	case ProtocolV2RequestGuardCustom:
		guardKind = pluginv2.RouteGuardKind_ROUTE_GUARD_KIND_CUSTOM
	case ProtocolV2RequestGuardRawRequest:
		guardKind = pluginv2.RouteGuardKind_ROUTE_GUARD_KIND_RAW_REQUEST
	default:
		return pluginv2.RouteRequestAuthorityMode_ROUTE_REQUEST_AUTHORITY_MODE_UNSPECIFIED,
			pluginv2.RouteGuardKind_ROUTE_GUARD_KIND_UNSPECIFIED, ErrProtocolV2RequestAuthorityInvalid
	}
	return mode, guardKind, nil
}

func wrapProtocolV2AuthorityError(owner error, err error) error {
	return fmt.Errorf("%w: %w", owner, err)
}
