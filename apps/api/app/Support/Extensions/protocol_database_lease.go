package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const (
	RecommendedProtocolDatabaseLeaseHeartbeatInterval = 30 * time.Second
	RecommendedProtocolDatabaseLeaseOperationTimeout  = 10 * time.Second

	protocolDatabaseURLEnv       = "SFORUM_DATABASE_URL"
	protocolDatabaseLeaseIDEnv   = "SFORUM_DATABASE_LEASE_ID"
	protocolDatabaseExpiresAtEnv = "SFORUM_DATABASE_LEASE_EXPIRES_AT"
	protocolDatabaseGrantsEnv    = "SFORUM_DATABASE_GRANTS"
	protocolDatabaseSchemaEnv    = "SFORUM_DATABASE_SCHEMA"
	protocolDatabaseSearchEnv    = "SFORUM_DATABASE_SEARCH_PATH"
)

type RuntimeDatabaseLeaseRegistry interface {
	IssueRuntimeLease(context.Context, ExtensionDatabaseRuntimeLeaseIssue) (ExtensionDatabaseRuntimeCredential, error)
	HeartbeatRuntimeLease(context.Context, ExtensionDatabaseRuntimeLeaseRef, int64) (ExtensionDatabaseRuntimeLeaseSnapshot, error)
	BeginRuntimeLeaseDrain(context.Context, ExtensionDatabaseRuntimeLeaseRef, int64) (ExtensionDatabaseRuntimeLeaseSnapshot, error)
	RevokeRuntimeLease(context.Context, ExtensionDatabaseRuntimeLeaseRef, ExtensionDatabaseLeaseAuthority) (ExtensionDatabaseRuntimeLeaseSnapshot, error)
	InspectRuntimeLease(context.Context, ExtensionDatabaseRuntimeLeaseRef) (ExtensionDatabaseRuntimeLeaseSnapshot, error)
}

type protocolDatabaseLease struct {
	registry RuntimeDatabaseLeaseRegistry
	ref      ExtensionDatabaseRuntimeLeaseRef

	mu        sync.Mutex
	revision  int64
	expiresAt time.Time
	cancel    context.CancelFunc
	done      chan struct{}
}

func (s *ProtocolStarter) issueProtocolDatabaseLease(
	ctx context.Context,
	extension extensions.Extension,
	instanceID string,
) (*protocolDatabaseLease, []string, error) {
	declaration := extension.Manifest.Database
	if declaration == nil || !extensionDatabaseHasDirectRuntimePower(extensionmanifest.DatabaseGrants(declaration)) {
		return nil, nil, nil
	}
	if s.databaseLeases == nil || extension.ActiveVersionID <= 0 || strings.TrimSpace(instanceID) == "" {
		return nil, nil, ErrExtensionDatabaseRegistryInvalid
	}
	if s.trust != nil {
		identity, err := s.trust.RuntimeIdentity(ctx, extension)
		if err != nil || strings.TrimSpace(identity.TrustGrantID) == "" {
			return nil, nil, fmt.Errorf("resolve database runtime trust identity: %w", errors.Join(err, extensions.ErrTrustGrantNotFound))
		}
	} else if extension.Source != extensions.SourceBuiltin {
		return nil, nil, fmt.Errorf("resolve database runtime trust identity: %w", extensions.ErrTrustGrantNotFound)
	}
	artifact := ExtensionDatabaseArtifact{
		ExtensionID: extension.ID, Version: extension.Version,
		VersionID: extension.ActiveVersionID, PackageDigest: extension.PackageDigest,
	}
	credential, err := s.databaseLeases.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: instanceID,
		Authority: protocolDatabaseLeaseAuthority(ctx),
	})
	if err != nil {
		return nil, nil, err
	}
	if credential.Artifact != artifact || credential.RuntimeInstanceID != instanceID ||
		credential.LeaseID == "" || credential.Revision <= 0 || credential.ExpiresAt.IsZero() {
		return nil, nil, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	env, err := protocolDatabaseLeaseEnv(credential)
	if err != nil {
		return nil, nil, err
	}
	return &protocolDatabaseLease{
		registry: s.databaseLeases,
		ref: ExtensionDatabaseRuntimeLeaseRef{
			Artifact: artifact, RuntimeInstanceID: instanceID, LeaseID: credential.LeaseID,
		},
		revision: credential.Revision, expiresAt: credential.ExpiresAt.UTC(),
	}, env, nil
}

