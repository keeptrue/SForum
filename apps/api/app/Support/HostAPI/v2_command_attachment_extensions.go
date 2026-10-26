package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 三条命令让扩展复用 Core 附件体系（上传、对象存储、出图、变体、回收），
// 而不是把图片塞进自己的业务表或自建 bytea 存储：
//
//	claim   —— 把某个已上传附件登记成「本扩展拥有的公开资产」，强制公开并重算计数
//	release —— 删掉引用并重算计数，让孤儿回收正常生效
//	list    —— 只读对账：列出本扩展全部存活引用
//
// 命令目录是数据驱动的，proto/SDK 生成物不变；`host_commands` 已是合法 grant。
const (
	CommandAttachmentExtensionClaimID             = "sforum.attachments.extension.claim"
	CommandAttachmentExtensionClaimVersion        = "1"
	CommandAttachmentExtensionClaimInputSchema    = "sforum.attachments.extension.claim.input"
	CommandAttachmentExtensionClaimOutputSchema   = "sforum.attachments.extension.claim.result"
	CommandAttachmentExtensionReleaseID           = "sforum.attachments.extension.release"
	CommandAttachmentExtensionReleaseVersion      = "1"
	CommandAttachmentExtensionReleaseInputSchema  = "sforum.attachments.extension.release.input"
	CommandAttachmentExtensionReleaseOutputSchema = "sforum.attachments.extension.release.result"
	CommandAttachmentExtensionListID              = "sforum.attachments.extension.list"
	CommandAttachmentExtensionListVersion         = "1"
	CommandAttachmentExtensionListInputSchema     = "sforum.attachments.extension.list.input"
	CommandAttachmentExtensionListOutputSchema    = "sforum.attachments.extension.list.result"
	CommandAttachmentExtensionSchemaVersion       = "1"
)

// ProtocolV2AttachmentExtensionRuntime 是扩展附件引用命令的领域边界。
// 约束（站点上限/引用上限）来自 attachments 服务，引用行读写来自
// Attachments/ExtensionReference store；bootstrap 把两者合成一个适配器注入，
// 这样 Host Command 目录本身不依赖任何附件存储实现。
type ProtocolV2AttachmentExtensionRuntime interface {
	ExtensionAttachmentPolicy(ctx context.Context) (extensionreference.Policy, error)
	PrepareClaim(ctx context.Context, tx pgx.Tx, input extensionreference.ClaimInput) (*extensionreference.ClaimPlan, error)
	ApplyClaim(ctx context.Context, tx pgx.Tx, plan *extensionreference.ClaimPlan) (extensionreference.Claim, error)
	Count(ctx context.Context, tx pgx.Tx, input extensionreference.ReleaseInput) (int, error)
	Release(ctx context.Context, tx pgx.Tx, input extensionreference.ReleaseInput) (extensionreference.Release, error)
	List(ctx context.Context, tx pgx.Tx, input extensionreference.ListInput) (extensionreference.List, error)
}

type protocolV2AttachmentExtensionClaimInput struct {
	AttachmentPublicID string `json:"attachmentPublicId"`
	Context            string `json:"context"`
}

type protocolV2AttachmentExtensionReleaseInput struct {
	Context            string `json:"context"`
	AttachmentPublicID string `json:"attachmentPublicId"`
}

type protocolV2AttachmentExtensionListInput struct {
	Page    int `json:"page"`
	PerPage int `json:"perPage"`
}

// newProtocolV2AttachmentExtensionCommandDefinitions 返回 claim/release/list 三条定义。
func newProtocolV2AttachmentExtensionCommandDefinitions(runtime ProtocolV2AttachmentExtensionRuntime) []protocolV2CommandDefinition {
	return []protocolV2CommandDefinition{
		newProtocolV2AttachmentExtensionClaimCommandDefinition(runtime),
		newProtocolV2AttachmentExtensionReleaseCommandDefinition(runtime),
		newProtocolV2AttachmentExtensionListCommandDefinition(runtime),
	}
}

