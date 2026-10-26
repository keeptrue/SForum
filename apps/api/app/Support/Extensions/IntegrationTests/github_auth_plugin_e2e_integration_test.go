package extensionsruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	githubAuthExtID      = "sforum.auth-github"
	githubAuthProviderID = "sforum.auth-github.auth"
	githubAuthSubject    = "424242"
)

// TestGitHubAuthPluginProtocolV2HeadlessHostSession is the M2B headless E2E:
// real Protocol V2 subprocess of sforum.auth-github + local fake GitHub →
// Host AuthProviderFlow complete → ExternalAuthService login / registration
// ticket / link session effects. No admin or public UI.
func TestGitHubAuthPluginProtocolV2HeadlessHostSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping github auth Protocol V2 E2E in short mode")
	}

	identity.ResetIdentitySubjectHMACKeyForTest()
	if err := identity.ConfigureIdentitySubjectHMAC(""); err != nil {
		t.Fatalf("configure subject hmac: %v", err)
	}
	t.Cleanup(identity.ResetIdentitySubjectHMACKeyForTest)

	fake := newHeadlessFakeGitHub()
	t.Cleanup(fake.Close)

	// 测试钩子：仅注入 fake 端点（生产不得设置；Host 白名单透传）。
	t.Setenv("SFORUM_AUTH_GITHUB_AUTH_URL", fake.AuthURL)
	t.Setenv("SFORUM_AUTH_GITHUB_TOKEN_URL", fake.TokenURL)
	t.Setenv("SFORUM_AUTH_GITHUB_API_URL", fake.APIURL)

	extension := buildGitHubAuthExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "github-auth-e2e", ImpactDigest: extension.PackageDigest,
		}},
		Settings: githubAuthPluginSettings{
			githubAuthExtID: {
				"client_id":     fake.ClientID,
				"client_secret": fake.ClientSecret,
			},
		},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatalf("start sforum.auth-github: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if extension.Manifest.Lifecycle == nil {
		t.Fatal("github built-in must declare Lifecycle V2 for exact disable")
	}
	lifecycleResult, err := starter.RunLifecycleInstance(t.Context(), active.Identity, extension, extensionsruntime.LifecycleInvocation{
		Action: extensionsruntime.LifecycleActionDisable, PlanVersion: extension.Manifest.Lifecycle.ContractVersion,
		StepID: "github-auth.lifecycle.disable",
	})
	if err != nil || lifecycleResult.State != extensionsruntime.LifecycleProgressSucceeded {
		t.Fatalf("github lifecycle disable acknowledgement = %#v, %v", lifecycleResult, err)
	}
	publication, err := extensionsruntime.BuildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: active.Identity.InstanceID,
	})
	if err != nil || publication == nil {
		t.Fatalf("lifecycle identity publication: %#v %v", publication, err)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatalf("publish identity: %v", err)
	}

	runtime, err := extensionsruntime.NewIdentityProviderRuntime(manager, registry)
	if err != nil {
		t.Fatal(err)
	}
	invoker, err := extensionsruntime.NewIdentitySessionEvaluateInvoker(runtime)
	if err != nil {
		t.Fatal(err)
	}
	authFlow, err := identity.NewAuthProviderFlow(
		identity.RegistryAuthProviderSource{Registry: registry},
		invoker,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	callbackURL := "https://forum.example.com/api/v1/auth/providers/" + githubAuthProviderID + "/callback"
	digest, err := identity.ComputeSubjectDigest(githubAuthProviderID, githubAuthSubject)
	if err != nil {
		t.Fatalf("subject digest: %v", err)
	}

	// --- login: Protocol V2 start/complete → Host CompleteLogin session user ---
	loginUser := identity.CurrentUser{
		ID: 11, Username: "linked-user",
		Status: identity.UserStatusActive, DisplayName: "Linked User",
	}
	linkStore := newHeadlessLinkStore()
	linkStore.seedActive(loginUser.ID, githubAuthProviderID, digest, extension.ID)
	recentAuth := newHeadlessRecentAuth()
	activation := &headlessActivationStore{items: map[string]identity.ProviderActivation{
		githubAuthProviderID: {
			ProviderID: githubAuthProviderID, OwnerExtensionID: extension.ID,
			OwnerPackageDigest: extension.PackageDigest,
			LoginEnabled:       true, RegistrationEnabled: true, LinkEnabled: true,
			Revision: 1,
		},
	}}
	extAuth := identity.NewExternalAuthService(identity.ExternalAuthDeps{
		LinkStore:        linkStore,
		ActivationStore:  activation,
		RecentAuth:       recentAuth,
		RecentAuthMarker: recentAuth,
		ProviderContribution: func(providerID string) (identityregistry.ProviderContribution, error) {
			return registry.ResolveProvider(providerID)
		},
		LoadCurrentUser: func(_ context.Context, userID int64) (identity.CurrentUser, error) {
			if userID == loginUser.ID {
				return loginUser, nil
			}
			if userID == 22 {
				return identity.CurrentUser{
					ID: 22, Username: "actor",
					Status: identity.UserStatusActive, DisplayName: "Actor",
				}, nil
			}
			return identity.CurrentUser{}, identity.ErrUserNotFound
		},
		AnyUserExists:       func(context.Context) (bool, error) { return true, nil },
		RegistrationEnabled: func(context.Context) (bool, error) { return true, nil },
	})

	// SyncBuiltins 默认不激活：清空 activation 时公开 catalog 为空。
	emptyActivation := &headlessActivationStore{items: map[string]identity.ProviderActivation{}}
	emptyCatalogSvc := identity.NewExternalAuthService(identity.ExternalAuthDeps{
		ActivationStore: emptyActivation,
	})
	catalog, err := emptyCatalogSvc.ListEffectivePublicCatalog(t.Context(), registry, "zh-CN")
	if err != nil {
		t.Fatalf("empty activation catalog: %v", err)
	}
	for _, entry := range catalog {
		if entry.ProviderID == githubAuthProviderID {
			t.Fatalf("github must not appear in public catalog without Host activation: %#v", entry)
		}
	}

	loginAssertion := runGitHubOAuthRoundTrip(t, fake, authFlow, identity.AuthOperationLoginStart, identity.AuthOperationLoginComplete, 0, callbackURL, "login-corr")
	if loginAssertion.ProviderSubject != githubAuthSubject {
		t.Fatalf("login subject = %q want %q", loginAssertion.ProviderSubject, githubAuthSubject)
	}
	if loginAssertion.SubjectDigest != digest {
		t.Fatalf("login Host HMAC digest mismatch: got=%q want=%q", loginAssertion.SubjectDigest, digest)
	}
	if loginAssertion.DisplayName == "" {
		t.Fatalf("login display name empty: %#v", loginAssertion)
	}
	loginResult, err := extAuth.CompleteLogin(t.Context(), identity.ExternalAuthAssertion{
		ProviderID:              githubAuthProviderID,
		ProviderContractVersion: githubAuthProviderID + "@1",
		OwnerExtensionID:        extension.ID,
		OwnerPackageDigest:      extension.PackageDigest,
		Operation:               identity.ExternalAuthOperationLogin,
		ProviderSubject:         loginAssertion.ProviderSubject,
		SubjectDigest:           loginAssertion.SubjectDigest,
		DisplayName:             loginAssertion.DisplayName,
		EmailHint:               loginAssertion.EmailHint,
		CorrelationID:           "login-corr",
	})
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if loginResult.User.ID != loginUser.ID || loginResult.ProviderID != githubAuthProviderID {
		t.Fatalf("CompleteLogin result = %#v", loginResult)
	}
	// Host session-bound recent-auth after external login (session issue effect).
	sid := "e2e-login-session"
	fp := identity.SessionFingerprint(sid)
	if err := extAuth.MarkSessionAuthenticated(t.Context(), loginResult.User.ID, fp, "external", githubAuthProviderID); err != nil {
		t.Fatalf("MarkSessionAuthenticated: %v", err)
	}
	ok, err := recentAuth.IsSessionRecentlyAuthenticated(t.Context(), loginResult.User.ID, fp)
	if err != nil || !ok {
		t.Fatalf("session recent-auth not marked: ok=%v err=%v", ok, err)
	}

	// --- registration: Protocol V2 complete → opaque Host registration ticket ---
	regAssertion := runGitHubOAuthRoundTrip(t, fake, authFlow, identity.AuthOperationRegistrationStart, identity.AuthOperationRegistrationComplete, 0, callbackURL, "reg-corr")
	if regAssertion.ProviderSubject != githubAuthSubject {
		t.Fatalf("registration subject = %q", regAssertion.ProviderSubject)
	}
	ticketStore := identity.NewInMemoryRegistrationTicketStore()
	ticketToken, err := identity.GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := ticketStore.Save(t.Context(), identity.RegistrationTicket{
		Token:                   ticketToken,
		ProviderID:              githubAuthProviderID,
		ProviderContractVersion: githubAuthProviderID + "@1",
		OwnerExtensionID:        extension.ID,
		OwnerPackageDigest:      extension.PackageDigest,
		Operation:               identity.ExternalAuthOperationRegistration,
		SourceOperation:         identity.ExternalAuthOperationRegistration,
		ProviderSubject:         regAssertion.ProviderSubject,
		SubjectDigest:           regAssertion.SubjectDigest,
		DisplayName:             regAssertion.DisplayName,
		EmailHint:               regAssertion.EmailHint,
		CorrelationID:           "reg-corr",
		CreatedAt:               now,
		ExpiresAt:               now.Add(identity.RegistrationTicketDefaultTTL),
	}); err != nil {
		t.Fatalf("save registration ticket: %v", err)
	}
	consumed, err := ticketStore.Consume(t.Context(), ticketToken)
	if err != nil {
		t.Fatalf("consume registration ticket: %v", err)
	}
	if consumed.ProviderSubject != githubAuthSubject || consumed.OwnerPackageDigest != extension.PackageDigest {
		t.Fatalf("ticket payload: %#v", consumed)
	}
	if _, err := ticketStore.Consume(t.Context(), ticketToken); err == nil {
		t.Fatal("registration ticket must be one-use")
	}
	// 路径继续到固定 Host 注册 continuation（浏览器只见 opaque ticket）。
	cont := identity.ExternalRegistrationContinuationPath(ticketToken, "/topics")
	if !strings.HasPrefix(cont, "/register?") || !strings.Contains(cont, "ticket=") {
		t.Fatalf("registration continuation path: %q", cont)
	}

	// --- link: Protocol V2 complete → Host CompleteLink with session-bound recent-auth ---
	actorID := int64(22)
	actorFP := identity.SessionFingerprint("e2e-actor-session")
	if err := recentAuth.MarkSessionRecentlyAuthenticated(t.Context(), actorID, actorFP, "password", "", identity.RecentAuthDefaultTTL); err != nil {
		t.Fatalf("mark actor recent-auth: %v", err)
	}
	// 使用不同 subject，避免与 login 已绑定的 subject 冲突。
	fake.UserID = 999001
	linkAssertion := runGitHubOAuthRoundTrip(t, fake, authFlow, identity.AuthOperationLinkStart, identity.AuthOperationLinkComplete, actorID, callbackURL, "link-corr")
	linkSubject := strconv.FormatInt(fake.UserID, 10)
	if linkAssertion.ProviderSubject != linkSubject {
		t.Fatalf("link subject = %q want %q", linkAssertion.ProviderSubject, linkSubject)
	}
	linkResult, err := extAuth.CompleteLink(t.Context(), identity.ExternalAuthAssertion{
		ProviderID:              githubAuthProviderID,
		ProviderContractVersion: githubAuthProviderID + "@1",
		OwnerExtensionID:        extension.ID,
		OwnerPackageDigest:      extension.PackageDigest,
		Operation:               identity.ExternalAuthOperationLink,
		ProviderSubject:         linkAssertion.ProviderSubject,
		SubjectDigest:           linkAssertion.SubjectDigest,
		DisplayName:             linkAssertion.DisplayName,
		EmailHint:               linkAssertion.EmailHint,
		CorrelationID:           "link-corr",
	}, actorID, actorFP)
	if err != nil {
		t.Fatalf("CompleteLink: %v", err)
	}
	if linkResult.User.ID != actorID || linkResult.LinkID == 0 {
		t.Fatalf("CompleteLink result = %#v", linkResult)
	}
	if linkStore.linkCalls == 0 {
		t.Fatal("expected link store write after authorized CompleteLink")
	}

	// 无 recent-auth 时 fail closed，零额外写入。
	before := linkStore.linkCalls
	if _, err := extAuth.CompleteLink(t.Context(), identity.ExternalAuthAssertion{
		ProviderID: githubAuthProviderID, Operation: identity.ExternalAuthOperationLink,
		OwnerExtensionID: extension.ID, OwnerPackageDigest: extension.PackageDigest,
		ProviderSubject: "777", CorrelationID: "link-deny",
	}, actorID, identity.SessionFingerprint("other-session")); !errors.Is(err, identity.ErrExternalAuthRecentAuthRequired) {
		t.Fatalf("link without recent-auth: %v", err)
	}
	if linkStore.linkCalls != before {
		t.Fatalf("unauthorized link must not write: before=%d after=%d", before, linkStore.linkCalls)
	}
}

