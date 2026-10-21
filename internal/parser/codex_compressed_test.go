package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestCodexCompressedSnapshotUsesSameParserAndPhysicalFingerprint(t *testing.T) {
	const id = "22222222-2222-4222-8222-222222222222"
	root := t.TempDir()
	body := testjsonl.JoinJSONL(testjsonl.CodexSessionMetaJSON(id, "/workspace/project", "codex_cli_rs", tsEarly), testjsonl.CodexMsgJSON("user", "question", tsEarlyS1), testjsonl.CodexMsgJSON("assistant", "answer", tsEarlyS5))
	path := filepath.Join(root, "rollout-2026-06-01T10-00-00-"+id+".jsonl.zst")
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	require.NoError(t, err)
	defer encoder.Close()
	compressed := encoder.EncodeAll([]byte(body), nil)
	require.NoError(t, os.WriteFile(path, compressed, 0600))
	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	source := requireCodexProviderSource(t, provider, id)
	fp, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	parsed, err := provider.Parse(t.Context(), ParseRequest{Source: source, Fingerprint: fp})
	require.NoError(t, err)
	require.Len(t, parsed.Results, 1)
	result := parsed.Results[0].Result
	require.Len(t, result.Messages, 2)
	require.Equal(t, "codex:"+id, result.Session.ID)
	require.True(t, result.Session.CanonicalDialogueSource)
	require.Equal(t, int64(len(compressed)), result.Session.File.Size)
	hash := sha256.Sum256(compressed)
	require.Equal(t, hex.EncodeToString(hash[:]), result.Session.File.Hash)
	require.Empty(t, result.Checkpoint)
	_, status, err := provider.(*codexProvider).ParseIncremental(t.Context(), IncrementalRequest{Source: source, Fingerprint: fp})
	require.NoError(t, err)
	require.Equal(t, IncrementalNeedsFullParse, status)
	// Plain and compressed copies retain one discovery identity.
	require.NoError(t, os.WriteFile(codexPlainName(path), []byte(body), 0600))
	require.Equal(t, id, CodexRolloutDiscoveryID(filepath.Base(path)))
	require.Equal(t, id, CodexThreadIDFromFilename(filepath.Base(path)))
	// A broken compressed frame must not publish partial dialogue.
	require.NoError(t, os.WriteFile(path, compressed[:len(compressed)-4], 0600))
	_, err = provider.Parse(t.Context(), ParseRequest{Source: source})
	require.Error(t, err)
}
