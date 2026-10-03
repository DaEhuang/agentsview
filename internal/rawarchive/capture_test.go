package rawarchive

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/dbtest"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/rawcheckpoint"
)

func captureFixture(t *testing.T) CaptureOptions {
	t.Helper()
	data := t.TempDir()
	database, err := db.OpenIsolatedContext(t.Context(), filepath.Join(data, "sessions.db"))
	require.NoError(t, err)
	require.NoError(t, database.Close())
	root := t.TempDir()
	dbtest.WriteTestFile(t, filepath.Join(root, "projects", "project-a", "saved.jsonl"), []byte("original bytes\n"))
	return CaptureOptions{DataDir: data, Destination: filepath.Join(t.TempDir(), "capture"), Roots: []RootSpec{{Provider: "claude", Path: filepath.Join(root, "projects")}}, Settings: RecoverySettings{LocalMachineName: "source-device"}, ReaderBuild: "test-build"}
}

func TestCaptureIdentityContinuity(t *testing.T) {
	opts := captureFixture(t)
	first, err := Capture(t.Context(), opts)
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(opts.DataDir, "telemetry-install-id"), "capture never mints an identity in source state")
	require.Len(t, first.NewIdentities, 2)
	opts.IdentityFrom = filepath.Join(opts.Destination, "capture.json")
	opts.Destination = filepath.Join(t.TempDir(), "again")
	opts.Settings.LocalMachineName = "renamed-label"
	second, err := Capture(t.Context(), opts)
	require.NoError(t, err)
	assert.Equal(t, first.Source.DeviceID, second.Source.DeviceID)
	assert.Equal(t, first.Source.Roots[0].ID, second.Source.Roots[0].ID)
	assert.Empty(t, second.NewIdentities)
	assert.Equal(t, "renamed-label", second.Source.Machine)
	dbtest.WriteTestFile(t, filepath.Join(opts.DataDir, "telemetry-install-id"), []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	opts.Destination = filepath.Join(t.TempDir(), "wrong-installation")
	_, err = Capture(t.Context(), opts)
	require.ErrorContains(t, err, "different installation")
	assert.NoDirExists(t, opts.Destination)
}

func TestCapturePreservesRawSyncRoot(t *testing.T) {
	opts := captureFixture(t)
	path := filepath.Join(opts.DataDir, "raw-sync", "checkpoint.db")
	store, err := rawcheckpoint.Open(t.Context(), path)
	require.NoError(t, err)
	require.NoError(t, store.SetDevice(t.Context(), "hosted-device"))
	root, err := store.ResolveConfiguredRoot(t.Context(), parser.AgentClaude, opts.Roots[0].Path)
	require.NoError(t, err)
	defer store.Close()
	descriptor, err := Capture(t.Context(), opts)
	require.NoError(t, err)
	assert.Equal(t, root.ID, descriptor.Source.Roots[0].ID)
	require.NotNil(t, descriptor.RawSync)
	assert.Equal(t, "hosted-device", descriptor.RawSync.DeviceID)
	assert.Equal(t, root.LocalPath, descriptor.RawSync.Roots[0].LocalPath)
	assert.NoFileExists(t, filepath.Join(opts.Destination, "roots", "application", "raw-sync", "checkpoint.db"))
}

func TestCaptureRejectsIncompletePackage(t *testing.T) {
	for _, fault := range []string{"missing", "changed", "extra", "version", "report"} {
		t.Run(fault, func(t *testing.T) {
			opts := captureFixture(t)
			d, err := Capture(t.Context(), opts)
			require.NoError(t, err)
			descriptor := filepath.Join(opts.Destination, "capture.json")
			transcript := filepath.Join(opts.Destination, d.Source.Roots[0].Path, "projects", "project-a", "saved.jsonl")
			switch fault {
			case "missing":
				require.NoError(t, os.Remove(transcript))
			case "changed":
				require.NoError(t, os.WriteFile(transcript, []byte("changed\n"), 0o600))
			case "extra":
				dbtest.WriteTestFile(t, filepath.Join(opts.Destination, d.Source.Roots[0].Path, "extra"), []byte("not inventoried"))
			case "version":
				d.Version = 2
			case "report":
				d.WritersStopped = !d.WritersStopped
			}
			if fault == "version" || fault == "report" {
				data, err := json.Marshal(d)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(descriptor, data, 0o600))
			}
			_, err = LoadImportSpec(t.Context(), descriptor)
			require.Error(t, err)
		})
	}
}