func newProtocolV2AttachmentExtensionClaimCommandDefinition(runtime ProtocolV2AttachmentExtensionRuntime) protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandAttachmentExtensionClaimID, Version: CommandAttachmentExtensionClaimVersion,
		InputSchemaID: CommandAttachmentExtensionClaimInputSchema, InputSchemaVersion: CommandAttachmentExtensionSchemaVersion,
		OutputSchemaID: CommandAttachmentExtensionClaimOutputSchema, OutputSchemaVersion: CommandAttachmentExtensionSchemaVersion,
		ActorMode: protocolV2CommandActorDelegated,
		// 能上传才可能认领；归属判定在命令体内，这样普通用户自助建应用时
		// 不必额外持 attachment.manage（那是管理全站附件库的权限）。
		RequiredPermissions: []string{identity.PermissionAttachmentUpload},
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2AttachmentExtensionClaimInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		// Plan 阶段没有事务也没有 actor，只能投影调用方已给出的标识；
		// 权威读取与校验全部在 Prepare 里完成。
		return protocolV2AttachmentExtensionClaimPreparation(input, extensionreference.Claim{
			PublicID: input.AttachmentPublicID, URL: extensionreference.URLPath(input.AttachmentPublicID),
		}, true)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2AttachmentExtensionClaimInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		if runtime == nil || tx == nil {
			return nil, protocolV2AttachmentExtensionUnavailable()
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		// 归属判定需要 actor 的实时权限，只能在这里从数据库重载。
		actor, err := loadProtocolV2CommandActor(ctx, tx, actorUserID)
		if err != nil {
			return nil, protocolV2AttachmentExtensionCommandError(err)
		}
		policy, err := runtime.ExtensionAttachmentPolicy(ctx)
		if err != nil {
			return nil, protocolV2AttachmentExtensionCommandError(err)
		}
		plan, err := runtime.PrepareClaim(ctx, tx, extensionreference.ClaimInput{
			ExtensionID:      protocolV2CommandExtensionID(request),
			Context:          input.Context,
			PublicID:         input.AttachmentPublicID,
			ActorUserID:      actor.ID,
			ManageAny:        actor.Can(identity.PermissionAttachmentManage),
			ExpectedRevision: protocolV2AttachmentExtensionRevision(request),
			Policy:           policy,
		})
		if err != nil {
			return nil, protocolV2AttachmentExtensionCommandError(err)
		}
		// 投影写后的状态：新增 context 会让引用数 +1，同 context 换图行数不变。
		projectedCount := plan.ReferenceCount + 1
		if plan.OldAttachmentID == plan.AttachmentID {
			projectedCount = plan.ReferenceCount
		}
		preparation, err := protocolV2AttachmentExtensionClaimPreparation(input, extensionreference.Claim{
			AttachmentID: plan.AttachmentID, PublicID: plan.PublicID,
			URL:         extensionreference.URLPath(plan.PublicID),
			ContentType: plan.ContentType, Width: plan.Width, Height: plan.Height,
			SizeBytes: plan.SizeBytes, SHA256: plan.SHA256,
			ReferenceCount: projectedCount, UpdatedAt: plan.UpdatedAt,
		}, true)
		if err != nil {
			return nil, err
		}
		preparation.private = plan
		return preparation, nil
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, _ *hostv2.CommandRequest, preparation *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		plan, ok := preparation.private.(*extensionreference.ClaimPlan)
		if !ok || plan == nil || runtime == nil || tx == nil {
			return nil, protocolV2AttachmentExtensionPreparationInvalid()
		}
		claim, err := runtime.ApplyClaim(ctx, tx, plan)
		if err != nil {
			return nil, protocolV2AttachmentExtensionCommandError(err)
		}
		output, err := protocolV2AttachmentExtensionClaimDocument(claim, false)
		if err != nil {
			return nil, fmt.Errorf("encode attachment extension claim result: %w", err)
		}
		return &protocolV2CommandExecution{
			Output: output, CommittedRevision: claim.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}, nil
	}
	return definition
}

