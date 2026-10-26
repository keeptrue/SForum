package bootstrap

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	hosthttp "github.com/keeptrue/sforum/apps/api/app/Support/HostHTTP"
	pluginfiles "github.com/keeptrue/sforum/apps/api/app/Support/PluginFiles"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	settingslifecycle "github.com/keeptrue/sforum/apps/api/app/Support/SettingsLifecycle"
	"github.com/keeptrue/sforum/apps/api/config"
)

// productionHostPlatform holds SecretStore / HostHTTP / PluginFiles / SettingsLifecycle.
type productionHostPlatform struct {
	Secrets  *secretstore.Service
	HTTP     *hosthttp.Client
	Files    *pluginfiles.Service
	Settings *settingslifecycle.Service
	SecretV2 *hostapi.ProtocolV2SecretServiceServer
	FileV2   *hostapi.ProtocolV2FileServiceServer
	HTTPV2   *hostapi.ProtocolV2HttpServiceServer
}

// bindProductionHostPlatform assembles P11 platform services and binds them to
// the Protocol V2 Gateway before any plugin broker freezes the service set.
func bindProductionHostPlatform(
	cfg config.Config,
	pool *pgxpool.Pool,
	optionCipher *cryptox.OptionCipher,
	settingsKV settingslifecycle.SettingsKV,
	logger *slog.Logger,
	gateway *hostapi.Gateway,
) (*productionHostPlatform, error) {
	if pool == nil || gateway == nil {
		return nil, fmt.Errorf("bootstrap: production Host platform dependency unavailable")
	}
	_ = logger

	store, err := secretstore.NewPostgresStore(pool)
	if err != nil {
		return nil, fmt.Errorf("create secret store: %w", err)
	}
	auditStore, err := secretstore.NewPostgresAuditStore(pool)
	if err != nil {
		return nil, fmt.Errorf("create secret audit store: %w", err)
	}
	requireEnc := strings.EqualFold(cfg.AppEnv, "production") || strings.EqualFold(cfg.AppEnv, "staging")
	secretSvc, err := secretstore.NewWithOptions(secretstore.Options{
		Store: store, Cipher: optionCipher, Audit: auditStore,
		RequireEncryption: requireEnc,
		// 开发/测试显式允许透明 cipher；生产 RequireEncryption 已 fail closed。
		AllowTransparent: !requireEnc,
	})
	if err != nil {
		return nil, fmt.Errorf("create secret store service: %w", err)
	}

	httpClient := hosthttp.New(hosthttp.Options{
		AllowHTTP: !strings.EqualFold(cfg.AppEnv, "production"),
		// raw 网络仅非生产调试；生产 fail closed。
		AllowRaw: !strings.EqualFold(cfg.AppEnv, "production"),
		Secrets:  secretSvc,
	})

	filesRoot := filepath.Join(strings.TrimSpace(cfg.ExtensionRoot), "plugin-files")
	if filesRoot == string(filepath.Separator)+"plugin-files" || strings.TrimSpace(cfg.ExtensionRoot) == "" {
		filesRoot = filepath.Join("var", "plugin-files")
	}
	filesSvc, err := pluginfiles.New(filesRoot)
	if err != nil {
		return nil, fmt.Errorf("create plugin files service: %w", err)
	}

	var settingsSvc *settingslifecycle.Service
	if settingsKV != nil {
		docStore, storeErr := settingslifecycle.NewSettingsKVStore(settingsKV)
		if storeErr != nil {
			return nil, fmt.Errorf("create settings lifecycle store: %w", storeErr)
		}
		settingsSvc = settingslifecycle.NewWithStore(docStore, secretSvc).WithLegacyCipher(optionCipher)
	} else {
		// 无 extension store 时仍装配内存权威（worker 轻路径）；API 必须注入 KV。
		settingsSvc = settingslifecycle.NewWithStore(settingslifecycle.NewMemoryDocumentStore(), secretSvc).WithLegacyCipher(optionCipher)
	}

	secretV2, err := hostapi.NewProtocolV2SecretServiceServer(secretSvc)
	if err != nil {
		return nil, fmt.Errorf("create Protocol V2 Secret service: %w", err)
	}
	fileV2, err := hostapi.NewProtocolV2FileServiceServer(filesSvc)
	if err != nil {
		return nil, fmt.Errorf("create Protocol V2 File service: %w", err)
	}
	httpV2, err := hostapi.NewProtocolV2HttpServiceServer(httpClient)
	if err != nil {
		return nil, fmt.Errorf("create Protocol V2 HTTP service: %w", err)
	}
	if err := gateway.BindProtocolV2SecretService(secretV2); err != nil {
		return nil, fmt.Errorf("bind Protocol V2 Secret service: %w", err)
	}
	if err := gateway.BindProtocolV2FileService(fileV2); err != nil {
		return nil, fmt.Errorf("bind Protocol V2 File service: %w", err)
	}
	if err := gateway.BindProtocolV2HttpService(httpV2); err != nil {
		return nil, fmt.Errorf("bind Protocol V2 HTTP service: %w", err)
	}
	return &productionHostPlatform{
		Secrets: secretSvc, HTTP: httpClient, Files: filesSvc, Settings: settingsSvc,
		SecretV2: secretV2, FileV2: fileV2, HTTPV2: httpV2,
	}, nil
}
