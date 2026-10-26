package extensionsruntime_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func TestProtocolV2DatabaseLeaseHelperProcess(t *testing.T) {
	if os.Getenv("SFORUM_PLUGIN_HELPER") != "protocol-v2-no-services" {
		return
	}
	pluginv2sdk.Serve(&protocolV2DatabaseLeaseHelper{Server: pluginv2sdk.NewServer()})
	os.Exit(0)
}

type protocolV2DatabaseLeaseHelper struct {
	*pluginv2sdk.Server
}

func (s *protocolV2DatabaseLeaseHelper) Health(
	ctx context.Context,
	request *protocolwire.HealthRequest,
) (*protocolwire.HealthResponse, error) {
	response, err := s.Server.Health(ctx, request)
	if err != nil || !response.GetHealthy() {
		return response, err
	}
	expectation := os.Getenv("SFORUM_PLUGIN_EXPECT_DATABASE")
	if expectation == "" {
		return response, nil
	}
	for _, key := range []string{
		"SFORUM_DATABASE_URL", "SFORUM_DATABASE_LEASE_ID", "SFORUM_DATABASE_LEASE_EXPIRES_AT",
		"SFORUM_DATABASE_GRANTS", "SFORUM_DATABASE_SCHEMA", "SFORUM_DATABASE_SEARCH_PATH",
	} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			return nil, errors.New("missing exact database lease environment: " + key)
		}
	}
	connectionURL := os.Getenv("SFORUM_DATABASE_URL")
	if expectation == "lease" && (!strings.Contains(connectionURL, "lease_role:lease_secret@") || strings.Contains(connectionURL, "host_secret")) {
		return nil, errors.New("database lease URL contains the wrong authority")
	}
	if expectation != "connect" {
		return response, nil
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := pgx.Connect(connectCtx, connectionURL)
	if err != nil {
		return nil, err
	}
	defer connection.Close(context.Background())
	var currentUser, searchPath string
	if err := connection.QueryRow(connectCtx, `SELECT current_user, current_setting('search_path')`).Scan(&currentUser, &searchPath); err != nil {
		return nil, err
	}
	if currentUser == "" || !strings.Contains(searchPath, os.Getenv("SFORUM_DATABASE_SCHEMA")) {
		return nil, errors.New("database lease role or search_path is not exact")
	}
	return response, nil
}