func runGitHubOAuthRoundTrip(
	t *testing.T,
	fake *headlessFakeGitHub,
	flow *identity.AuthProviderFlow,
	startOp, completeOp string,
	actorUserID int64,
	callbackURL, correlation string,
) identity.AuthProviderCompleteResult {
	t.Helper()
	state, err := identity.GenerateCallbackState()
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge, err := identity.GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	start, err := flow.Start(t.Context(), identity.AuthProviderStartInput{
		ProviderID: githubAuthProviderID, Operation: startOp,
		ActorUserID: actorUserID, CorrelationID: correlation,
		State: state, CodeChallenge: challenge, CallbackURL: callbackURL,
	})
	if err != nil {
		t.Fatalf("%s: %v", startOp, err)
	}
	if start.Status != identity.AuthStartStatusRedirect || start.RedirectURL == "" {
		t.Fatalf("%s result = %#v", startOp, start)
	}
	if !strings.HasPrefix(start.RedirectURL, fake.AuthURL) {
		t.Fatalf("authorize URL not pointed at fake GitHub: %s", start.RedirectURL)
	}
	parsed, err := url.Parse(start.RedirectURL)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("state") != state || q.Get("code_challenge") != challenge || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorize params: %v", q)
	}
	if q.Get("redirect_uri") != callbackURL {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}

	code := "code-" + correlation
	fake.IssueCode(code, verifier)
	complete, err := flow.Complete(t.Context(), identity.AuthProviderCompleteInput{
		ProviderID: githubAuthProviderID, Operation: completeOp,
		ActorUserID: actorUserID, TargetUserID: actorUserID,
		CorrelationID: correlation, CompletionToken: code,
		CodeVerifier: verifier, CallbackURL: callbackURL,
	})
	if err != nil {
		t.Fatalf("%s: %v", completeOp, err)
	}
	if complete.ProviderSubject == "" || complete.SubjectDigest == "" {
		t.Fatalf("%s missing subject/digest: %#v", completeOp, complete)
	}
	if complete.OwnerExtensionID != githubAuthExtID || complete.OwnerPackageDigest == "" {
		t.Fatalf("%s live artifact missing: %#v", completeOp, complete)
	}
	// 公开响应路径不得泄漏 raw subject 到日志以外的结构字段已在 Host 内部。
	return complete
}

