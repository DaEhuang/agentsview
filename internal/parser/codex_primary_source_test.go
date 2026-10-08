package parser

import (
	"database/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestCodexPrimarySourceUsesNativeThreadIndex(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "archived_sessions")
	id := "01000000-0000-7000-8000-000000000001"
	older := filepath.Join(root, "rollout-2026-09-01T10-00-00-"+id+".jsonl")
	primary := filepath.Join(root, "rollout-2026-09-01T11-00-00-"+id+"_01000000-0000-7000-8000-000000000002.jsonl")
	writeSourceFile(t, older, `{"type":"session_meta","payload":{"id":"`+id+`"}}`+"\n")
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
	require.Len(t, all, 1)
	assert.Equal(t, primary, all[0].DisplayPath)
	var streamed []SourceRef
	require.NoError(t, sources.DiscoverEach(t.Context(), func(s SourceRef) error { streamed = append(streamed, s); return nil }))
	require.Len(t, streamed, 1)
	assert.Equal(t, primary, streamed[0].DisplayPath)
	_, ok := sources.directPathSource(root, older, true)
	assert.False(t, ok)
	assert.FileExists(t, older, "a shadowed original is never removed")
	fallback := newCodexSourceSet(AgentTraeX, []string{root})
	assert.True(t, fallback.selectsPath(older, id), "other providers do not inherit Codex metadata")
}
