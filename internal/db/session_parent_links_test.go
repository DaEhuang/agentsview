package db

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func linkOwned(t *testing.T, database *DB, id string) bool {
	t.Helper()
	var owned bool
	require.NoError(t, database.getReader().QueryRowContext(t.Context(),
		"SELECT parent_from_link FROM sessions WHERE id = ?", id).Scan(&owned))
	return owned
}

func localModifiedAt(t *testing.T, database *DB, id string) string {
	t.Helper()
	var modified string
	require.NoError(t, database.getReader().QueryRowContext(t.Context(),
		"SELECT COALESCE(local_modified_at, '') FROM sessions WHERE id = ?", id).Scan(&modified))
	return modified
}

func stampLocalModifiedAt(t *testing.T, database *DB, id string) {
	t.Helper()
	_, err := database.getWriter().ExecContext(t.Context(),
		"UPDATE sessions SET local_modified_at = '2020-01-01T00:00:00.000Z' WHERE id = ?", id)
	require.NoError(t, err)
}

func withParent(parent, rel string) func(*Session) {
	return func(s *Session) {
		s.ParentSessionID = new(parent)
		s.RelationshipType = rel
	}
}

func TestParserParentWinsOverParentLink(t *testing.T) {
	database := testDB(t)
	insertSession(t, database, "manager", "proj")
	insertSession(t, database, "path-parent", "proj")
	insertSession(t, database, "worker", "proj", withParent("path-parent", "continuation"))

	link, err := database.SetSessionParentLink(t.Context(), "worker", "manager", "subagent")
	require.NoError(t, err)
	assert.False(t, link.Applied)
	assert.Equal(t, "path-parent", parentOfSession(t, database, "worker"))

	insertSession(t, database, "worker", "proj")
	assert.Equal(t, "manager", parentOfSession(t, database, "worker"))
	insertSession(t, database, "worker", "proj", withParent("path-parent", "continuation"))
	assert.Equal(t, "path-parent", parentOfSession(t, database, "worker"))
	assert.False(t, linkOwned(t, database, "worker"))

	_, err = database.SetSessionParentLink(t.Context(), "worker", "", "")
	require.NoError(t, err)
	stored, err := database.GetSession(t.Context(), "worker")
	require.NoError(t, err)
	assert.Equal(t, "path-parent", parentOfSession(t, database, "worker"))
	assert.Equal(t, "continuation", stored.RelationshipType)
}

func TestPendingParentLinkAppliesOnEveryWriter(t *testing.T) {
	writers := map[string]func(t *testing.T, database *DB, s Session){
		"upsert": func(t *testing.T, database *DB, s Session) {
			t.Helper()
			require.NoError(t, database.UpsertSession(t.Context(), s))
		},
		"batch": func(t *testing.T, database *DB, s Session) {
			t.Helper()
			_, err := database.WriteSessionBatchAtomic(t.Context(), []SessionBatchWrite{{Session: s}})
			require.NoError(t, err)
		},
		"placeholder": func(t *testing.T, database *DB, s Session) {
			t.Helper()
			require.NoError(t, database.insertSessionIfAbsent(t.Context(), s))
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			database := testDB(t)
			insertSession(t, database, "manager", "proj")
			link, err := database.SetSessionParentLink(t.Context(), "worker", "manager", "fork")
			require.NoError(t, err)
			assert.False(t, link.Applied)

			write(t, database, Session{
				ID: "worker", Project: "proj", Machine: defaultMachine, Agent: defaultAgent,
			})
			stored, err := database.GetSession(t.Context(), "worker")
			require.NoError(t, err)
			assert.Equal(t, "manager", parentOfSession(t, database, "worker"))
			assert.Equal(t, "fork", stored.RelationshipType)
			assert.True(t, linkOwned(t, database, "worker"))
		})
	}
}

