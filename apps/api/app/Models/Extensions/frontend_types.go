package extensions

import (
	"context"
	"errors"
	"time"
)

const (
	AdminFrontendKindNone              = "none"
	AdminFrontendKindPrebuiltComponent = "prebuilt_component"

	FrontendTrustNone          = "none"
	FrontendTrustRequired      = "required"
	FrontendTrustTrusted       = "trusted"
	FrontendTrustSourceTrusted = "source_trusted"
	FrontendTrustRevoked       = "revoked"
	FrontendTrustInvalidated   = "invalidated"
)

type FrontendStatus struct {
	ExtensionID string           `json:"extensionId"`
	Kind        string           `json:"kind"`
	Component   *AdminComponent  `json:"component,omitempty"`
	Components  []AdminComponent `json:"components,omitempty"`
	TrustState  string           `json:"trustState"`
	Digest      string           `json:"digest,omitempty"`
}

type GrantFrontendInput struct {
	Digest       string                     `json:"digest"`
	Confirmation *FrontendTrustConfirmation `json:"confirmation"`
}

// FrontendTrustConfirmation 只证明管理员有意授权当前声明；真正边界仍是服务端保存的 digest grant。
type FrontendTrustConfirmation struct {
	ChallengeID  string `json:"challengeId"`
	Code         string `json:"code"`
	ExtensionID  string `json:"extensionId"`
	Version      string `json:"version"`
	Digest       string `json:"digest"`
	APIVersion   int    `json:"apiVersion"`
	ComponentID  string `json:"componentId"`
	Phrase       string `json:"phrase"`
	Acknowledged bool   `json:"acknowledged"`
}

type FrontendTrustChallenge struct {
	ChallengeID string    `json:"challengeId"`
	Code        string    `json:"code"`
	ExtensionID string    `json:"extensionId"`
	Version     string    `json:"version"`
	Digest      string    `json:"digest"`
	APIVersion  int       `json:"apiVersion"`
	ComponentID string    `json:"componentId"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type FrontendAsset struct {
	Body        []byte
	ContentType string
	ETag        string
	Digest      string
	Integrity   string
	CSP         []string
}

const (
	PublicFrontendSchemaV1    = "sforum.public-frontend-component@1"
	PublicFrontendAPIVersion  = 1
	PublicFrontendTrustNotice = "fully_trusted_browser_code"
)

type PublicFrontendAssetReference struct {
	Handle          string   `json:"handle"`
	ContractVersion string   `json:"contractVersion"`
	ExtensionID     string   `json:"extensionId"`
	PackageDigest   string   `json:"packageDigest"`
	ImpactDigest    string   `json:"impactDigest"`
	Type            string   `json:"type"`
	Digest          string   `json:"digest"`
	Integrity       string   `json:"integrity"`
	Dependencies    []string `json:"dependencies,omitempty"`
	Scope           []string `json:"scope,omitempty"`
	Module          bool     `json:"module"`
	Loading         string   `json:"loading"`
	CSP             []string `json:"csp,omitempty"`
	AssetPath       string   `json:"assetPath"`
}

type PublicFrontendComponent struct {
	SchemaVersion    string `json:"schemaVersion"`
	APIVersion       int    `json:"apiVersion"`
	TrustNotice      string `json:"trustNotice"`
	ExtensionID      string `json:"extensionId"`
	ExtensionVersion string `json:"extensionVersion"`
	// CoreVersion 是宿主 release 级版本；公开面只给这一项，不含 commit/dev 标识。
	CoreVersion           string                         `json:"coreVersion"`
	PackageDigest         string                         `json:"packageDigest"`
	ImpactDigest          string                         `json:"impactDigest"`
	ComponentID           string                         `json:"componentId"`
	ContractVersion       string                         `json:"contractVersion"`
	Action                string                         `json:"action"`
	TargetID              string                         `json:"targetId,omitempty"`
	TargetContractVersion string                         `json:"targetContractVersion,omitempty"`
	PropsSchema           string                         `json:"propsSchema,omitempty"`
	ResultSchema          string                         `json:"resultSchema,omitempty"`
	Entry                 PublicFrontendAssetReference   `json:"entry"`
	Assets                []PublicFrontendAssetReference `json:"assets"`
	CSP                   []string                       `json:"csp,omitempty"`
}

type FrontendTrustGrant struct {
	ID                  int64      `json:"id"`
	ExtensionID         string     `json:"extensionId"`
	ExtensionVersion    string     `json:"extensionVersion"`
	PackageDigest       string     `json:"packageDigest"`
	AdminFrontendDigest string     `json:"adminFrontendDigest"`
	APIVersion          int        `json:"apiVersion"`
	ComponentIDs        []string   `json:"componentIds"`
	GrantedByUserID     int64      `json:"grantedByUserId"`
	GrantedAt           time.Time  `json:"grantedAt"`
	RevokedAt           *time.Time `json:"revokedAt,omitempty"`
	RevokedByUserID     int64      `json:"revokedByUserId,omitempty"`
}

type FrontendTrustGrantInput struct {
	ExtensionID         string
	ExtensionVersion    string
	PackageDigest       string
	AdminFrontendDigest string
	APIVersion          int
	ComponentIDs        []string
	GrantedByUserID     int64
}

type FrontendRevocationInput struct {
	ExtensionID         string
	ExtensionVersion    string
	AdminFrontendDigest string
	RequestedByUserID   int64
}

var (
	ErrFrontendGrantNotFound      = errors.New("extensions: frontend trust grant not found")
	ErrFrontendGrantConflict      = errors.New("extensions: frontend trust grant conflicts with existing immutable declaration")
	ErrFrontendGrantStateConflict = errors.New("extensions: frontend trust grant state conflict")
)

type FrontendTrustStore interface {
	FrontendGrant(context.Context, string, string, string) (FrontendTrustGrant, error)
	LiveFrontendGrants(context.Context, string) ([]FrontendTrustGrant, error)
	CreateFrontendGrant(context.Context, FrontendTrustGrantInput) (FrontendTrustGrant, error)
	RevokeFrontendGrant(context.Context, FrontendRevocationInput) (FrontendTrustGrant, error)
	RevokeAllFrontendGrants(context.Context, string, int64) error
}
