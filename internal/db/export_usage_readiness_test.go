package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageBlockExplanationIsBoundedAndStable(t *testing.T) {
	snapshot := usageQuerySnapshot{}
	for n := 39; n >= 0; n-- {
		snapshot.Sessions = append(snapshot.Sessions, usageQuerySession{ID: fmt.Sprintf("codex:%02d", n), ProviderAgent: "codex", Agent: "codex", Machine: "local", ParserDataVersion: CurrentDataVersion() - 1, StartedAt: "2026-06-01T01:00:00Z", EndedAt: "2026-06-01T02:00:00Z"})
	}
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	opts := UsageDaysOptions{Source: "codex", ExplainBlocked: true}
	got := usageDayBlocks(snapshot, opts, time.UTC, day, day)
	require.Len(t, got, 1)
	assert.Equal(t, 40, got[0].SessionCount)
	require.Len(t, got[0].SessionIDs, 32)
	assert.Equal(t, "codex:00", got[0].SessionIDs[0])
	assert.Equal(t, "codex:31", got[0].SessionIDs[31])
	assert.Equal(t, "codex:39", snapshot.Sessions[0].ID)
	opts.ExplainBlocked = false
	plain := usageDayBlocks(snapshot, opts, time.UTC, day, day)
	require.Len(t, plain, 1)
	assert.Empty(t, plain[0].SessionIDs)
	assert.Zero(t, plain[0].SessionCount)
	opts.Source = "claude"
	assert.Empty(t, usageDayBlocks(snapshot, opts, time.UTC, day, day))
}