// r1: the PUT check follows stored parents and every stored link, applied or
// not, with no depth bound.
func TestSetSessionParentLinkRejectsInvalidInput(t *testing.T) {
	database := testDB(t)
	insertSession(t, database, "a", "proj")
	insertSession(t, database, "b", "proj", withParent("a", "subagent"))
	_, err := database.SetSessionParentLink(t.Context(), "pending-c", "b", "")
	require.NoError(t, err)
	insertSession(t, database, "root", "proj")
	insertSession(t, database, "d", "proj", withParent("root", "subagent"))
	insertSession(t, database, "e", "proj")
	link, err := database.SetSessionParentLink(t.Context(), "d", "e", "")
	require.NoError(t, err)
	require.False(t, link.Applied)
	insertSession(t, database, "manager", "proj")
	insertSession(t, database, "worker", "proj")
	link, err = database.SetSessionParentLink(t.Context(), "worker", "manager", "")
	require.NoError(t, err)
	require.True(t, link.Applied)
	insertSession(t, database, "chain-0", "proj")
	for i := 1; i < 133; i++ {
		id, parent := "chain-"+strconv.Itoa(i), "chain-"+strconv.Itoa(i-1)
		if i%2 == 0 {
			insertSession(t, database, id, "proj", withParent(parent, "subagent"))
			continue
		}
		insertSession(t, database, id, "proj")
		_, err = database.SetSessionParentLink(t.Context(), id, parent, "")
		require.NoError(t, err)
	}

	tests := []struct {
		name, session, parent, rel, msg string
	}{
		{"self link", "a", "a", "", "a is already an ancestor of a"},
		{"unknown relationship", "b", "a", "teammate", "relationship_type must be"},
		{"loop through session row", "a", "b", "", "a is already an ancestor of b"},
		{"loop through pending link", "a", "pending-c", "", "of pending-c through a stored parent link"},
		{"loop through unapplied link", "e", "d", "", "of d through a stored parent link"},
		{"loop through applied link", "manager", "worker", "", "of worker through a stored parent link"},
		{"loop through long chain", "chain-0", "chain-132", "", "through a stored parent link"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := database.SetSessionParentLink(t.Context(), tt.session, tt.parent, tt.rel)
			require.ErrorIs(t, err, ErrSessionParentLinkInvalid)
			assert.ErrorContains(t, err, tt.msg)
		})
	}

	_, err = database.SetSessionParentLink(t.Context(), "a", "b", "")
	require.ErrorIs(t, err, ErrSessionParentLinkInvalid)
	assert.NotContains(t, err.Error(), "stored parent link", "no link to clear on a parser-only loop")

	_, err = database.SetSessionParentLink(t.Context(), "never-linked", "", "teammate")
	require.NoError(t, err, "clearing skips relationship validation and needs no link")
}

func TestCopySessionMetadataFromKeepsSessionParentLinks(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	src := testDBAtPath(t, srcPath, "source db")
	insertSession(t, src, "manager", "proj")
	insertSession(t, src, "worker", "proj")
	insertSession(t, src, "orphan", "proj")
	for _, id := range []string{"worker", "orphan", "later-worker"} {
		_, err := src.SetSessionParentLink(t.Context(), id, "manager", "")
		require.NoError(t, err)
	}
	require.NoError(t, src.Close())

	dst := testDB(t)
	insertSession(t, dst, "manager", "proj")
	insertSession(t, dst, "worker", "proj")
	copied, err := dst.CopyOrphanedDataFrom(srcPath)
	require.NoError(t, err)
	require.Equal(t, 1, copied)
	require.NoError(t, dst.CopySessionMetadataFrom(srcPath))
	assert.Equal(t, "manager", parentOfSession(t, dst, "worker"))
	assert.Equal(t, "manager", parentOfSession(t, dst, "orphan"))
	assert.True(t, linkOwned(t, dst, "orphan"), "orphan copy keeps its flag")

	_, err = dst.SetSessionParentLink(t.Context(), "orphan", "", "")
	require.NoError(t, err)
	stored, err := dst.GetSession(t.Context(), "orphan")
	require.NoError(t, err)
	assert.Nil(t, stored.ParentSessionID)

	insertSession(t, dst, "later-worker", "proj")
	assert.Equal(t, "manager", parentOfSession(t, dst, "later-worker"))
}

