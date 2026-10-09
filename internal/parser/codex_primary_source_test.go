package parser

import (
	"database/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestCodexNativeThreadIndexDoesNotExcludeHistoricalRollouts(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "archived_sessions")
	id := "01000000-0000-7000-8000-000000000001"
	older := filepath.Join(root, "rollout-2026-09-01T10-00-00-"+id+".jsonl")
	primary := filepath.Join(root, "rollout-2026-09-01T11-00-00-"+id+"_01000000-0000-7000-8000-000000000002.jsonl")
	writeSourceFile(t, older, `{"type":"session_meta","payload":{"id":"`+id+`"}}`+"\n")
	shadow := filepath.Join(root, "rollout-2026-09-01T10-30-00-"+id+"_01000000-0000-7000-8000-000000000003.jsonl")
	writeSourceFile(t, shadow, `{"type":"session_meta","payload":{"id":"`+id+`"}}`+"\n")
	writeSourceFile(t, primary, `{"type":"session_meta","payload":{"id":"`+id+`"}}`+"\n")
	d, err := sql.Open("sqlite3", filepath.Join(home, "state_5.sqlite"))
	require.NoError(t, err)
	_, err = d.Exec("CREATE TABLE threads(id TEXT,rollout_path TEXT)")
	require.NoError(t, err)
	_, err = d.Exec("INSERT INTO threads VALUES(?,?)", id, primary)
	require.NoError(t, err)
	require.NoError(t, d.Close())
	sources := newCodexSourceSet(AgentCodex, []string{root})
	all, err := sources.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, all, 3)
	var streamed []SourceRef
	require.NoError(t, sources.DiscoverEach(t.Context(), func(s SourceRef) error { streamed = append(streamed, s); return nil }))
	require.Len(t, streamed, 3)
	_, ok := sources.directPathSource(root, older, true)
	assert.True(t, ok)
	assert.FileExists(t, older, "a shadowed original is never removed")
	renamed := filepath.Join(root, "rollout-retained-copy.jsonl")
	writeSourceFile(t, renamed, `{"type":"session_meta","payload":{"id":"`+id+`"}}`+"\n")
	source, ok := sources.directPathSource(root, renamed, true)
	require.True(t, ok, "an unrecognized filename is decided by the parsed native identity")
	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	outcome, err := provider.Parse(t.Context(), ParseRequest{Source: source})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	assert.False(t, outcome.Results[0].Result.Session.CanonicalDialogueSource)
	fallback := newCodexSourceSet(AgentTraeX, []string{root})
	assert.False(t, fallback.isBoundRollout(older, id), "other providers do not inherit Codex repair")
	assert.True(t, sources.isBoundRollout(older, id))
	assert.True(t, sources.isBoundRollout(primary, id))
}
