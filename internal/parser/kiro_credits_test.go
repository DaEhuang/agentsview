package parser

import (
	"encoding/json/v2"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiroCreditAccountingUsesDatedNativeCredits(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "credit-session.jsonl")
	writeSourceFile(t, path, kiroProviderJSONLFixture("question"))
	writeSourceFile(t, filepath.Join(root, "credit-session.json"), `{"session_id":"credit-session","session_state":{"agent_name":"kiro_default","conversation_metadata":{"user_turn_metadatas":[{"end_timestamp":"2026-06-01T23:59:59+08:00","metering_usage":[{"unit":"credit","value":1},{"unit":"other","value":999}]},{"end_timestamp":"2026-06-02T00:00:01+08:00","metering_usage":[{"unit":"credit","value":1000}]}]}}}`)
	provider, ok := NewProvider(AgentKiro, ProviderConfig{Roots: []string{root}, Machine: "local"})
	require.True(t, ok)
	source, found, err := provider.FindSource(t.Context(), FindSourceRequest{RawSessionID: "credit-session"})
	require.NoError(t, err)
	require.True(t, found)
	fingerprint, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	outcome, err := provider.Parse(t.Context(), ParseRequest{Source: source, Fingerprint: fingerprint, Machine: "local"})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	result := outcome.Results[0].Result
	assert.Equal(t, "kiro-cli", result.Session.AgentLabel)
	require.Len(t, result.UsageEvents, 2)
	events := result.UsageEvents
	assert.Equal(t, "2026-06-01T15:59:59Z", events[0].OccurredAt)
	assert.Equal(t, "2026-06-01T16:00:01Z", events[1].OccurredAt)
	assert.Equal(t, 92593, events[0].InputTokens+events[0].CacheReadInputTokens+events[0].OutputTokens)
	assert.Equal(t, 92592593, events[1].InputTokens+events[1].CacheReadInputTokens+events[1].OutputTokens)
	assert.EqualValues(t, 40_000_000, events[1].Cost.Microdollars)
	assert.Equal(t, "estimated", events[1].CostStatus)
	assert.Equal(t, kiroCreditEstimateModel, events[1].Model)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(result.Messages[len(result.Messages)-1].TokenUsage, &payload))
	assert.Equal(t, float64(1000), payload["credits"])
	assert.Equal(t, "credit", payload["measurement"])
	_, nativeInput := payload["input_tokens"]
	assert.False(t, nativeInput)
	again, err := provider.Parse(t.Context(), ParseRequest{Source: source, Fingerprint: fingerprint, Machine: "local"})
	require.NoError(t, err)
	assert.Equal(t, result.UsageEvents, again.Results[0].Result.UsageEvents)
}

func TestKiroCreditAccountingRejectsInvalidDatedMeters(t *testing.T) {
	for _, payload := range []string{
		`{"session_state":{"conversation_metadata":{"user_turn_metadatas":[{"end_timestamp":"invalid","metering_usage":[{"unit":"credit","value":1}]}]}}}`,
		`{"session_state":{"conversation_metadata":{"user_turn_metadatas":[{"end_timestamp":"2026-06-01T12:00:00Z","metering_usage":[{"unit":"credit","value":-1}]}]}}}`,
	} {
		var meta kiroMeta
		require.NoError(t, json.Unmarshal([]byte(payload), &meta))
		_, _, err := kiroCreditAccounting("kiro:example", 0, &meta)
		require.Error(t, err)
	}
}