func buildGitHubAuthExtension(t *testing.T) extensions.Extension {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../"))
	sourceRoot := filepath.Join(repoRoot, "extensions/builtin/plugins/sforum-auth-github")
	packageRoot := filepath.Join(t.TempDir(), "sforum-auth-github")
	if err := os.CopyFS(packageRoot, os.DirFS(sourceRoot)); err != nil {
		t.Fatalf("copy github auth package: %v", err)
	}
	binaryPath := filepath.Join(packageRoot, "backend", "plugin")
	_ = os.Remove(binaryPath)
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binaryPath, ".")
	build.Dir = filepath.Join(sourceRoot, "backend")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sforum.auth-github: %v\n%s", err, output)
	}
	// 刷新精确 packageFiles digest。
	digestCmd := exec.Command("go", "run", "./cmd/sforum", "extension", "digest", "--write", packageRoot)
	digestCmd.Dir = filepath.Join(repoRoot, "apps/api")
	if output, err := digestCmd.CombinedOutput(); err != nil {
		t.Fatalf("digest --write: %v\n%s", err, output)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load package: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceBuiltin,
		IsSystem: true, IsDeletable: false,
		PackagePath: packageRoot, PackageDigest: packageDigest, Manifest: manifest, ActiveVersionID: 901,
		CapabilityGrants: []extensions.CapabilityGrant{
			{Key: capabilities.HostAPI, Risk: capabilities.RiskLow},
			{Key: capabilities.SettingsOwn, Risk: capabilities.RiskLow},
		},
	}
}

