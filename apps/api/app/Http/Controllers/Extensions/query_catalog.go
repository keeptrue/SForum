package extensionscontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

const queryCatalogUnavailableReason = "extensions.query_catalog_unavailable"

// QueryCatalogSource 是密封 Core 查询目录的只读边界。生产装配注入启动时
// 构造的 *hostapi.QueryRegistryCoreCatalog；同构替身即可测试。
type QueryCatalogSource interface {
	Publication() queryregistry.Publication
	Bindings() []hostapi.QueryRegistryProtocolV2Binding
	Schemas() []queryregistry.JSONResultSchemaBinding
	Artifact() queryregistry.Artifact
	CostMaximum() int
}

// queryCatalogEntryView 与 Query Registry 目录声明同口径，附 Host 侧
// query id/plan version 与结果 schema 摘要，供管理员核对「目录里有哪些
// 稳定查询、各自需要什么权限形态」。
type queryCatalogEntryView struct {
	QueryID            string   `json:"queryId"`
	HostQueryID        string   `json:"hostQueryId"`
	HostPlanVersion    string   `json:"hostPlanVersion"`
	Entity             string   `json:"entity"`
	Fields             []string `json:"fields"`
	Filters            []string `json:"filters"`
	Sort               []string `json:"sort"`
	Pagination         string   `json:"pagination"`
	PermissionPolicy   string   `json:"permissionPolicy"`
	ResultSchema       string   `json:"resultSchema"`
	ResultSchemaDigest string   `json:"resultSchemaDigest"`
}

type queryCatalogResponse struct {
	ExtensionID      string                  `json:"extensionId"`
	ExtensionVersion string                  `json:"extensionVersion"`
	ArtifactDigest   string                  `json:"artifactDigest"`
	CostMaximum      int                     `json:"costMaximum"`
	Total            int                     `json:"total"`
	Queries          []queryCatalogEntryView `json:"queries"`
}

// queryCatalog 返回密封 Core Query 目录的只读快照（stable + shortcode 投影）。
func (h *Controller) queryCatalog(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.queryCatalogAPI == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, queryCatalogUnavailableReason)
	}
	source := h.queryCatalogAPI
	publication := source.Publication()
	artifact := source.Artifact()
	declarations := make(map[string]queryregistry.QueryDeclaration, len(publication.Queries))
	for _, item := range publication.Queries {
		declarations[item.ID] = item
	}
	schemaDigests := make(map[string]string, len(publication.Queries))
	for _, item := range source.Schemas() {
		schemaDigests[item.QueryID+"\x00"+item.PlanVersion] = item.SchemaDigest
	}
	response := queryCatalogResponse{
		ExtensionID:      artifact.ExtensionID,
		ExtensionVersion: artifact.ExtensionVersion,
		ArtifactDigest:   artifact.PackageDigest,
		CostMaximum:      source.CostMaximum(),
		Total:            len(source.Bindings()),
		Queries:          make([]queryCatalogEntryView, 0, len(source.Bindings())),
	}
	for _, binding := range source.Bindings() {
		declaration, ok := declarations[binding.QueryID]
		if !ok {
			return fiber.NewError(fiber.StatusServiceUnavailable, queryCatalogUnavailableReason)
		}
		view := queryCatalogEntryView{
			QueryID:            binding.QueryID,
			HostQueryID:        binding.HostQueryID,
			HostPlanVersion:    binding.HostPlanVersion,
			Entity:             declaration.Entity,
			Fields:             append([]string(nil), declaration.Fields...),
			Filters:            append([]string(nil), declaration.Filters...),
			Sort:               append([]string(nil), declaration.Sort...),
			Pagination:         declaration.Pagination,
			PermissionPolicy:   declaration.PermissionPolicy,
			ResultSchema:       binding.ResultSchema,
			ResultSchemaDigest: schemaDigests[binding.QueryID+"\x00"+binding.PlanVersion],
		}
		response.Queries = append(response.Queries, view)
	}
	return apphttp.OK(c, response)
}
