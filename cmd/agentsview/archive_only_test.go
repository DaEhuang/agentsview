package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
)

func TestArchiveOnlyRefusesReceivingHostOnRestart(t *testing.T) {
	cfg := testConfigWithClaudeFixture(t)
	cfg.Host = "127.0.0.1"
	database, err := db.Open(t.Context(), cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.EnableArchiveOnly(t.Context()))
	require.NoError(t, database.Close())
	markArchiveStale(t, cfg.DBPath)
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	for range 2 {
		out, err := runRuntimeWarningHelperProcess(t, "serve", "TestServeStaleArchiveHelperProcess",
			[]string{"AGENTSVIEW_STALE_SERVE_CONFIG=" + string(data), "AGENTSVIEW_STALE_SERVE_SOURCES=" + cfg.AgentDirs[parser.AgentClaude][0]}, "listening at")
		require.NoError(t, err, string(out))
		database, err = db.OpenIsolatedContext(t.Context(), cfg.DBPath)
		require.NoError(t, err)
		var count int
		require.NoError(t, database.Reader().QueryRow(t.Context(), "SELECT count(*) FROM sessions").Scan(&count))
		assert.Zero(t, count, "receiver files must not become retired-machine sessions")
		assert.False(t, database.NeedsResync())
		require.NoError(t, database.Close())
	}
	for _, mode := range []string{"startup", "sync", "audit", "resync-build"} {
		var out bytes.Buffer
		err := runSyncWorkerContext(t.Context(), cfg, syncWorkerRequest{Mode: mode}, &out)
		assert.ErrorContains(t, err, "archive-only", mode)
	}
}

func TestArchiveOnlyRefusesRawSyncWatch(t *testing.T) {
	cfg := testConfigWithClaudeFixture(t)
	t.Setenv("AGENTSVIEW_DATA_DIR", cfg.DataDir)
	t.Setenv("AGENTSVIEW_RAW_SYNC_CREDENTIAL", "test-credential")
	database, err := db.Open(t.Context(), cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.EnableArchiveOnly(t.Context()))
	require.NoError(t, database.Close())
	err = runRawSyncWatch(t.Context(), rawSyncWatchConfig{Server: "http://127.0.0.1:1", DeviceID: "original-device", AllowInsecureHTTP: true, Debounce: defaultRawSyncDebounce, Interval: defaultRawSyncAudit, AuditLimit: defaultRawSyncAuditLimit})
	require.ErrorIs(t, err, db.ErrArchiveOnly)
}

func TestArchiveOnlyRefusesRawSyncBackfill(t *testing.T) {
	cfg := testConfigWithClaudeFixture(t)
	t.Setenv("AGENTSVIEW_DATA_DIR", cfg.DataDir)
	t.Setenv("AGENTSVIEW_RAW_SYNC_CREDENTIAL", "test-credential")
	require.NoError(t, os.WriteFile(filepath.Join(cfg.DataDir, "config.toml"),
		[]byte(fmt.Sprintf("[agents.claude]\ndirs = [%q]\n", cfg.AgentDirs[parser.AgentClaude][0])), 0o600))
	database, err := db.Open(t.Context(), cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.EnableArchiveOnly(t.Context()))
	require.NoError(t, database.Close())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected backfill request", http.StatusForbidden)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := newRootCommand()
	cmd.SetContext(ctx)
	_, err = executeCommand(cmd, "raw-sync", "backfill", "--server", server.URL,
		"--device-id", "original-device", "--allow-insecure-http", "--run-id", "archive-test", "--provider", "claude")
	require.ErrorIs(t, err, db.ErrArchiveOnly)
	assert.Zero(t, requests.Load())
	assert.NoFileExists(t, rawSyncCheckpointPath(cfg.DataDir))
}

func TestArchiveOnlyConfigOnlyBackgroundStart(t *testing.T) {
	cfg := testConfigWithClaudeFixture(t)
	cfg.Host, cfg.NoBrowser, cfg.NoSync = "127.0.0.1", true, true
	database, err := db.Open(t.Context(), cfg.DBPath)
	require.NoError(t, err)
	require.NoError(t, database.EnableArchiveOnly(t.Context()))
	require.NoError(t, database.Close())
	previousStart := startServeBackgroundProcessForRun
	t.Cleanup(func() { startServeBackgroundProcessForRun = previousStart })
	// Re-exec the test binary through the real background launcher. The helper
	// runs the real foreground entry point; only executable argument dispatch
	// differs from the release binary.
	startServeBackgroundProcessForRun = func(ctx context.Context, childCfg config.Config, _ []string) (*exec.Cmd, string, error) {
		data, err := json.Marshal(childCfg)
		require.NoError(t, err)
		t.Setenv("AGENTSVIEW_STALE_SERVE_CONFIG", string(data))
		t.Setenv("AGENTSVIEW_STALE_SERVE_SOURCES", childCfg.AgentDirs[parser.AgentClaude][0])
		return startServeBackgroundProcess(ctx, childCfg, []string{"-test.run=^TestServeStaleArchiveHelperProcess$"})
	}
	result, err := startServeBackground(t.Context(), cfg, []string{"serve"}, serveReplacementOptions{}, backgroundLaunchPolicy{ConfigOnly: true, Context: t.Context()})
	require.NoError(t, err)
	require.NotNil(t, result.Runtime)
	t.Cleanup(func() { _ = stopDaemonProcess(result.Runtime.Record, 5*time.Second) })
	client := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, urlFromDaemonRuntime(result.Runtime)+"/api/v1/sessions", nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, stopDaemonProcess(result.Runtime.Record, 5*time.Second))
	database, err = db.OpenIsolatedContext(t.Context(), filepath.Join(cfg.DataDir, "sessions.db"))
	require.NoError(t, err)
	defer database.Close()
	var count int
	require.NoError(t, database.Reader().QueryRow(t.Context(), "SELECT count(*) FROM sessions").Scan(&count))
	assert.Zero(t, count)
}
