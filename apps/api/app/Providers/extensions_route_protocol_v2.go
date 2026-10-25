package providers

import (
	"bytes"
	"encoding/json"
	stdhttp "net/http"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// 本文件只做一件事：把 Fiber 侧的 HTTP 请求原语（header / query / body）转换成
// Protocol V2 派发需要的传输字段。它刻意不引用 Support/Extensions 的类型，
// 这样「legacy 代理回落」的运行时装配仍留在 extensions.go（架构闸只允许既有文件
// import 那个 legacy 包），而纯转换逻辑可以独立演进与测试。

// protocolV2RouteRequestHeaders 复制原始请求头。凭证与 x-sforum-* 的过滤由运行时
// 按请求权威统一执行（protocolV2AuthorizedRequestHeaders），宿主不在这里重写规则。
func protocolV2RouteRequestHeaders(c fiber.Ctx) stdhttp.Header {
	headers := make(stdhttp.Header)
	for name, value := range c.Request().Header.All() {
		headers.Add(string(name), string(value))
	}
	return headers
}

func protocolV2RouteQuery(raw string) (map[string]string, map[string][]string, error) {
	if raw == "" {
		return nil, nil, nil
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, nil, fiber.NewError(fiber.StatusBadRequest, "validation.invalid")
	}
	legacy := make(map[string]string, len(values))
	lossless := make(map[string][]string, len(values))
	for key, items := range values {
		if len(items) == 0 {
			return nil, nil, fiber.NewError(fiber.StatusBadRequest, "validation.invalid")
		}
		legacy[key] = items[0]
		lossless[key] = append([]string(nil), items...)
	}
	return legacy, lossless, nil
}

// protocolV2RouteBody 把 HTTP body 转成 V2 的类型化文档。没有声明 requestSchema
// 就没有可校验的形状：拒绝而不是把未声明的 JSON 塞进调用（声明路由派发同样拒绝）。
func protocolV2RouteBody(schema string, raw []byte) (map[string]any, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	if strings.TrimSpace(schema) == "" {
		return nil, false, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	var body map[string]any
	if err := json.Unmarshal(trimmed, &body); err != nil || body == nil {
		return nil, false, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	return body, true, nil
}