// --- fake GitHub (test-local; mirrors plugin FakeGitHub contract) ---

type headlessFakeGitHub struct {
	Server       *httptest.Server
	ClientID     string
	ClientSecret string
	UserID       int64
	Login        string
	Name         string
	Email        string

	AuthURL  string
	TokenURL string
	APIURL   string

	mu    sync.Mutex
	codes map[string]string
}

func newHeadlessFakeGitHub() *headlessFakeGitHub {
	f := &headlessFakeGitHub{
		ClientID: "e2e-client-id", ClientSecret: "e2e-client-secret",
		UserID: 424242, Login: "octocat", Name: "The Octocat",
		Email: "octocat@example.com",
		codes: map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/login/oauth/access_token", f.handleToken)
	mux.HandleFunc("/user/emails", f.handleEmails)
	mux.HandleFunc("/user", f.handleUser)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"current_user_url": f.APIURL + "/user"})
	})
	f.Server = httptest.NewServer(mux)
	f.APIURL = f.Server.URL
	f.AuthURL = f.Server.URL + "/login/oauth/authorize"
	f.TokenURL = f.Server.URL + "/login/oauth/access_token"
	return f
}

func (f *headlessFakeGitHub) Close() {
	if f != nil && f.Server != nil {
		f.Server.Close()
	}
}

