package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func testOAuth(t *testing.T, fake *FakeGitHub) *GitHubOAuth {
	t.Helper()
	return NewGitHubOAuth(GitHubConfig{
		ClientID:     fake.ClientID,
		ClientSecret: fake.ClientSecret,
		Endpoints:    fake.Endpoints(),
		HTTPTimeout:  3 * time.Second,
	}, fake.Server.Client())
}

func newAuthCall(operation string, input map[string]any) *pluginv2.IdentityProviderCall {
	doc, err := pluginv2.NewTypedDocument(schemaID("start.input@1"), input)
	if strings.Contains(operation, "complete") {
		doc, err = pluginv2.NewTypedDocument(schemaID("complete.input@1"), input)
	}
	if err != nil {
		panic(err)
	}
	return &pluginv2.IdentityProviderCall{
		Context: &protocolwire.RequestContext{
			RequestId: "test-request",
			Extension: &protocolwire.ExtensionIdentity{
				ExtensionId: extID, ExtensionVersion: "1.0.0",
			},
		},
		ID:              authProviderID,
		ContractVersion: authProviderID + "@1",
		Kind:            "auth",
		Handler:         handlerName,
		Operation:       operation,
		Input:           doc,
	}
}

func TestBuildAuthorizeURL_IncludesHostStatePKCEAndScopes(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	oauth := testOAuth(t, fake)

	verifier := "test-verifier-abcdefghijklmnopqrstuvwxyz012345"
	challenge := S256Challenge(verifier)
	callback := "https://forum.example.com/api/v1/auth/providers/sforum.auth-github.auth/callback"
	state := "host-owned-state-token"

	authURL, err := oauth.BuildAuthorizeURL(state, challenge, callback)
	if err != nil {
		t.Fatalf("BuildAuthorizeURL: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	if !strings.HasPrefix(authURL, fake.Endpoints().AuthURL) {
		t.Fatalf("auth URL host mismatch: %s", authURL)
	}
	q := parsed.Query()
	if q.Get("client_id") != fake.ClientID {
		t.Fatalf("client_id: %q", q.Get("client_id"))
	}
	if q.Get("state") != state {
		t.Fatalf("state: %q", q.Get("state"))
	}
	if q.Get("code_challenge") != challenge {
		t.Fatalf("code_challenge: %q", q.Get("code_challenge"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method: %q", q.Get("code_challenge_method"))
	}
	if q.Get("redirect_uri") != callback {
		t.Fatalf("redirect_uri: %q", q.Get("redirect_uri"))
	}
	scope := q.Get("scope")
	if !strings.Contains(scope, "read:user") || !strings.Contains(scope, "user:email") {
		t.Fatalf("scope missing required entries: %q", scope)
	}

	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatalf("GET authorize: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize status: %d", resp.StatusCode)
	}
	if fake.LastAuthorizeQuery.Get("state") != state {
		t.Fatalf("fake did not record state")
	}
}

func TestCompleteWithCode_SuccessLoginRegistrationLinkFields(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	oauth := testOAuth(t, fake)

	verifier := "pkce-verifier-success-path-0123456789abcdef"
	code := "auth-code-success"
	fake.IssueCode(code, verifier)
	callback := "https://forum.example.com/api/v1/auth/providers/sforum.auth-github.auth/callback"

	assertion, err := oauth.CompleteWithCode(context.Background(), code, verifier, callback)
	if err != nil {
		t.Fatalf("CompleteWithCode: %v", err)
	}
	// login / registration / link 共用同一断言字段。
	if assertion.ProviderSubject != "424242" {
		t.Fatalf("providerSubject: %q", assertion.ProviderSubject)
	}
	if assertion.DisplayName != "The Octocat" {
		t.Fatalf("displayName: %q", assertion.DisplayName)
	}
	if assertion.UsernameHint != "octocat" {
		t.Fatalf("usernameHint: %q", assertion.UsernameHint)
	}
	if assertion.EmailHint != "octocat@example.com" {
		t.Fatalf("emailHint: %q", assertion.EmailHint)
	}
	if !assertion.EmailVerified {
		t.Fatal("primary verified email must be marked verified")
	}
	if fake.TokenExchanges != 1 || fake.UserFetches != 1 || fake.EmailFetches != 1 {
		t.Fatalf("fetch counts token=%d user=%d email=%d", fake.TokenExchanges, fake.UserFetches, fake.EmailFetches)
	}
	// 重放 code 必须失败。
	if _, err := oauth.CompleteWithCode(context.Background(), code, verifier, callback); err == nil {
		t.Fatal("expected replayed code to fail")
	}
}

func TestCompleteWithCode_DisplayNameFallsBackToLogin(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	fake.User.Name = ""
	fake.User.Login = "login-only"
	oauth := testOAuth(t, fake)
	fake.IssueCode("c1", "v1")
	assertion, err := oauth.CompleteWithCode(context.Background(), "c1", "v1", "https://forum.example.com/cb")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if assertion.DisplayName != "login-only" {
		t.Fatalf("displayName: %q", assertion.DisplayName)
	}
}

func TestCompleteWithCode_TokenError(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	fake.TokenError = "bad_verification_code"
	oauth := testOAuth(t, fake)
	fake.IssueCode("c-bad", "v")
	_, err := oauth.CompleteWithCode(context.Background(), "c-bad", "v", "https://forum.example.com/cb")
	if err == nil {
		t.Fatal("expected token error")
	}
	if pub, ok := err.(*ErrPublic); !ok || !strings.Contains(pub.Reason, "github.token") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), fake.ClientSecret) || strings.Contains(err.Error(), "gho_") {
		t.Fatalf("secret leaked in error: %v", err)
	}
}

