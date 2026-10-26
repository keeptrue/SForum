package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

var _ RouteSchemaCatalog = (*extensionopenapi.RouteSchemaCatalog)(nil)

func TestCatalogRouteSchemaValidatorNormalizesMediaAndForwardsResponseStatus(t *testing.T) {
	catalog := &recordingRouteSchemaCatalog{}
	validator := CatalogRouteSchemaValidator{Catalog: catalog}
	step := routes.RouteExecutionStep{
		Action: extensionmanifest.RouteActionAdd, RouteID: "schema.http.route", Method: "POST",
		ContractVersion: "schema.http.route@1", ResponseSchema: "schema.http.response@1",
		Provider: routes.Provider{Kind: routes.ProviderPlugin, Artifact: routes.PluginArtifact{
			ExtensionID: "schema.http", ExtensionVersion: "1.0.0", PackageDigest: string(make([]byte, 64)),
		}},
	}
	if err := validator.ValidateResponse(context.Background(), step, routes.DispatchRequest{Method: "POST"}, routes.DispatchResponse{
		Status: 422, Headers: stdhttp.Header{"Content-Type": {"Application/Problem+JSON; Charset=UTF-8"}}, Body: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if catalog.mediaType != "application/problem+json" || catalog.responseStatus != 422 ||
		catalog.direction != "response" || catalog.actualMethod != "POST" {
		t.Fatalf("catalog=%#v", catalog)
	}
}

func TestCatalogRouteSchemaValidatorRejectsMissingAndInvalidMedia(t *testing.T) {
	validator := CatalogRouteSchemaValidator{Catalog: &recordingRouteSchemaCatalog{}}
	step := routes.RouteExecutionStep{
		Action: extensionmanifest.RouteActionAdd, RouteID: "schema.http.route", Method: "POST",
		ContractVersion: "schema.http.route@1", RequestSchema: "schema.http.request@1",
		Provider: routes.Provider{Kind: routes.ProviderPlugin},
	}
	for _, headers := range []stdhttp.Header{
		{},
		{"Content-Type": {"not a media type"}},
		{"Content-Type": {"text/plain"}},
		{"Content-Type": {"application/json", "text/plain"}},
	} {
		err := validator.ValidateRequest(context.Background(), step, routes.DispatchRequest{Method: "POST", Headers: headers, Body: []byte(`{}`)})
		if !errors.Is(err, ErrRouteSchemaUnavailable) {
			t.Fatalf("headers=%#v error=%v", headers, err)
		}
	}
}

func TestCatalogRouteSchemaValidatorForwardsHEADAgainstDeclaredGET(t *testing.T) {
	catalog := &recordingRouteSchemaCatalog{}
	validator := CatalogRouteSchemaValidator{Catalog: catalog}
	step := routes.RouteExecutionStep{
		Action: extensionmanifest.RouteActionAdd, RouteID: "schema.http.head", Method: "GET",
		ContractVersion: "schema.http.head@1", ResponseSchema: "schema.http.response@1",
		Provider: routes.Provider{Kind: routes.ProviderPlugin},
	}
	if err := validator.ValidateResponse(
		context.Background(), step, routes.DispatchRequest{Method: "HEAD"},
		routes.DispatchResponse{Status: 200, Headers: stdhttp.Header{"Content-Type": {"application/json"}}},
	); err != nil {
		t.Fatal(err)
	}
	if catalog.actualMethod != "HEAD" {
		t.Fatalf("actual method=%q", catalog.actualMethod)
	}
}

type recordingRouteSchemaCatalog struct {
	direction      string
	actualMethod   string
	mediaType      string
	responseStatus int
}

func (c *recordingRouteSchemaCatalog) ValidateRouteSchema(
	_ context.Context,
	_ routes.PluginArtifact,
	direction string,
	_ string,
	_ string,
	actualMethod string,
	_ string,
	_ string,
	_ string,
	mediaType string,
	responseStatus int,
	_ []byte,
) error {
	c.direction = direction
	c.actualMethod = actualMethod
	c.mediaType = mediaType
	c.responseStatus = responseStatus
	return nil
}
