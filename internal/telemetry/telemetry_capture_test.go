package telemetry

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kittelemetry "go.kenn.io/kit/telemetry"

	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/dbtest"
	"go.kenn.io/agentsview/internal/server"
)

func TestCoreActionAllowlist(t *testing.T) {
	t.Setenv(EnabledEnv, "1")
	t.Setenv(GenericEnabledEnv, "1")

	endpoint, captured := captureCollector(t)
	reporter := captureReporter(t, endpoint, Options{
		AgentTypes: []string{"freebuff"}, InsightKinds: []string{"daily_activity"},
	})
	srv := server.New(config.Config{Host: "127.0.0.1", Port: 8080},
		dbtest.OpenTestDB(t), nil, server.WithTelemetryCapture(reporter.CaptureHandler()))
	cases := []struct {
		event, key, value string
		kept              bool
	}{
		{EventSearchRun, "query_type", "semantic", true},
		{EventSearchRun, "query_type", "regex", false},
		{EventSessionViewed, "agent", "freebuff", true},
		{EventSessionViewed, "agent", "/Users/alice/secret", false},
		{EventExportRun, "format", "markdown_link", true},
		{EventExportRun, "format", "pdf", false},
		{EventInsightGenerated, "kind", "daily_activity", true},
		{EventInsightGenerated, "kind", "llm_canned", false},
		{EventAnalyticsViewed, "page", "trends", true},
		{EventAnalyticsViewed, "page", "sessions", false},
	}
	for _, c := range cases {
		body, err := json.Marshal(map[string]any{"event": c.event, "properties": map[string]any{c.key: c.value, "query": "secret prompt"}})
		require.NoError(t, err)
		postCapture(t, srv.Handler(), string(body), http.StatusAccepted)
	}
	require.NoError(t, reporter.Close())

	sent := captured()
	require.Len(t, sent, len(cases))
	for i, c := range cases {
		assert.NotContains(t, sent[i], "query", c.event)
		value, ok := sent[i][c.key]
		if c.kept {
			assert.Equal(t, c.value, value, c.event)
		} else {
			assert.False(t, ok, "%s %s=%v should be dropped", c.event, c.key, value)
		}
	}
}

func TestScreenViewedCapture(t *testing.T) {
	t.Setenv(EnabledEnv, "1")
	t.Setenv(GenericEnabledEnv, "1")
	endpoint, captured := captureCollector(t)
	cfg := config.Config{DataDir: t.TempDir(), InstallationID: "install-id", Host: "127.0.0.1", Port: 8080}
	opts := Options{InstallationID: cfg.InstallationID, ClaimScreenView: cfg.ClaimScreenView}
	reporter := captureReporter(t, endpoint, opts)
	archive := dbtest.OpenTestDB(t)
	post := func(body string, status int) {
		srv := server.New(cfg, archive, nil, server.WithTelemetryCapture(reporter.CaptureHandler()))
		postCapture(t, srv.Handler(), body, status)
	}
	post(`{"event":"screen_viewed","properties":{"screen":"unknown","surface":"web"}}`, 202)
	post(`{"event":"screen_viewed"}`, 202)
	reporter.claimScreenView = func(screen string, now time.Time, _ func() error) (bool, error) {
		return cfg.ClaimScreenView(screen, now, func() error { return errors.New("queue full") })
	}
	post(`{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web"}}`, 500)
	reporter.claimScreenView = cfg.ClaimScreenView
	t.Setenv(EnabledEnv, "0")
	post(`{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web"}}`, 202)
	t.Setenv(EnabledEnv, "1")
	post(`{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web","query":"secret"}}`, 202)
	post(`{"event":" screen_viewed ","properties":{"screen":"sessions","surface":"web"}}`, 202)
	require.NoError(t, reporter.Close())
	reporter = captureReporter(t, endpoint, opts)
	post(`{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web"}}`, 202)
	for _, screen := range []string{"usage", "activity", "trends", "recall", "quality", "pinned", "trash", "recent-edits", "data", "settings"} {
		post(`{"event":"screen_viewed","properties":{"screen":"`+screen+`","surface":"terminal"}}`, 202)
	}
	reporter.claimScreenView = func(screen string, now time.Time, send func() error) (bool, error) {
		return cfg.ClaimScreenView(screen, now.Add(24*time.Hour), send)
	}
	reporter.screenDay = time.Now().UTC().Add(-24 * time.Hour).Format(time.DateOnly)
	post(`{"event":"screen_viewed","properties":{"screen":"usage","surface":"web"}}`, 202)
	require.NoError(t, reporter.Close())
	sent := captured()
	require.Len(t, sent, 12)
	assert.Equal(t, "sessions", sent[0]["screen"])
	assert.Equal(t, "web", sent[0]["surface"])
	assert.NotContains(t, sent[0], "query")
	assert.Equal(t, "usage", sent[11]["screen"])
	for _, item := range sent[1:11] {
		assert.NotContains(t, item, "surface")
	}
}