func newProtocolV2AttachmentExtensionReleaseCommandDefinition(runtime ProtocolV2AttachmentExtensionRuntime) protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandAttachmentExtensionReleaseID, Version: CommandAttachmentExtensionReleaseVersion,
		InputSchemaID: CommandAttachmentExtensionReleaseInputSchema, InputSchemaVersion: CommandAttachmentExtensionSchemaVersion,
		OutputSchemaID: CommandAttachmentExtensionReleaseOutputSchema, OutputSchemaVersion: CommandAttachmentExtensionSchemaVersion,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionAttachmentUpload},
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2AttachmentExtensionReleaseInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2AttachmentExtensionReleasePreparation(input, 0, time.Time{}, true)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2AttachmentExtensionReleaseInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		if runtime == nil || tx == nil {
			return nil, protocolV2AttachmentExtensionUnavailable()
		}
		count, err := runtime.Count(ctx, tx, input)
		if err != nil {
			return nil, protocolV2AttachmentExtensionCommandError(err)
		}
		return protocolV2AttachmentExtensionReleasePreparation(input, count, time.Time{}, true)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, _ *hostv2.CommandRequest, preparation *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		input, ok := preparation.private.(extensionreference.ReleaseInput)
		if !ok || runtime == nil || tx == nil {
			return nil, protocolV2AttachmentExtensionPreparationInvalid()
		}
		released, err := runtime.Release(ctx, tx, input)
		if err != nil {
			return nil, protocolV2AttachmentExtensionCommandError(err)
		}
		output, err := protocolV2AttachmentExtensionReleaseDocument(released.Released, released.Revision, false)
		if err != nil {
			return nil, fmt.Errorf("encode attachment extension release result: %w", err)
		}
		revision := ""
		if !released.Revision.IsZero() {
			revision = released.Revision.UTC().Format(time.RFC3339Nano)
		}
		return &protocolV2CommandExecution{Output: output, CommittedRevision: revision}, nil
	}
	return definition
}

func newProtocolV2AttachmentExtensionListCommandDefinition(runtime ProtocolV2AttachmentExtensionRuntime) protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandAttachmentExtensionListID, Version: CommandAttachmentExtensionListVersion,
		InputSchemaID: CommandAttachmentExtensionListInputSchema, InputSchemaVersion: CommandAttachmentExtensionSchemaVersion,
		OutputSchemaID: CommandAttachmentExtensionListOutputSchema, OutputSchemaVersion: CommandAttachmentExtensionSchemaVersion,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionAttachmentUpload},
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2AttachmentExtensionListInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2AttachmentExtensionListPreparation(extensionreference.List{
			Items: []extensionreference.Item{}, Page: input.Page, PerPage: input.PerPage,
		}, true)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2AttachmentExtensionListInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		if runtime == nil || tx == nil {
			return nil, protocolV2AttachmentExtensionUnavailable()
		}
		list, err := runtime.List(ctx, tx, extensionreference.ListInput{
			ExtensionID: protocolV2CommandExtensionID(request),
			Page:        input.Page, PerPage: input.PerPage,
		})
		if err != nil {
			return nil, protocolV2AttachmentExtensionCommandError(err)
		}
		preparation, err := protocolV2AttachmentExtensionListPreparation(list, true)
		if err != nil {
			return nil, err
		}
		preparation.private = list
		return preparation, nil
	}
	definition.Execute = func(_ context.Context, _ pgx.Tx, _ *hostv2.CommandRequest, preparation *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		list, ok := preparation.private.(extensionreference.List)
		if !ok {
			return nil, protocolV2AttachmentExtensionPreparationInvalid()
		}
		output, err := protocolV2AttachmentExtensionListDocument(list, false)
		if err != nil {
			return nil, fmt.Errorf("encode attachment extension list result: %w", err)
		}
		return &protocolV2CommandExecution{Output: output}, nil
	}
	return definition
}

func protocolV2AttachmentExtensionClaimInputFromRequest(request *hostv2.CommandRequest) (protocolV2AttachmentExtensionClaimInput, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2AttachmentExtensionClaimInput](request)
	if err != nil {
		return input, err
	}
	input.AttachmentPublicID = strings.TrimSpace(input.AttachmentPublicID)
	input.Context = strings.TrimSpace(input.Context)
	if input.AttachmentPublicID == "" || input.Context == "" {
		return input, invalidProtocolV2DomainCommandInput()
	}
	if protocolV2AttachmentExtensionRevision(request).IsZero() {
		return input, invalidProtocolV2DomainCommandInput()
	}
	return input, nil
}

