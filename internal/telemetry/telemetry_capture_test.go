package telemetry

import (
	"encoding/json"
	"errors"
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

	endpoint, captured := captureCollector(t)
	reporter := captureReporter(t, endpoint, Options{
		AgentTypes: []string{"freebuff"}, InsightKinds: []string{"daily_activity"},
		ClaimScreenView: func(_ string, _ time.Time, send func() error) (bool, error) { return true, send() },
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
		{EventScreenViewed, "screen", "sessions", true},
		{EventScreenViewed, "screen", "usage", true},
		{EventScreenViewed, "screen", "activity", true},
		{EventScreenViewed, "screen", "trends", true},
		{EventScreenViewed, "screen", "recall", true},
		{EventScreenViewed, "screen", "quality", true},
		{EventScreenViewed, "screen", "pinned", true},
		{EventScreenViewed, "screen", "trash", true},
		{EventScreenViewed, "screen", "recent-edits", true},
		{EventScreenViewed, "screen", "data", true},
		{EventScreenViewed, "screen", "settings", true},
		{EventScreenViewed, "screen", "unknown", false},
		{EventScreenViewed, "surface", "terminal", false},
	}
	for _, c := range cases {
		properties := map[string]any{c.key: c.value, "query": "secret prompt"}
		if c.event == EventScreenViewed {
			// Each row checks filtering independently of daily deduplication.
			reporter.screenViews = make(map[string]bool)
			if c.key == "surface" {
				properties["screen"] = "sessions"
			}
		}
		body, err := json.Marshal(map[string]any{"event": c.event, "properties": properties})
		require.NoError(t, err)
		postCapture(t, srv.Handler(), string(body), http.StatusAccepted)
	}
	reporter.screenViews = make(map[string]bool)
	post := func(body string, status int) { postCapture(t, srv.Handler(), body, status) }
	body := `{"event":"screen_viewed","properties":{"screen":"sessions","surface":"web"}}`
	post(`{"event":"screen_viewed"}`, 202)
	reporter.claimScreenView = func(_ string, _ time.Time, _ func() error) (bool, error) {
		return false, errors.New("claim failed")
	}
	post(body, 500)
	reporter.claimScreenView = func(_ string, _ time.Time, send func() error) (bool, error) { return true, send() }
	t.Setenv(EnabledEnv, "0")
	post(body, 202)
	t.Setenv(EnabledEnv, "1")
	post(body, 202)
	for _, enabled := range []string{"0", "1"} {
		t.Setenv(EnabledEnv, enabled)
		for _, tc := range []struct {
			body   string
			status int
		}{
			{`{"event":`, 400},
			{`{"event":42}`, 400},
			{`{"event":"screen_viewed","event":null,"properties":{"screen":"sessions"}}`, 400},
			{`{"e_vent":"screen_viewed","properties":{"screen":"sessions"}}`, 202},
			{`{"Event":"screen_viewed","Properties":{"screen":"sessions"}}`, 202},
			{`{"event":"unknown","event":"screen_viewed","properties":{"screen":"sessions"}}`, 202},
			{"{\"event\":\"screen_viewed\",\"properties\":{\"screen\":\"sessions\",\"unused\":\"\xff\"}}", 202},
		} {
			post(tc.body, tc.status)
		}
	}
	post(`{"event":" screen_viewed ","properties":{"screen":"sessions","surface":"web"}}`, 202)
	for _, contentType := range []string{"", "text/plain"} {
		for _, event := range []string{EventAppOpened, EventScreenViewed} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/telemetry/events",
				strings.NewReader(`{"event":"`+event+`","properties":{"screen":"usage"}} {}`))
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			reporter.CaptureHandler().ServeHTTP(rec, req)
			assert.Equal(t, http.StatusAccepted, rec.Code, "%s %s", contentType, event)
		}
	}
	reporter.claimScreenView = func(_ string, _ time.Time, send func() error) (bool, error) {
		if err := send(); err != nil {
			return false, err
		}
		return true, errors.New("saving claim failed")
	}
	post(`{"event":"screen_viewed","properties":{"screen":"settings"}}`, 202)
	post(`{"event":"screen_viewed","properties":{"screen":"settings"}}`, 202)
	reporter.claimScreenView = func(_ string, _ time.Time, send func() error) (bool, error) { return true, send() }
	reporter.screenDay = time.Now().UTC().Add(-24 * time.Hour).Format(time.DateOnly)
	post(body, 202)
	require.NoError(t, reporter.Close())

	sent := captured()
	require.Len(t, sent, len(cases)-1+6)
	i := 0
	for _, c := range cases {
		if c.event == EventScreenViewed && c.key == "screen" && !c.kept {
			continue
		}
		assert.NotContains(t, sent[i], "query", c.event)
		value, ok := sent[i][c.key]
		if c.kept {
			assert.Equal(t, c.value, value, c.event)
		} else {
			assert.False(t, ok, "%s %s=%v should be dropped", c.event, c.key, value)
		}
		i++
	}
	flow := sent[len(cases)-1:]
	assert.Equal(t, "web", flow[0]["surface"])
	var screens []string
	for _, item := range flow {
		if screen, ok := item["screen"].(string); ok {
			screens = append(screens, screen)
		}
	}
	assert.Equal(t, []string{"sessions", "usage", "settings", "sessions"}, screens)
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
		if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&payload)) {
			http.Error(w, "invalid capture batch", http.StatusBadRequest)
			return
		}
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