func TestScreenViewRequestContract(t *testing.T) {
	t.Setenv(EnabledEnv, "1")
	t.Setenv(GenericEnabledEnv, "1")
	endpoint, _ := captureCollector(t)
	for _, contentType := range []string{"", "text/plain", "application/json"} {
		cfg := config.Config{DataDir: t.TempDir(), InstallationID: "install-id"}
		reporter := captureReporter(t, endpoint, Options{ClaimScreenView: cfg.ClaimScreenView})
		for _, event := range []string{EventAppOpened, EventScreenViewed} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events",
				strings.NewReader(`{"event":"`+event+`","properties":{"screen":"sessions"}} {}`))
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			reporter.CaptureHandler().ServeHTTP(rec, req)
			assert.Equal(t, http.StatusAccepted, rec.Code, "%s %s", contentType, event)
		}
		require.NoError(t, reporter.Close())
	}
}

func TestScreenViewAcceptedBeforeWriteFailure(t *testing.T) {
	t.Setenv(EnabledEnv, "1")
	t.Setenv(GenericEnabledEnv, "1")
	endpoint, captured := captureCollector(t)
	cfg := config.Config{DataDir: t.TempDir(), InstallationID: "install-id", Host: "127.0.0.1", Port: 8080}
	path := filepath.Join(cfg.DataDir, "telemetry-screen-views")
	sends := 0
	reporter := captureReporter(t, endpoint, Options{ClaimScreenView: func(screen string, now time.Time, send func() error) (bool, error) {
		return cfg.ClaimScreenView(screen, now, func() error {
			sends++
			if err := send(); err != nil {
				return err
			}
			return os.Mkdir(path, 0o700)
		})
	}})
	srv := server.New(cfg, dbtest.OpenTestDB(t), nil, server.WithTelemetryCapture(reporter.CaptureHandler()))
	body := `{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web"}}`
	postCapture(t, srv.Handler(), body, http.StatusAccepted)
	require.NoError(t, os.Remove(path))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { postCapture(t, srv.Handler(), body, http.StatusAccepted) })
	}
	wg.Wait()
	require.NoError(t, reporter.Close())
	assert.Equal(t, 1, sends)
	assert.Len(t, captured(), 1)
}

func captureCollector(t *testing.T) (string, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var sent []map[string]any
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Batch []struct {
				Properties map[string]any `json:"properties"`
			} `json:"batch"`
		}
		require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
		mu.Lock()
		for _, item := range payload.Batch {
			sent = append(sent, item.Properties)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	return collector.URL, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), sent...)
	}
}

func captureReporter(t *testing.T, endpoint string, opts Options) *Reporter {
	t.Helper()
	client, err := kittelemetry.NewPostHogReporter(kittelemetry.PostHogOptions{
		APIKey: "phc_test", Application: application, EnvPrefix: envPrefix,
		DistinctID: "install-id", Source: "daemon", Endpoint: endpoint,
	}, allowedEventOptions(opts)...)
	require.NoError(t, err)
	return &Reporter{client: client, claimScreenView: opts.ClaimScreenView}
}

func postCapture(t *testing.T, handler http.Handler, body string, status int) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://127.0.0.1:8080/api/v1/telemetry/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, status, rec.Code, rec.Body.String())
}
