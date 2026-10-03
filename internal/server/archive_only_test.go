package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/artifact"
	"go.kenn.io/agentsview/internal/remotesync"
)

func TestArchiveOnlyRejectsManualSync(t *testing.T) {
	f := newSyncRouteFixture(t)
	require.NoError(t, f.db.EnableArchiveOnly(t.Context()))
	for _, path := range []string{"/api/v1/sync", "/api/v1/sync/remotes", "/api/v1/sessions/sync", "/api/v1/push/pg", "/api/v1/push/duckdb"} {
		t.Run(path, func(t *testing.T) {
			var body any = map[string]string{"id": "archived"}
			if path == "/api/v1/sync/remotes" {
				body = remoteSyncRequest{}
			}
			if path == "/api/v1/push/pg" || path == "/api/v1/push/duckdb" {
				body = daemonPushRequest{}
			}
			response := serveJSON(t, f.handler, http.MethodPost, path, body)
			assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), "archive-only")
		})
	}
	f.srv.cfg.AuthToken = "test-token"
	transferServer := New(f.srv.cfg, f.db, nil, WithArtifactExchangeRunner(func(context.Context, ArtifactExchangeRequest) (artifact.SyncResult, error) {
		panic("archive-only exchange must not run")
	}))
	// Source-transfer requests must fail before resolving roots or touching a target.
	handler := transferServer.Handler()
	for _, path := range []string{"/api/v1/remote-sync/manifest", "/api/v1/remote-sync/archive", "/api/v1/artifacts/exchange"} {
		response := serveJSON(t, handler, http.MethodPost, path, map[string]any{}, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer test-token")
			remotesync.SetProtocolHeader(r.Header)
		})
		assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), "archive-only")
	}

	// Read commands wait for startup through this endpoint. An archive-only
	// daemon has no startup ingestion to wait for.
	response := serveJSON(t, f.handler, http.MethodPost, "/api/v1/sync?startup_only=true", nil)
	assert.Equal(t, http.StatusOK, response.Code, response.Body.String())
}