// r3: StepCode and Pi write some subagents with no parent, so clearing a
// link must restore the parser's relationship rather than blank it.
func TestSetSessionParentLinkClearRestoresParserRelationship(t *testing.T) {
	database := testDB(t)
	insertSession(t, database, "manager", "proj")
	subagent := func(s *Session) { s.RelationshipType = "subagent" }
	insertSession(t, database, "worker", "proj", subagent)

	link, err := database.SetSessionParentLink(t.Context(), "worker", "manager", "")
	require.NoError(t, err)
	assert.True(t, link.Applied)
	assert.Equal(t, "subagent", link.RelationshipType)
	assert.Equal(t, "manager", parentOfSession(t, database, "worker"))

	_, err = database.SetSessionParentLink(t.Context(), "worker", "manager", "continuation")
	require.NoError(t, err)
	stampLocalModifiedAt(t, database, "worker")
	insertSession(t, database, "worker", "proj", subagent)
	stored, err := database.GetSession(t.Context(), "worker")
	require.NoError(t, err)
	assert.Equal(t, "manager", parentOfSession(t, database, "worker"))
	assert.Equal(t, "continuation", stored.RelationshipType)
	assert.True(t, linkOwned(t, database, "worker"))
	assert.Equal(t, "2020-01-01T00:00:00.000Z", localModifiedAt(t, database, "worker"),
		"a re-parse that keeps the link must not trigger a mirror re-push")

	link, err = database.SetSessionParentLink(t.Context(), "worker", " ", "")
	require.NoError(t, err)
	assert.False(t, link.Applied)
	assert.Empty(t, link.ParentSessionID)
	stored, err = database.GetSession(t.Context(), "worker")
	require.NoError(t, err)
	assert.Nil(t, stored.ParentSessionID)
	assert.Equal(t, "subagent", stored.RelationshipType)
	assert.NotEqual(t, "2020-01-01T00:00:00.000Z", localModifiedAt(t, database, "worker"))

	insertSession(t, database, "worker", "proj", subagent)
	stored, err = database.GetSession(t.Context(), "worker")
	require.NoError(t, err)
	assert.Nil(t, stored.ParentSessionID)
}

// r4: a spawn edge takes the row even when it names the link's own parent,
// and no later write hands it back to the link while the edge stands.
func TestSpawnEdgeTakesOverParentLink(t *testing.T) {
	database := testDB(t)
	insertSession(t, database, "manager", "proj")
	insertSession(t, database, "spawner", "proj")
	insertSession(t, database, "worker", "proj")
	_, err := database.SetSessionParentLink(t.Context(), "worker", "manager", "")
	require.NoError(t, err)
	require.True(t, linkOwned(t, database, "worker"))

	insertMessages(t, database, spawnEdgeTo("spawner", "worker", "spawn worker"))
	require.NoError(t, database.LinkSubagentSessions())
	assert.Equal(t, "spawner", parentOfSession(t, database, "worker"))
	assert.False(t, linkOwned(t, database, "worker"))

	link, err := database.SetSessionParentLink(t.Context(), "worker", "manager", "fork")
	require.NoError(t, err)
	assert.False(t, link.Applied)
	insertSession(t, database, "worker", "proj")
	_, err = database.LinkSubagentSessionsForSessions(t.Context(), []string{"worker"})
	require.NoError(t, err)
	assert.Equal(t, "spawner", parentOfSession(t, database, "worker"))

	_, err = database.SetSessionParentLink(t.Context(), "worker", "", "")
	require.NoError(t, err)
	assert.Equal(t, "spawner", parentOfSession(t, database, "worker"))
}

func TestQueuedDanglingClearHandsRowBackToLink(t *testing.T) {
	database := testDB(t)
	insertSession(t, database, "manager", "proj")
	insertSession(t, database, "spawner", "proj")
	insertSession(t, database, "worker", "proj", func(s *Session) { s.RelationshipType = "subagent" })
	_, err := database.SetSessionParentLink(t.Context(), "worker", "manager", "")
	require.NoError(t, err)
	insertMessages(t, database, spawnEdgeTo("spawner", "worker", "spawn worker"))
	require.NoError(t, database.LinkSubagentSessions())
	assert.Equal(t, "spawner", parentOfSession(t, database, "worker"))

	for _, q := range []string{
		"DELETE FROM tool_calls WHERE session_id = 'spawner'",
		"DELETE FROM messages WHERE session_id = 'spawner'",
		"DELETE FROM sessions WHERE id = 'spawner'",
	} {
		_, err := database.getWriter().ExecContext(t.Context(), q)
		require.NoError(t, err)
	}
	require.NoError(t, database.QueueSubagentParentCleanupRepairs(t.Context(), []string{"worker"}))
	require.NoError(t, database.RepairQueuedSubagentParents())
	assert.Equal(t, "manager", parentOfSession(t, database, "worker"))
	assert.True(t, linkOwned(t, database, "worker"))
}

