package server_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/db"
)

// TestGetSessionIncludesTranscriptFidelity asserts that a session seeded with
// TranscriptFidelity="summary" exposes the field in the GET /api/v1/sessions/{id}
// response. The field is carried by db.Session (embedded in service.SessionDetail)
// with json tag "transcript_fidelity,omitempty", so no additional plumbing is
// needed — this test acts as a regression guard.
func TestGetSessionIncludesTranscriptFidelity(t *testing.T) {
	te := setup(t)
	te.seedSession(t, "agy-fidelity-test", "test-project", 2, func(s *db.Session) {
		s.TranscriptFidelity = "summary"
	})

	w := te.get(t, "/api/v1/sessions/agy-fidelity-test")
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), `"transcript_fidelity":"summary"`)
}

func TestSessionParentLinkAPILocalNoSyncMode(t *testing.T) {
	te := setupNoSyncMode(t)
	for _, id := range []string{"manager", "worker"} {
		require.NoError(t, te.db.UpsertSession(t.Context(), db.Session{
			ID: id, Machine: "test", Agent: "claude", Project: "example",
		}))
	}

	w := te.put(t, "/api/v1/sessions/worker/parent",
		`{"parent_session_id":"manager","relationship_type":"teammate"}`)
	assertStatus(t, w, http.StatusBadRequest)

	w = te.put(t, "/api/v1/sessions/worker/parent",
		`{"parent_session_id":"manager"}`)
	assertStatus(t, w, http.StatusOK)
	link := decode[db.SessionParentLink](t, w)
	assert.Equal(t, "manager", link.ParentSessionID)
	assert.True(t, link.Applied)

	w = te.put(t, "/api/v1/sessions/worker/parent", `{"parent_session_id":null}`)
	assertStatus(t, w, http.StatusOK)
	assert.False(t, decode[db.SessionParentLink](t, w).Applied)
}
