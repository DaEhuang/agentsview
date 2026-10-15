package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/export"
)

func TestExplicitLocalProjectMachinePreservesOwnership(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[remote \"origin\"]\nurl = https://example.com/team/repo.git\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))
	for _, tc := range []struct {
		name     string
		aliases  []string
		resolved bool
	}{
		{"foreign", nil, false}, {"explicit", []string{"bound-device"}, true}, {"other", []string{"other-device"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewEngine(t.Context(), openTestDB(t), EngineConfig{Machine: "installation", LocalProjectMachines: tc.aliases})
			defer engine.Close()
			obs, ok := engine.projectIdentityObservation(db.Session{ID: "session", Project: "repo", Machine: "bound-device", Cwd: root})
			require.True(t, ok)
			reference := export.ResolveProjectReferenceFromObservation(obs, export.IdentityScope{ArchiveID: "archive", ArchiveSalt: "private-salt"})
			assert.Equal(t, tc.resolved, reference.Resolution == export.ProjectResolutionResolved)
			assert.Equal(t, "bound-device", obs.Machine)
			assert.False(t, engine.sameLocalMachineAttribution("bound-device", "installation"), "project probing never adopts another machine's message identity")
		})
	}
}
