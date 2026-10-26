package http

import (
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gofiber/fiber/v3"

	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestWriteRouteDispatchResponsePreservesHostAuthorityHeaders(t *testing.T) {
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error {
		c.Append(fiber.HeaderSetCookie, "host_session=trusted")
		c.Append(fiber.HeaderVary, "Origin")
		c.Set("X-Request-ID", "host-request")
		hostHeaders := hostRouteMiddlewareResponseHeaders(c)

		writeRouteDispatchResponse(c, routes.DispatchResponse{
			Status: stdhttp.StatusCreated,
			Headers: stdhttp.Header{
				fiber.HeaderSetCookie: {"plugin_session=forged"},
				fiber.HeaderVary:      {"Accept-Encoding"},
				"X-Request-ID":        {"plugin-request"},
			},
			Body: []byte("plugin response"),
		}, hostHeaders)
		return nil
	})

	response, err := app.Test(httptest.NewRequest(stdhttp.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != stdhttp.StatusCreated || string(body) != "plugin response" {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}
	if got := response.Header.Values(fiber.HeaderSetCookie); len(got) != 1 || got[0] != "host_session=trusted" {
		t.Fatalf("Set-Cookie=%#v", got)
	}
	if got := response.Header.Get("X-Request-ID"); got != "host-request" {
		t.Fatalf("X-Request-ID=%q", got)
	}
	got := response.Header.Values(fiber.HeaderVary)
	slices.Sort(got)
	if len(got) != 2 || got[0] != "Accept-Encoding" || got[1] != "Origin" {
		t.Fatalf("Vary=%#v", got)
	}
}
