package identitycontroller

import (
	"bytes"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func registerTestUser(t *testing.T, app *fiber.App, username, email string) *nethttp.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"username": username,
		"email":    email,
		"password": "correct horse battery staple",
	})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("register %s: %v", username, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("register %s expected 201, got %d", username, resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatalf("register %s returned no session cookie", username)
	}
	return resp.Cookies()[0]
}

func createUserRequestWithCookie(t *testing.T, app *fiber.App, cookie *nethttp.Cookie, body map[string]any) *nethttp.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/users", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("create user request failed: %v", err)
	}
	return resp
}

func TestCreateAdminUserEndpointCreatesAccount(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerTestUser(t, app, "alice", "alice@example.com")
	promoteToUserManager(t, app, store, 1)

	resp := createUserRequestWithCookie(t, app, cookie, map[string]any{
		"username":    "bob",
		"email":       "bob@example.com",
		"displayName": "Bob",
		"password":    "correct horse battery staple",
		"roleKeys":    []string{"member"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var envelope struct {
		Data struct {
			User struct {
				Username string   `json:"username"`
				Email    string   `json:"email"`
				RoleKeys []string `json:"roleKeys"`
			} `json:"user"`
			GeneratedPassword string `json:"generatedPassword"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.User.Username != "bob" {
		t.Fatalf("unexpected created user: %+v", envelope.Data.User)
	}
	if len(envelope.Data.User.RoleKeys) != 1 || envelope.Data.User.RoleKeys[0] != "member" {
		t.Fatalf("unexpected roles: %+v", envelope.Data.User.RoleKeys)
	}
	if envelope.Data.GeneratedPassword != "" {
		t.Fatal("manual password mode must not return a generated password")
	}
}

func TestCreateAdminUserEndpointRejectsMember(t *testing.T) {
	app, _ := newSessionTestApp(t)
	registerTestUser(t, app, "alice", "alice@example.com")
	memberCookie := registerTestUser(t, app, "bob", "bob@example.com")

	resp := createUserRequestWithCookie(t, app, memberCookie, map[string]any{
		"username": "carol",
		"email":    "carol@example.com",
		"password": "correct horse battery staple",
	})
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestCreateAdminUserEndpointReturnsFieldConflicts(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerTestUser(t, app, "alice", "alice@example.com")
	promoteToUserManager(t, app, store, 1)

	first := createUserRequestWithCookie(t, app, cookie, map[string]any{
		"username": "bob",
		"email":    "bob@example.com",
		"password": "correct horse battery staple",
	})
	first.Body.Close()
	if first.StatusCode != nethttp.StatusCreated {
		t.Fatalf("first create expected 201, got %d", first.StatusCode)
	}

	second := createUserRequestWithCookie(t, app, cookie, map[string]any{
		"username": "bob",
		"email":    "bob@example.com",
		"password": "correct horse battery staple",
	})
	defer second.Body.Close()
	if second.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", second.StatusCode)
	}

	var envelope struct {
		Data struct {
			Fields map[string][]string `json:"fields"`
		} `json:"data"`
	}
	if err := json.NewDecoder(second.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Fields["username"]) == 0 || len(envelope.Data.Fields["email"]) == 0 {
		t.Fatalf("expected username and email field errors, got %+v", envelope.Data.Fields)
	}
}
