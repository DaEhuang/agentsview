package sync

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
)

func TestRetainedNativeDialogueSurvivesRotationAndRebuild(t *testing.T) {
	for _, mode := range []syncWriteMode{syncWriteDefault, syncWriteBulk} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			archive := openTestDB(t)
			engine := NewEngine(t.Context(), archive, EngineConfig{Machine: "local"})
			t.Cleanup(engine.Close)
			stamp := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
			makeWrite := func(numbers ...int) pendingWrite {
				pw := pendingWrite{forceReplace: true, sess: parser.ParsedSession{
					ID: "kiro-crew:dialogue:slot", Agent: parser.AgentKiroCrew, Machine: "local", Project: "sample",
					RetainMissingNativeMessages: true, StartedAt: stamp, EndedAt: stamp,
				}}
				for ordinal, number := range numbers {
					role := parser.RoleUser
					subtype := ""
					if number%2 == 1 {
						role, subtype = parser.RoleAssistant, "final_answer"
					}
					pw.msgs = append(pw.msgs, parser.ParsedMessage{Ordinal: ordinal, Role: role,
						Content: fmt.Sprintf("message %d", number), SourceUUID: fmt.Sprintf("native-%d", number),
						SourceType: "crew_transcript", SourceSubtype: subtype, Timestamp: stamp.Add(time.Duration(number) * time.Second)})
				}
				return pw
			}
			write := func(engine *Engine, pw pendingWrite) {
				result := engine.writeBatchWithOutcome([]pendingWrite{pw}, mode, true)
				require.Equal(t, 1, result.writtenSessions)
				require.Zero(t, result.failedSessions)
			}
			write(engine, makeWrite(0, 1, 2, 3))
			// Deleted first segment and appended a turn to the surviving segment.
			write(engine, makeWrite(2, 3, 4, 5))
			check := func(store *db.DB) {
				rows, err := store.GetAllMessages(t.Context(), "kiro-crew:dialogue:slot")
				require.NoError(t, err)
				require.Len(t, rows, 6)
				for i, row := range rows {
					assert.Equal(t, fmt.Sprintf("message %d", i), row.Content)
				}
				session, err := store.GetSessionFull(t.Context(), "kiro-crew:dialogue:slot")
				require.NoError(t, err)
				assert.Equal(t, 6, session.MessageCount)
				assert.Equal(t, 3, session.UserMessageCount)
			}
			check(archive)
			write(engine, makeWrite(2, 3, 4, 5))
			check(archive)
			replacement := openTestDB(t)
			rebuild := NewEngine(t.Context(), replacement, EngineConfig{Machine: "local"})
			rebuild.archiveStore = archive
			t.Cleanup(rebuild.Close)
			write(rebuild, makeWrite(4, 5))
			check(replacement)
			// The old store must not be mutated by replacement preparation.
			check(archive)
		})
	}
}

func TestMergeNativeArchiveMessagesRejectsAmbiguityAndStaleStreaming(t *testing.T) {
	old := db.Message{SourceUUID: "native-1", Role: "assistant", Content: "complete", Timestamp: "2026-01-01T00:00:00Z", SourceSubtype: "final_answer"}
	stale := old
	stale.Content, stale.SourceSubtype = "partial", "commentary"
	rows, err := mergeNativeArchiveMessages(t.Context(), []db.Message{old}, []db.Message{stale})
	require.NoError(t, err)
	assert.Equal(t, "complete", rows[0].Content)
	changed := old
	changed.Content = "corrected"
	rows, err = mergeNativeArchiveMessages(t.Context(), []db.Message{old}, []db.Message{changed})
	require.NoError(t, err)
	assert.Equal(t, "corrected", rows[0].Content)
	for _, invalid := range [][]db.Message{{old, old}, {{Role: "user", Timestamp: old.Timestamp}}, {{SourceUUID: old.SourceUUID, Role: "user", Timestamp: old.Timestamp}}} {
		_, err := mergeNativeArchiveMessages(t.Context(), []db.Message{old}, invalid)
		require.Error(t, err)
	}
}

type nativeArchiveReadCounter struct {
	db.Store
	messageReads []string
}

func (s *nativeArchiveReadCounter) GetAllMessages(ctx context.Context, id string) ([]db.Message, error) {
	s.messageReads = append(s.messageReads, id)
	return s.Store.GetAllMessages(ctx, id)
}

func TestRetainedNativeArchiveReadIsBoundedByChangedSession(t *testing.T) {
	for _, size := range []int{1, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			archive := openTestDB(t)
			for i := 0; i < size; i++ {
				require.NoError(t, archive.UpsertSession(t.Context(), db.Session{ID: fmt.Sprintf("session-%d", i), Agent: "kiro-crew", Project: "sample", Machine: "local"}))
			}
			counter := &nativeArchiveReadCounter{Store: archive}
			engine := NewEngine(t.Context(), openTestDB(t), EngineConfig{Machine: "local"})
			engine.archiveStore = counter
			t.Cleanup(engine.Close)
			_, err := engine.retainNativeArchiveMessages(t.Context(), &db.Session{ID: "session-0"}, []db.Message{{SourceUUID: "native", Role: "user", Timestamp: "2026-01-01T00:00:00Z", Content: "new"}})
			require.NoError(t, err)
			assert.Equal(t, []string{"session-0"}, counter.messageReads)
		})
	}
}