func (f *headlessFakeGitHub) IssueCode(code, verifier string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes[code] = verifier
}

func (f *headlessFakeGitHub) handleToken(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	values, _ := url.ParseQuery(string(body))
	_ = r.ParseForm()
	for k, vs := range r.Form {
		if values.Get(k) == "" && len(vs) > 0 {
			values.Set(k, vs[0])
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if values.Get("client_id") != f.ClientID || values.Get("client_secret") != f.ClientSecret {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "incorrect_client_credentials"})
		return
	}
	code := values.Get("code")
	verifier := values.Get("code_verifier")
	want, ok := f.codes[code]
	if !ok || (want != "" && want != verifier) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		return
	}
	delete(f.codes, code)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"access_token": "gho_e2e_token", "token_type": "bearer", "scope": "read:user,user:email",
	})
}

func (f *headlessFakeGitHub) handleUser(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	user := map[string]any{
		"id": f.UserID, "login": f.Login, "name": f.Name, "email": f.Email,
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(user)
}

func (f *headlessFakeGitHub) handleEmails(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	email := f.Email
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode([]map[string]any{
		{"email": email, "primary": true, "verified": true},
	})
}

// --- Host fakes ---

type githubAuthPluginSettings map[string]map[string]string

func (s githubAuthPluginSettings) ListSettings(_ context.Context, extensionID string) (map[string]string, error) {
	return s[extensionID], nil
}

type headlessActivationStore struct {
	items map[string]identity.ProviderActivation
}

func (s *headlessActivationStore) Get(_ context.Context, providerID string) (identity.ProviderActivation, error) {
	if a, ok := s.items[providerID]; ok {
		return a, nil
	}
	return identity.ProviderActivation{}, identity.ErrProviderActivationNotFound
}
func (s *headlessActivationStore) List(_ context.Context) ([]identity.ProviderActivation, error) {
	out := make([]identity.ProviderActivation, 0, len(s.items))
	for _, a := range s.items {
		out = append(out, a)
	}
	return out, nil
}
func (s *headlessActivationStore) Upsert(context.Context, identity.ProviderActivationInput) (identity.ProviderActivation, error) {
	return identity.ProviderActivation{}, nil
}
func (s *headlessActivationStore) RecordProbe(context.Context, identity.ProviderActivationProbeResult) error {
	return nil
}
func (s *headlessActivationStore) Delete(context.Context, string) error { return nil }
func (s *headlessActivationStore) ResetOperationsToDefaults(context.Context, string) (identity.ProviderActivation, error) {
	return identity.ProviderActivation{}, nil
}

type headlessLinkStore struct {
	mu        sync.Mutex
	byKey     map[string]identity.ExternalIdentityLink
	byID      map[int64]identity.ExternalIdentityLink
	digests   map[int64]string
	nextID    int64
	linkCalls int
}

func newHeadlessLinkStore() *headlessLinkStore {
	return &headlessLinkStore{
		byKey:   map[string]identity.ExternalIdentityLink{},
		byID:    map[int64]identity.ExternalIdentityLink{},
		digests: map[int64]string{},
		nextID:  1,
	}
}

func (s *headlessLinkStore) seedActive(userID int64, providerID, digest, ownerExt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	link := identity.ExternalIdentityLink{
		ID: id, UserID: userID, ProviderID: providerID,
		Status:           identity.ExternalIdentityLinkStatusActive,
		OwnerExtensionID: ownerExt, Revision: 1,
	}
	s.byID[id] = link
	s.byKey[providerID+"|"+digest] = link
	s.digests[id] = digest
}

func (s *headlessLinkStore) Link(_ context.Context, input identity.LinkExternalIdentityInput, fence identity.ExternalIdentityLinkCommitFence) (identity.ExternalIdentityLinkMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linkCalls++
	if fence != nil {
		if err := fence(); err != nil {
			return identity.ExternalIdentityLinkMutation{}, err
		}
	}
	id := s.nextID
	s.nextID++
	link := identity.ExternalIdentityLink{
		ID: id, UserID: input.UserID, ProviderID: input.Provider.ID,
		Status:           identity.ExternalIdentityLinkStatusActive,
		OwnerExtensionID: input.Provider.Artifact.ExtensionID, Revision: 1,
	}
	s.byID[id] = link
	s.byKey[input.Provider.ID+"|"+input.ProviderSubjectDigest] = link
	s.digests[id] = input.ProviderSubjectDigest
	return identity.ExternalIdentityLinkMutation{Link: link}, nil
}
func (s *headlessLinkStore) Unlink(context.Context, identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, identity.ErrExternalIdentityLinkNotFound
}
func (s *headlessLinkStore) Erase(context.Context, identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, identity.ErrExternalIdentityLinkNotFound
}
func (s *headlessLinkStore) Get(_ context.Context, id int64) (identity.ExternalIdentityLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.byID[id]; ok {
		return l, nil
	}
	return identity.ExternalIdentityLink{}, identity.ErrExternalIdentityLinkNotFound
}
func (s *headlessLinkStore) FindActive(_ context.Context, providerID, digest string) (identity.ExternalIdentityLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.byKey[providerID+"|"+digest]; ok && l.Status == identity.ExternalIdentityLinkStatusActive {
		return l, nil
	}
	return identity.ExternalIdentityLink{}, identity.ErrExternalIdentityLinkNotFound
}
func (s *headlessLinkStore) ListUser(_ context.Context, userID int64) ([]identity.ExternalIdentityLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]identity.ExternalIdentityLink, 0)
	for _, l := range s.byID {
		if l.UserID == userID {
			out = append(out, l)
		}
	}
	return out, nil
}

type headlessRecentAuth struct {
	mu   sync.Mutex
	rows map[string]time.Time
}

func newHeadlessRecentAuth() *headlessRecentAuth {
	return &headlessRecentAuth{rows: map[string]time.Time{}}
}

func (m *headlessRecentAuth) key(userID int64, fp string) string {
	return strings.TrimSpace(fp) + "|" + strconv.FormatInt(userID, 10)
}

func (m *headlessRecentAuth) MarkSessionRecentlyAuthenticated(
	_ context.Context, userID int64, sessionFingerprint, method, providerID string, ttl time.Duration,
) error {
	_ = method
	_ = providerID
	if userID <= 0 || sessionFingerprint == "" {
		return identity.ErrExternalAuthRecentAuthRequired
	}
	if ttl <= 0 {
		ttl = identity.RecentAuthDefaultTTL
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[m.key(userID, sessionFingerprint)] = time.Now().Add(ttl)
	return nil
}

func (m *headlessRecentAuth) IsSessionRecentlyAuthenticated(
	_ context.Context, userID int64, sessionFingerprint string,
) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.rows[m.key(userID, sessionFingerprint)]
	return ok && time.Now().Before(exp), nil
}