func TestCompleteWithCode_PKCEVerifierMismatch(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	oauth := testOAuth(t, fake)
	fake.IssueCode("c-pkce", "correct-verifier")
	_, err := oauth.CompleteWithCode(context.Background(), "c-pkce", "wrong-verifier", "https://forum.example.com/cb")
	if err == nil {
		t.Fatal("expected PKCE mismatch failure")
	}
}

func TestCompleteWithCode_InvalidUserJSON(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	fake.UserBody = "not-json"
	oauth := testOAuth(t, fake)
	fake.IssueCode("c-user", "v")
	_, err := oauth.CompleteWithCode(context.Background(), "c-user", "v", "https://forum.example.com/cb")
	if err == nil {
		t.Fatal("expected malformed user failure")
	}
	if pub, ok := err.(*ErrPublic); !ok || pub.Reason != "github.user_malformed" {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCompleteWithCode_SubjectMissing(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	fake.User.ID = 0
	oauth := testOAuth(t, fake)
	fake.IssueCode("c-sub", "v")
	_, err := oauth.CompleteWithCode(context.Background(), "c-sub", "v", "https://forum.example.com/cb")
	if err == nil {
		t.Fatal("expected subject missing failure")
	}
	if pub, ok := err.(*ErrPublic); !ok || pub.Reason != "github.subject_missing" {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCompleteWithCode_MalformedEmails(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	fake.EmailsBody = "{bad"
	oauth := testOAuth(t, fake)
	fake.IssueCode("c-em", "v")
	_, err := oauth.CompleteWithCode(context.Background(), "c-em", "v", "https://forum.example.com/cb")
	if err == nil {
		t.Fatal("expected email malformed failure")
	}
	if pub, ok := err.(*ErrPublic); !ok || pub.Reason != "github.email_malformed" {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCompleteWithCode_EmailUnavailableSoft(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	fake.EmailsHTTPStatus = http.StatusNotFound
	oauth := testOAuth(t, fake)
	fake.IssueCode("c-en", "v")
	assertion, err := oauth.CompleteWithCode(context.Background(), "c-en", "v", "https://forum.example.com/cb")
	if err != nil {
		t.Fatalf("email unavailable should soft-fail: %v", err)
	}
	if assertion.ProviderSubject != "424242" {
		t.Fatalf("subject: %q", assertion.ProviderSubject)
	}
	if assertion.EmailHint != "" {
		t.Fatalf("emailHint should be empty, got %q", assertion.EmailHint)
	}
	if assertion.EmailVerified {
		t.Fatal("missing email must not be marked verified")
	}
}

func TestCompleteWithCode_RateLimited(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	fake.RateLimitUser = true
	oauth := testOAuth(t, fake)
	fake.IssueCode("c-rl", "v")
	_, err := oauth.CompleteWithCode(context.Background(), "c-rl", "v", "https://forum.example.com/cb")
	if err == nil {
		t.Fatal("expected rate limit failure")
	}
	if pub, ok := err.(*ErrPublic); !ok || pub.Reason != "github.rate_limited" {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCompleteWithCode_Timeout(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	fake.SlowToken = 200 * time.Millisecond
	oauth := NewGitHubOAuth(GitHubConfig{
		ClientID:     fake.ClientID,
		ClientSecret: fake.ClientSecret,
		Endpoints:    fake.Endpoints(),
		HTTPTimeout:  30 * time.Millisecond,
	}, &http.Client{Timeout: 30 * time.Millisecond})
	fake.IssueCode("c-to", "v")
	_, err := oauth.CompleteWithCode(context.Background(), "c-to", "v", "https://forum.example.com/cb")
	if err == nil {
		t.Fatal("expected timeout failure")
	}
}

func TestCompleteWithCode_MissingCredentials(t *testing.T) {
	oauth := NewGitHubOAuth(GitHubConfig{}, nil)
	_, err := oauth.CompleteWithCode(context.Background(), "c", "v", "https://forum.example.com/cb")
	if err == nil {
		t.Fatal("expected credentials missing")
	}
}

func TestBuildAuthorizeURL_RejectsRelativeCallback(t *testing.T) {
	oauth := NewGitHubOAuth(GitHubConfig{
		ClientID: "id", ClientSecret: "secret",
		Endpoints: OfficialGitHubEndpoints(),
	}, nil)
	_, err := oauth.BuildAuthorizeURL("state", "challenge", "/relative/callback")
	if err == nil {
		t.Fatal("expected relative callback rejection")
	}
}

func TestProbe_CredentialsAndReachability(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	oauth := testOAuth(t, fake)

	result := oauth.Probe(context.Background())
	if !result.OK {
		t.Fatalf("probe should succeed: %#v", result)
	}
	if !strings.Contains(result.Message, "cannot be proven") {
		t.Fatalf("probe must not claim secret proof: %q", result.Message)
	}

	oauth.Config.ClientSecret = ""
	result = oauth.Probe(context.Background())
	if result.OK || result.Reason != "github.client_secret_missing" {
		t.Fatalf("expected secret missing: %#v", result)
	}

	oauth.Config.ClientSecret = fake.ClientSecret
	fake.APIRootBody = "not-json"
	result = oauth.Probe(context.Background())
	if result.OK || result.Reason != "github.response_malformed" {
		t.Fatalf("expected malformed: %#v", result)
	}
}

func TestRedactSensitive_StripsTokensAndSecrets(t *testing.T) {
	in := `token exchange failed: access_token=gho_ABCDEF0123456789 client_secret=supersecret code_verifier=abc123`
	out := RedactSensitive(in)
	if strings.Contains(out, "gho_ABCDEF") || strings.Contains(out, "supersecret") || strings.Contains(out, "abc123") {
		t.Fatalf("not redacted: %q", out)
	}
}

func TestHandleStartAndComplete_ViaIdentityHandlers(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	oauth := testOAuth(t, fake)
	if _, err := newGitHubIdentityRegistry(oauth); err != nil {
		t.Fatalf("registry: %v", err)
	}

	opsStart := []string{"login.start", "registration.start", "link.start"}
	opsComplete := []string{"login.complete", "registration.complete", "link.complete"}
	verifier := "registry-path-verifier-0123456789"
	challenge := S256Challenge(verifier)
	callback := "https://forum.example.com/api/v1/auth/providers/sforum.auth-github.auth/callback"

	for _, opName := range opsStart {
		doc, err := handleAuthOperation(context.Background(), oauth, newAuthCall(opName, map[string]any{
			"correlationId": "corr-" + opName,
			"state":         "state-" + opName,
			"codeChallenge": challenge,
			"callbackUrl":   callback,
		}))
		if err != nil {
			t.Fatalf("%s: %v", opName, err)
		}
		values := pluginv2.TypedDocumentValues(doc)
		if values["status"] != "redirect" {
			t.Fatalf("%s status: %#v", opName, values)
		}
		redirect, _ := values["redirectUrl"].(string)
		if !strings.Contains(redirect, challenge) {
			t.Fatalf("%s redirect missing challenge: %s", opName, redirect)
		}
	}

	for _, opName := range opsComplete {
		code := "code-" + opName
		fake.IssueCode(code, verifier)
		doc, err := handleAuthOperation(context.Background(), oauth, newAuthCall(opName, map[string]any{
			"correlationId":   "corr-complete-" + opName,
			"completionToken": code,
			"codeVerifier":    verifier,
			"callbackUrl":     callback,
		}))
		if err != nil {
			t.Fatalf("%s: %v", opName, err)
		}
		values := pluginv2.TypedDocumentValues(doc)
		if values["providerSubject"] != "424242" {
			t.Fatalf("%s subject: %#v", opName, values)
		}
		if values["displayName"] != "The Octocat" {
			t.Fatalf("%s displayName: %#v", opName, values)
		}
		if values["usernameHint"] != "octocat" {
			t.Fatalf("%s usernameHint: %#v", opName, values)
		}
		if values["emailHint"] != "octocat@example.com" {
			t.Fatalf("%s emailHint: %#v", opName, values)
		}
		if values["emailVerified"] != true {
			t.Fatalf("%s emailVerified: %#v", opName, values)
		}
		if _, hasDigest := values["providerSubjectDigest"]; hasDigest {
			t.Fatalf("%s must not emit digest", opName)
		}
	}
}

func TestHandleStart_RejectsNonS256Method(t *testing.T) {
	fake := NewFakeGitHub()
	defer fake.Close()
	oauth := testOAuth(t, fake)
	_, err := handleAuthOperation(context.Background(), oauth, newAuthCall("login.start", map[string]any{
		"correlationId":       "corr",
		"state":               "state",
		"codeChallenge":       "challenge",
		"codeChallengeMethod": "plain",
		"callbackUrl":         "https://forum.example.com/cb",
	}))
	if err == nil {
		t.Fatal("expected plain PKCE rejection")
	}
}