func protocolV2AttachmentExtensionReleaseInputFromRequest(request *hostv2.CommandRequest) (extensionreference.ReleaseInput, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2AttachmentExtensionReleaseInput](request)
	if err != nil {
		return extensionreference.ReleaseInput{}, err
	}
	contextValue := strings.TrimSpace(input.Context)
	publicID := strings.TrimSpace(input.AttachmentPublicID)
	// 必须且只能提供一个作用域：两者都给会让「释放哪个」产生歧义。
	if (contextValue == "") == (publicID == "") {
		return extensionreference.ReleaseInput{}, invalidProtocolV2DomainCommandInput()
	}
	return extensionreference.ReleaseInput{
		ExtensionID: protocolV2CommandExtensionID(request),
		Context:     contextValue, PublicID: publicID,
	}, nil
}

func protocolV2AttachmentExtensionListInputFromRequest(request *hostv2.CommandRequest) (protocolV2AttachmentExtensionListInput, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2AttachmentExtensionListInput](request)
	if err != nil {
		return input, err
	}
	if input.Page < 0 || input.PerPage < 0 {
		return input, invalidProtocolV2DomainCommandInput()
	}
	return input, nil
}

// protocolV2CommandExtensionID 返回 Host 已证实的扩展 id。
// 引擎在 definition() 阶段已校验它与运行时身份一致，绝不取插件输入。
func protocolV2CommandExtensionID(request *hostv2.CommandRequest) string {
	return strings.TrimSpace(request.GetContext().GetExtension().GetExtensionId())
}

func protocolV2AttachmentExtensionRevision(request *hostv2.CommandRequest) time.Time {
	revision, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(request.GetExpectedRevision()))
	if err != nil || revision.IsZero() {
		return time.Time{}
	}
	return revision.UTC()
}

func protocolV2AttachmentExtensionClaimPreparation(
	input protocolV2AttachmentExtensionClaimInput,
	claim extensionreference.Claim,
	planned bool,
) (*protocolV2CommandPreparation, error) {
	document, err := protocolV2AttachmentExtensionClaimDocument(claim, planned)
	if err != nil {
		return nil, err
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.attachments.extension.claim@1", ResourceId: input.AttachmentPublicID, Allowed: true,
			Reason: "The delegated actor must own the attachment or hold attachment.manage at execution time.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "attachments", Action: "claim", ResourceType: "attachment", ResourceId: input.AttachmentPublicID,
			Summary:    "Register an uploaded image as this extension's public asset; the Host keeps the storage object, reference count, and cleanup lifecycle.",
			Reversible: true,
		}},
		ProjectedResult: document,
	}, nil
}

func protocolV2AttachmentExtensionReleasePreparation(
	input extensionreference.ReleaseInput,
	released int,
	revision time.Time,
	planned bool,
) (*protocolV2CommandPreparation, error) {
	resourceID := input.Context
	if resourceID == "" {
		resourceID = input.PublicID
	}
	document, err := protocolV2AttachmentExtensionReleaseDocument(released, revision, planned)
	if err != nil {
		return nil, err
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.attachments.extension.release@1", ResourceId: resourceID, Allowed: true,
			Reason: "Release only deletes references owned by the calling extension namespace.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "attachments", Action: "release", ResourceType: "attachment_extension_reference", ResourceId: resourceID,
			Summary:    "Delete this extension's attachment references; the stored object becomes an orphan and is reclaimed by the normal retention window.",
			Reversible: true,
		}},
		ProjectedResult: document,
		private:         input,
	}, nil
}

func protocolV2AttachmentExtensionListPreparation(
	list extensionreference.List,
	planned bool,
) (*protocolV2CommandPreparation, error) {
	document, err := protocolV2AttachmentExtensionListDocument(list, planned)
	if err != nil {
		return nil, err
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.attachments.extension.list@1", ResourceId: "extension", Allowed: true,
			Reason: "The command only reads references under the calling extension's namespace.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "attachments", Action: "list", ResourceType: "attachment_extension_reference", ResourceId: "extension",
			Summary: "Read this extension's live attachment references for reconciliation.", Reversible: true,
		}},
		ProjectedResult: document,
	}, nil
}

