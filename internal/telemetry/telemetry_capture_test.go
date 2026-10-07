package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

	var mu sync.Mutex
	var sent []map[string]any
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Batch []struct {
				Properties map[string]any `json:"properties"`
			} `json:"batch"`
		}
		body, _ := io.ReadAll(r.Body)
		if json.Unmarshal(body, &payload) == nil {
			mu.Lock()
			for _, msg := range payload.Batch {
				sent = append(sent, msg.Properties)
			}
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)

	client, err := kittelemetry.NewPostHogReporter(kittelemetry.PostHogOptions{
		APIKey: "phc_test", Application: application, EnvPrefix: envPrefix,
		DistinctID: "install-id", Source: "daemon", Endpoint: collector.URL,
	}, allowedEventOptions(Options{
		AgentTypes: []string{"freebuff"}, InsightKinds: []string{"daily_activity"},
	})...)
	require.NoError(t, err)
	reporter := &Reporter{client: client}
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
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"http://127.0.0.1:8080/api/v1/telemetry/events", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusAccepted, rec.Code, "%s: %s", body, rec.Body.String())
	}
	require.NoError(t, client.Close())

	mu.Lock()
	defer mu.Unlock()
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
	var sent []map[string]any
	var mu sync.Mutex
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
	defer collector.Close()
	cfg := config.Config{DataDir: t.TempDir(), InstallationID: "install-id", Host: "127.0.0.1", Port: 8080}
	newReporter := func() *Reporter {
		client, err := kittelemetry.NewPostHogReporter(kittelemetry.PostHogOptions{
			APIKey: "phc_test", Application: application, EnvPrefix: envPrefix,
			DistinctID: cfg.InstallationID, Source: "daemon", Endpoint: collector.URL,
		}, allowedEventOptions(Options{})...)
		require.NoError(t, err)
		return &Reporter{client: client, claimScreenView: cfg.ClaimScreenView}
	}
	reporter := newReporter()
	archive := dbtest.OpenTestDB(t)
	post := func(body string) {
		srv := server.New(cfg, archive, nil, server.WithTelemetryCapture(reporter.CaptureHandler()))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"http://127.0.0.1:8080/api/v1/telemetry/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	}
	post(`{"event":"screen_viewed","properties":{"screen":"unknown","surface":"web"}}`)
	post(`{"event":"screen_viewed"}`)
	t.Setenv(EnabledEnv, "0")
	post(`{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web"}}`)
	post(`{"event":" screen_viewed ","properties":{"screen":"sessions","surface":"web"}}`)
	t.Setenv(EnabledEnv, "1")
	post(`{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web","query":"secret"}}`)
	post(`{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web"}}`)
	require.NoError(t, reporter.Close())
	reporter = newReporter()
	post(`{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web"}}`)
	for _, screen := range []string{"usage", "activity", "trends", "recall", "quality", "pinned", "trash", "recent-edits", "data", "settings"} {
		post(`{"event":"screen_viewed","properties":{"screen":"` + screen + `","surface":"terminal"}}`)
	}
	// Exercise the next UTC day through the same production claim operation.
	reporter.claimScreenView = func(screen string, now time.Time) (bool, error) {
		return cfg.ClaimScreenView(screen, now.Add(24*time.Hour))
	}
	post(`{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web"}}`)
	require.NoError(t, reporter.Close())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, sent, 12)
	assert.Equal(t, "sessions", sent[0]["screen"])
	assert.Equal(t, "web", sent[0]["surface"])
	assert.NotContains(t, sent[0], "query")
	for _, item := range sent[1:11] {
		assert.NotContains(t, item, "surface")
	}
}
