package extensionscontroller

import (
	"encoding/json"
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
)

const routeContractUnavailableReason = "extensions.route_contract_unavailable"

// RouteContractCatalog is the immutable lifecycle-owned OpenAPI publication.
// Both consumer views must come from one exact snapshot revision.
type RouteContractCatalog interface {
	ContractSnapshot() extensionopenapi.PublishedContractSnapshot
}

type ProxyInput struct {
	Matched             extensions.MatchedRoute
	Actor               identity.Actor
	HasActor            bool
	PublicFrontendExact *PublicFrontendBridgeIdentity
}

type PublicFrontendBridgeIdentity struct {
	ExtensionID      string
	ExtensionVersion string
	PackageDigest    string
	ImpactDigest     string
	ComponentID      string
}

type RouteGateway interface {
	Proxy(c fiber.Ctx, input ProxyInput) error
}

type routeOpenAPIAggregateView struct {
	Revision          uint64                                          `json:"revision"`
	AggregateRevision string                                          `json:"aggregateRevision"`
	Artifacts         []extensionopenapi.PublishedRouteSchemaArtifact `json:"artifacts"`
	Sources           []extensionopenapi.SourceIdentity               `json:"sources"`
	Document          json.RawMessage                                 `json:"document"`
}

type routeGeneratedClientView struct {
	Revision          uint64                                `json:"revision"`
	AggregateRevision string                                `json:"aggregateRevision"`
	Operations        []extensionopenapi.GeneratedOperation `json:"operations"`
}

func (h *Controller) WithRouteContractCatalog(catalog RouteContractCatalog) *Controller {
	h.routeContracts = catalog
	return h
}

func (h *Controller) routeOpenAPIAggregate(c fiber.Ctx) error {
	snapshot, err := h.routeContractSnapshot(c)
	if err != nil {
		return err
	}
	return apphttp.OK(c, routeOpenAPIAggregateView{
		Revision: snapshot.Revision, AggregateRevision: snapshot.AggregateRevision,
		Artifacts: snapshot.Artifacts, Sources: snapshot.Sources, Document: snapshot.Document,
	})
}

func (h *Controller) routeGeneratedClientMetadata(c fiber.Ctx) error {
	snapshot, err := h.routeContractSnapshot(c)
	if err != nil {
		return err
	}
	return apphttp.OK(c, routeGeneratedClientView{
		Revision: snapshot.Revision, AggregateRevision: snapshot.AggregateRevision,
		Operations: snapshot.GeneratedClientOperations,
	})
}

func (h *Controller) routeContractSnapshot(c fiber.Ctx) (extensionopenapi.PublishedContractSnapshot, error) {
	if _, err := h.routeProviderViewer(c); err != nil {
		return extensionopenapi.PublishedContractSnapshot{}, err
	}
	if h.routeContracts == nil {
		return extensionopenapi.PublishedContractSnapshot{}, fiber.NewError(
			fiber.StatusServiceUnavailable, routeContractUnavailableReason,
		)
	}
	snapshot := h.routeContracts.ContractSnapshot()
	if snapshot.AggregateRevision == "" || len(snapshot.Document) == 0 {
		return extensionopenapi.PublishedContractSnapshot{}, fiber.NewError(
			fiber.StatusServiceUnavailable, routeContractUnavailableReason,
		)
	}
	c.Set(fiber.HeaderCacheControl, "private, no-cache")
	c.Set(fiber.HeaderETag, `"`+snapshot.AggregateRevision+`"`)
	c.Set("X-SForum-Route-Contract-Revision", strconv.FormatUint(snapshot.Revision, 10))
	return snapshot, nil
}

var _ RouteContractCatalog = (*extensionopenapi.RouteSchemaPublication)(nil)
