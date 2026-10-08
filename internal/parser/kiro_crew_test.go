package parser

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func crewFixture(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func crewParseSource(t *testing.T, provider Provider, source SourceRef) ParseResult {
	t.Helper()
	fp, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	out, err := provider.Parse(t.Context(), ParseRequest{Source: source, Fingerprint: fp, Machine: "local"})
	require.NoError(t, err)
	require.Len(t, out.Results, 1)
	return out.Results[0].Result
}

func TestCrewNativeDialogueSurvivesRotationAndKeepsMessageIdentity(t *testing.T) {
	root := t.TempDir()
	old := `{"role":"user","content":"real question","ts":"2026-06-01T12:00:00Z","meta":{"mid":"user-one"}}
{"role":"assistant","content":"working","ts":"2026-06-01T12:00:01Z","meta":{"mid":"progress"}}
{"role":"tool","content":"large private output","ts":"2026-06-01T12:00:02Z","meta":{"mid":"tool"}}
{"role":"assistant","content":"final answer","ts":"2026-06-01T12:00:03Z","meta":{"mid":"answer","turn_stats":{"credits":1}}}
`
	live := crewFixture(t, root, "sessions/example.jsonl", old)
	provider, ok := NewProvider(AgentKiroCrew, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	before := crewParseSource(t, provider, sources[0])
	require.Len(t, before.Messages, 3)
	assert.Equal(t, "commentary", before.Messages[1].SourceSubtype)
	assert.Equal(t, "final_answer", before.Messages[2].SourceSubtype)
	archive := crewFixture(t, root, "sessions/archive/example__20260601.jsonl", old)
	crewFixture(t, root, "sessions/example.jsonl", old+`{"role":"user","content":"next question","ts":"2026-06-01T12:01:00Z","meta":{"mid":"user-two"}}`+"\n")
	sources, err = provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1, "rotated files belong to the same source")
	after := crewParseSource(t, provider, sources[0])
	require.Len(t, after.Messages, 4, "overlapping segments are not duplicate dialogue")
	assert.Equal(t, before.Session.ID, after.Session.ID)
	assert.Equal(t, before.Messages, after.Messages[:3])
	changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{Path: archive, WatchRoot: root, EventKind: "write"})
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, sources[0].Key, changed[0].Key)
	require.NoError(t, os.Remove(live))
	sources, err = provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1, "archived segment remains discoverable without the live file")
	assert.Equal(t, before.Session.ID, crewParseSource(t, provider, sources[0]).Session.ID)
}

func TestCrewCreditsCountEverySurfaceOnceWithoutCLIReplay(t *testing.T) {
	root := t.TempDir()
	row := `{"_type":"tokens","provider":"acp","ts":"2026-06-01T23:59:59+08:00","slot":"example","credits":1,"phase":"session_start","surface":"dashboard"}`
	rows := []string{row, row,
		strings.ReplaceAll(strings.ReplaceAll(row, "23:59:59", "23:59:58"), "session_start", "per_turn"),
		strings.ReplaceAll(strings.ReplaceAll(row, "23:59:59", "23:59:57"), "dashboard", "subagent"),
		strings.ReplaceAll(strings.ReplaceAll(row, "23:59:59", "23:59:56"), "dashboard", "bg:consolidation"),
		strings.ReplaceAll(strings.ReplaceAll(row, "2026-06-01T23:59:59", "2026-06-02T00:00:01"), "dashboard", "bg:suggestions"),
	}
	crewFixture(t, root, "usage/tokens/2026-06-01.jsonl", strings.Join(rows, "\n")+"\n{\"unfinished\":")
	provider, ok := NewProvider(AgentKiroCrew, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	result := crewParseSource(t, provider, sources[0])
	require.Len(t, result.UsageEvents, 5, "equal amounts from distinct events must all count")
	assert.Equal(t, "2026-06-01T16:00:01Z", result.UsageEvents[4].OccurredAt)
	assert.Empty(t, result.Session.FirstMessage)
	for _, msg := range result.Messages {
		var usage map[string]any
		require.NoError(t, json.Unmarshal(msg.TokenUsage, &usage))
		assert.Equal(t, float64(1), usage["credits"])
		assert.Equal(t, "kiro-crew", usage["source"])
		assert.Empty(t, msg.Content)
	}
	assert.Equal(t, result.UsageEvents, crewParseSource(t, provider, sources[0]).UsageEvents)
	cliRoot := t.TempDir()
	log := crewFixture(t, cliRoot, "replay.jsonl", "not read: injected runtime prompt and large replay")
	crewFixture(t, cliRoot, "replay.json", `{"session_id":"replay","session_state":{"agent_name":"kirocrew-example","conversation_metadata":{"user_turn_metadatas":[{"end_timestamp":"2026-06-01T23:59:59+08:00","metering_usage":[{"unit":"credit","value":1}]}]}}}`)
	cli, ok := NewProvider(AgentKiro, ProviderConfig{Roots: []string{cliRoot}})
	require.True(t, ok)
	source, found, err := cli.FindSource(t.Context(), FindSourceRequest{RawSessionID: "replay"})
	require.NoError(t, err)
	require.True(t, found)
	fp, err := cli.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	result = crewParseSource(t, cli, source)
	assert.Empty(t, result.Messages)
	assert.Empty(t, result.UsageEvents)
	assert.Zero(t, result.Session.MessageCount)
	require.NoError(t, os.WriteFile(log, []byte(strings.Repeat("unused replay\n", 10000)), 0o600))
	after, err := cli.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	assert.Equal(t, fp, after, "Crew replay growth does not trigger redundant transcript hashing/import")
}

func TestCrewChangedPathDoesNotSelectUnrelatedSources(t *testing.T) {
	root := t.TempDir()
	row := `{"role":"user","content":"question","ts":"2026-06-01T12:00:00Z","meta":{"mid":"one"}}` + "\n"
	live := crewFixture(t, root, "sessions/selected.jsonl", row)
	provider, ok := NewProvider(AgentKiroCrew, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	for _, size := range []int{1, 1000} {
		for i := 0; i < size; i++ {
			crewFixture(t, root, fmt.Sprintf("sessions/unrelated-%d.jsonl", i), "malformed unrelated source\n")
			crewFixture(t, root, fmt.Sprintf("sessions/archive/unrelated-%d__old.jsonl", i), "malformed unrelated source\n")
		}
		changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{Path: live, WatchRoot: root, EventKind: "write"})
		require.NoError(t, err)
		require.Len(t, changed, 1)
		result := crewParseSource(t, provider, changed[0])
		assert.Equal(t, "kiro-crew:dialogue:selected", result.Session.ID)
		require.Len(t, result.Messages, 1)
	}
	private := crewFixture(t, root, "settings/secrets.jsonl", row)
	changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{Path: private, WatchRoot: root, EventKind: "write"})
	require.NoError(t, err)
	assert.Empty(t, changed)
}
