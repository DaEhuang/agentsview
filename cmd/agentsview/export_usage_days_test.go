package main

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
)

func TestExportUsageDaysPublicCommand(t *testing.T) {
	database := seedExportSessionsArchive(t)
	require.NoError(t, database.Close())
	stdout, stderr, err := executeExportSessionsCommand(newRootCommand(), "export", "usage-days", "--from", "2026-06-01", "--to", "2026-06-01", "--timezone", "UTC")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	var document db.UsageDaysExport
	require.NoError(t, json.Unmarshal([]byte(stdout), &document))
	assert.Equal(t, "agentsview.usage-days/v1", document.Schema)
	assert.NotEmpty(t, document.DatabaseID)
	assert.Equal(t, "UTC", document.Timezone)
	stdout, _, err = executeExportSessionsCommand(newRootCommand(), "export", "usage-days", "--from", "invalid", "--to", "2026-06-01", "--timezone", "UTC")
	require.Error(t, err)
	assert.Empty(t, stdout)
}