// protocolDatabaseLeaseAuthority 决定这次启动用哪种权威签发租约。
//
// 只有操作者发起的启动会在 context 上携带 actor + 审计锚点，此时才允许自建
// 精确授权行；其余启动（进程重启、Reconcile、恢复）一律走 host 权威，只能复用
// 既有授权，缺行失败关闭。
func protocolDatabaseLeaseAuthority(ctx context.Context) ExtensionDatabaseLeaseAuthority {
	if ctx != nil {
		if authority, ok := extensions.DatabaseGrantAuthorityFrom(ctx); ok {
			return ExtensionDatabaseLeaseAuthority{
				Kind:         ExtensionDatabaseLeaseIssuerActor,
				ActorUserID:  authority.ActorUserID,
				AuditEventID: authority.AuditEventID,
			}
		}
	}
	return ExtensionDatabaseLeaseAuthority{Kind: ExtensionDatabaseLeaseIssuerHost}
}

func protocolDatabaseLeaseEnv(credential ExtensionDatabaseRuntimeCredential) ([]string, error) {
	values := []struct {
		key   string
		value string
	}{
		{protocolDatabaseURLEnv, credential.ConnectionURL},
		{protocolDatabaseLeaseIDEnv, credential.LeaseID},
		{protocolDatabaseExpiresAtEnv, credential.ExpiresAt.UTC().Format(time.RFC3339Nano)},
		{protocolDatabaseGrantsEnv, strings.Join(credential.Powers, ",")},
		{protocolDatabaseSchemaEnv, credential.SchemaName},
		{protocolDatabaseSearchEnv, credential.SearchPath},
	}
	env := make([]string, 0, len(values))
	for _, item := range values {
		if item.value == "" || strings.ContainsAny(item.value, "\x00\r\n") {
			return nil, ErrExtensionDatabaseCredential
		}
		env = append(env, item.key+"="+item.value)
	}
	return env, nil
}

func extensionDatabaseHasDirectRuntimePower(powers []string) bool {
	for _, power := range powers {
		switch power {
		case extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantCoreViews,
			extensionmanifest.DatabaseGrantRawCore,
			extensionmanifest.DatabaseGrantKernel:
			return true
		}
	}
	return false
}

func (l *protocolDatabaseLease) startHeartbeat(
	interval time.Duration,
	timeout time.Duration,
	onFatal func(error),
) {
	if l == nil || l.registry == nil {
		return
	}
	if interval <= 0 {
		interval = RecommendedProtocolDatabaseLeaseHeartbeatInterval
	}
	if timeout <= 0 {
		timeout = RecommendedProtocolDatabaseLeaseOperationTimeout
	}
	l.mu.Lock()
	if l.cancel != nil {
		l.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.done = make(chan struct{})
	done := l.done
	l.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			l.mu.Lock()
			revision, expiresAt := l.revision, l.expiresAt
			l.mu.Unlock()
			heartbeatCtx, cancel := context.WithTimeout(ctx, timeout)
			next, err := l.registry.HeartbeatRuntimeLease(heartbeatCtx, l.ref, revision)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				l.mu.Lock()
				l.revision, l.expiresAt = next.Revision, next.ExpiresAt.UTC()
				l.mu.Unlock()
				continue
			}
			if errors.Is(err, ErrExtensionDatabaseRuntimeLeaseConflict) ||
				errors.Is(err, ErrExtensionDatabaseRuntimeLeaseNotFound) ||
				time.Now().UTC().Add(2*interval).After(expiresAt) {
				if onFatal != nil {
					onFatal(err)
				}
				return
			}
		}
	}()
}

func (l *protocolDatabaseLease) stopHeartbeat() {
	if l == nil {
		return
	}
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.cancel = nil
	l.done = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (l *protocolDatabaseLease) revoke(ctx context.Context) error {
	if l == nil || l.registry == nil {
		return nil
	}
	l.stopHeartbeat()
	current, err := l.registry.InspectRuntimeLease(ctx, l.ref)
	if err != nil {
		return err
	}
	switch current.Status {
	case ExtensionDatabaseLeaseRevoked, ExtensionDatabaseLeaseFailed:
		return nil
	case ExtensionDatabaseLeaseActive:
		current, err = l.registry.BeginRuntimeLeaseDrain(ctx, l.ref, current.Revision)
		if err != nil {
			latest, inspectErr := l.registry.InspectRuntimeLease(ctx, l.ref)
			if inspectErr != nil || (latest.Status != ExtensionDatabaseLeaseRevoked && latest.Status != ExtensionDatabaseLeaseFailed) {
				return errors.Join(err, inspectErr)
			}
			return nil
		}
	case ExtensionDatabaseLeaseDraining:
	default:
		return ErrExtensionDatabaseRuntimeLeaseConflict
	}
	_, err = l.registry.RevokeRuntimeLease(ctx, l.ref, ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerHost,
	})
	return err
}

var _ RuntimeDatabaseLeaseRegistry = (*PostgresExtensionDatabaseRegistry)(nil)