func protocolV2AttachmentExtensionClaimDocument(claim extensionreference.Claim, planned bool) (*protocolv2.TypedDocument, error) {
	return protocolV2Document(CommandAttachmentExtensionClaimOutputSchema, CommandAttachmentExtensionSchemaVersion, map[string]any{
		"planned":        planned,
		"attachmentId":   strconv.FormatInt(claim.AttachmentID, 10),
		"publicId":       claim.PublicID,
		"url":            claim.URL,
		"contentType":    claim.ContentType,
		"width":          strconv.Itoa(claim.Width),
		"height":         strconv.Itoa(claim.Height),
		"sizeBytes":      strconv.FormatInt(claim.SizeBytes, 10),
		"sha256":         claim.SHA256,
		"referenceCount": strconv.Itoa(claim.ReferenceCount),
		"revision":       protocolV2AttachmentExtensionTime(claim.UpdatedAt),
	})
}

func protocolV2AttachmentExtensionReleaseDocument(released int, revision time.Time, planned bool) (*protocolv2.TypedDocument, error) {
	return protocolV2Document(CommandAttachmentExtensionReleaseOutputSchema, CommandAttachmentExtensionSchemaVersion, map[string]any{
		"planned":  planned,
		"released": strconv.Itoa(released),
		"revision": protocolV2AttachmentExtensionTime(revision),
	})
}

func protocolV2AttachmentExtensionListDocument(list extensionreference.List, planned bool) (*protocolv2.TypedDocument, error) {
	items := make([]any, 0, len(list.Items))
	for _, item := range list.Items {
		items = append(items, map[string]any{
			"context":     item.Context,
			"publicId":    item.PublicID,
			"url":         item.URL,
			"contentType": item.ContentType,
			"width":       strconv.Itoa(item.Width),
			"height":      strconv.Itoa(item.Height),
			"sizeBytes":   strconv.FormatInt(item.SizeBytes, 10),
			"sha256":      item.SHA256,
			"createdAt":   protocolV2AttachmentExtensionTime(item.CreatedAt),
		})
	}
	return protocolV2Document(CommandAttachmentExtensionListOutputSchema, CommandAttachmentExtensionSchemaVersion, map[string]any{
		"planned": planned,
		"total":   strconv.FormatInt(list.Total, 10),
		"page":    strconv.Itoa(list.Page),
		"perPage": strconv.Itoa(list.PerPage),
		"items":   items,
	})
}

func protocolV2AttachmentExtensionTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func protocolV2AttachmentExtensionUnavailable() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
		"host.attachment_extension_runtime_unavailable",
		"The attachment extension reference runtime is unavailable.",
		true,
	)
}

func protocolV2AttachmentExtensionPreparationInvalid() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_INTERNAL,
		"host.attachment_extension_preparation_invalid",
		"The attachment extension reference preparation is unavailable.",
		false,
	)
}

// protocolV2AttachmentExtensionCommandError 把领域错误映射成稳定的 Host 错误码。
// 插件侧据此给出可读提示（例如「当前宿主版本不支持图标上传，请升级 SForum」），
// 而不是静默退回 base64 内联。
func protocolV2AttachmentExtensionCommandError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, extensionreference.ErrNotFound):
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND,
			"host.attachment_not_found",
			"The attachment does not exist or is not active.",
			false,
		)
	case errors.Is(err, extensionreference.ErrForbidden):
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"host.attachment_extension_forbidden",
			"The attachment is not owned by the delegated actor or is already used by another feature.",
			false,
		)
	case errors.Is(err, extensionreference.ErrTooLarge):
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"host.attachment_extension_too_large",
			"The attachment exceeds the effective extension image size limit.",
			false,
		)
	case errors.Is(err, extensionreference.ErrLimitReached):
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"host.attachment_extension_reference_limit",
			"This extension reached its attachment reference limit.",
			false,
		)
	case errors.Is(err, extensionreference.ErrRevisionConflict):
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_CONFLICT,
			"host.attachment_revision_conflict",
			"The attachment revision changed before the command executed.",
			false,
		)
	case errors.Is(err, extensionreference.ErrConflict):
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_CONFLICT,
			"host.attachment_extension_conflict",
			"The extension attachment reference conflicted with a concurrent change.",
			false,
		)
	case errors.Is(err, extensionreference.ErrInvalid):
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"host.attachment_extension_invalid",
			"The attachment cannot be registered as an extension asset.",
			false,
		)
	default:
		return err
	}
}