func TestCapturePreflightBlocksUnknownAndDeletedProjection(t *testing.T) {
	for _, condition := range []string{"absent", "trashed"} {
		t.Run(condition, func(t *testing.T) {
			opts := captureFixture(t)
			if condition == "absent" {
				require.NoError(t, os.Remove(filepath.Join(opts.DataDir, "sessions.db")))
			} else {
				database, err := db.OpenIsolatedContext(t.Context(), filepath.Join(opts.DataDir, "sessions.db"))
				require.NoError(t, err)
				require.NoError(t, database.UpsertSession(t.Context(), db.Session{ID: "trashed", Agent: "claude", Project: "project-a", Machine: "source"}))
				require.NoError(t, database.SoftDeleteSession(t.Context(), "trashed"))
				require.NoError(t, database.Close())
			}
			d, err := Capture(t.Context(), opts)
			require.NoError(t, err)
			if condition == "absent" {
				assert.Nil(t, d.Preflight.Counts["trashed"])
				assert.NotEmpty(t, d.Preflight.Unknown)
			} else {
				require.NotNil(t, d.Preflight.Counts["trashed"])
				assert.EqualValues(t, 1, *d.Preflight.Counts["trashed"])
			}
			_, err = LoadImportSpec(t.Context(), filepath.Join(opts.Destination, "capture.json"))
			require.Error(t, err)
		})
	}
}

func TestCaptureDoesNotPublishFailure(t *testing.T) {
	for _, condition := range []string{"nested", "existing", "vault", "raw-vault", "canceled", "symlink-root", "invalid-identity", "missing-asset"} {
		t.Run(condition, func(t *testing.T) {
			opts := captureFixture(t)
			ctx := t.Context()
			switch condition {
			case "nested":
				opts.Destination = filepath.Join(opts.Roots[0].Path, "capture")
			case "existing":
				require.NoError(t, os.Mkdir(opts.Destination, 0o700))
				dbtest.WriteTestFile(t, filepath.Join(opts.Destination, "keep"), []byte("keep"))
			case "vault":
				require.NoError(t, os.Mkdir(filepath.Join(opts.DataDir, "artifacts"), 0o700))
			case "raw-vault":
				require.NoError(t, os.Mkdir(filepath.Join(opts.DataDir, Directory), 0o700))
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "symlink-root":
				alias := filepath.Join(t.TempDir(), "alias")
				require.NoError(t, os.Symlink(opts.Roots[0].Path, alias))
				opts.Roots[0].Path = alias
			case "invalid-identity":
				dbtest.WriteTestFile(t, filepath.Join(opts.DataDir, "telemetry-install-id"), nil)
			case "missing-asset":
				database, err := db.OpenIsolatedContext(ctx, filepath.Join(opts.DataDir, "sessions.db"))
				require.NoError(t, err)
				require.NoError(t, database.UpsertSession(ctx, db.Session{ID: "image", Agent: "claude", Machine: "source", Project: "project-a"}))
				require.NoError(t, database.InsertMessages(ctx, []db.Message{{SessionID: "image", Ordinal: 0, Role: "user", Content: "![image](asset://" + strings.Repeat("a", 64) + ".png)"}}))
				require.NoError(t, database.Close())
			}
			_, err := Capture(ctx, opts)
			require.Error(t, err)
			if condition == "existing" {
				assert.FileExists(t, filepath.Join(opts.Destination, "keep"))
			} else {
				assert.NoDirExists(t, opts.Destination)
			}
		})
	}
}

func TestCaptureRejectsChangedFile(t *testing.T) {
	source := filepath.Join(t.TempDir(), "transcript.jsonl")
	dbtest.WriteTestFile(t, source, []byte("before"))
	info, err := os.Stat(source)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(source, []byte("changed after enumeration"), 0o600))
	target := filepath.Join(t.TempDir(), "copy")
	_, err = captureFile(t.Context(), source, target, "source", "transcript.jsonl", info)
	require.ErrorContains(t, err, "source changed")
	assert.NoFileExists(t, target)
}
