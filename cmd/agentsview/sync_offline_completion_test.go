package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	agentsync "go.kenn.io/agentsview/internal/sync"
)

func TestDoSyncOfflineReportsIncompleteProcessing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stats agentsync.SyncStats
		err   error
		fail  bool
	}{
		{name: "complete"},
		{name: "failed", stats: agentsync.SyncStats{Failed: 1}, fail: true},
		{name: "deferred", stats: agentsync.SyncStats{Deferred: 1}, fail: true},
		{name: "aborted", stats: agentsync.SyncStats{Aborted: true}, fail: true},
		{name: "error", err: errors.New("sync failed"), fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newSyncCLIEnv(t)
			t.Setenv("AGENTSVIEW_NO_DAEMON", "1")
			original := coordinateLocalSyncRunner
			coordinateLocalSyncRunner = func(
				context.Context, config.Config, *db.DB, bool, agentsync.ProgressFunc, bool,
				func() (agentsync.RebuildOptions, agentsync.RebuildCleanup, error),
				func(bool, bool) error,
			) (bool, agentsync.SyncStats, error) {
				return false, tc.stats, tc.err
			}
			t.Cleanup(func() { coordinateLocalSyncRunner = original })
			var failed bool
			captureStdout(t, func() { failed = doSync(SyncConfig{}) })
			require.Equal(t, tc.fail, failed)
		})
	}
}