// A transcript that arrives after the PUT and contradicts it closes a loop.
// The archive keeps both parents, the sessions stay reachable by id, and
// clearing the link breaks the loop (docs/session-api.md).
func TestContradictingTranscriptLoopClearsWithLink(t *testing.T) {
	database := testDB(t)
	insertSession(t, database, "manager", "proj")
	insertSession(t, database, "worker", "proj")
	_, err := database.SetSessionParentLink(t.Context(), "worker", "manager", "")
	require.NoError(t, err)

	insertSession(t, database, "manager", "proj", withParent("worker", "continuation"))
	assert.Equal(t, "manager", parentOfSession(t, database, "worker"))
	assert.Equal(t, "worker", parentOfSession(t, database, "manager"))

	index, err := database.GetSidebarSessionIndex(t.Context(), SessionFilter{})
	require.NoError(t, err)
	for _, row := range index.Sessions {
		assert.NotContains(t, []string{"manager", "worker"}, row.ID)
	}
	children, err := database.GetChildSessions(t.Context(), "manager")
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, "worker", children[0].ID)

	_, err = database.SetSessionParentLink(t.Context(), "worker", "", "")
	require.NoError(t, err)
	stored, err := database.GetSession(t.Context(), "worker")
	require.NoError(t, err)
	assert.Nil(t, stored.ParentSessionID)
	index, err = database.GetSidebarSessionIndex(t.Context(), SessionFilter{})
	require.NoError(t, err)
	ids := make([]string, 0, len(index.Sessions))
	for _, row := range index.Sessions {
		ids = append(ids, row.ID)
	}
	assert.ElementsMatch(t, []string{"worker", "manager"}, ids)
}

// The upsert's link lookups and the scoped apply run on every sync batch under
// the writer lock, so they must seek by key rather than scan.
func TestParentLinkStatementsSeekByKey(t *testing.T) {
	database := testDB(t)
	for i := range 500 {
		id := "bulk-" + strconv.Itoa(i)
		insertSession(t, database, id, "proj")
		_, err := database.SetSessionParentLink(t.Context(), "pending-"+strconv.Itoa(i), id, "")
		require.NoError(t, err)
	}
	insertSession(t, database, "manager", "proj")
	insertSession(t, database, "worker", "proj")
	_, err := database.SetSessionParentLink(t.Context(), "worker", "manager", "")
	require.NoError(t, err)

	plans := map[string]string{
		"previous row": queryPlanOf(t, database, upsertPreviousRowSQL, "worker"),
		"link lookup":  queryPlanOf(t, database, parentLinkLookupSQL, "worker"),
		"spawn edge":   queryPlanOf(t, database, spawnEdgeExistsSQL, "worker"),
		"apply one":    queryPlanOf(t, database, applyParentLinksSQL("l.session_id = ?"), "worker"),
		"apply chunk":  queryPlanOf(t, database, applyParentLinksSQL("l.session_id IN (?, ?)"), "worker", "bulk-1"),
		"loop check":   queryPlanOf(t, database, parentLinkLoopSQL, "manager", "worker"),
	}
	for name, plan := range plans {
		assert.NotRegexp(t, `(?m)SCAN (s|l|sessions|session_parent_links|tool_calls)( |$)`, plan,
			"%s scans a table\n%s", name, plan)
	}
}

// The backfill can't know what the parser wrote for a row the spawn linker
// rewrote, so it leaves those empty for the next parse.
func TestParserRelationshipBackfillSkipsSpawnLinkedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := testDBAtPath(t, path, "pre-migration db")
	continuation := func(s *Session) { s.RelationshipType = "continuation" }
	insertSession(t, d, "spawner", "proj")
	insertSession(t, d, "kid", "proj", continuation)
	insertSession(t, d, "plain", "proj", continuation)
	insertMessages(t, d, spawnEdgeTo("spawner", "kid", "spawn kid"))
	require.NoError(t, d.LinkSubagentSessions())
	require.NoError(t, d.Close())

	conn, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), `DROP TRIGGER IF EXISTS artifact_sessions_update_queue`)
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), `ALTER TABLE sessions DROP COLUMN parser_relationship_type`)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	d, err = Open(t.Context(), path)
	require.NoError(t, err)
	defer d.Close()
	got := map[string]string{}
	for _, id := range []string{"kid", "plain"} {
		var rel string
		require.NoError(t, d.getReader().QueryRowContext(t.Context(),
			"SELECT parser_relationship_type FROM sessions WHERE id = ?", id).Scan(&rel))
		got[id] = rel
	}
	assert.Equal(t, map[string]string{"kid": "", "plain": "continuation"}, got)
}
