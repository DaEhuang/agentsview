package rawarchive

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
)

var captureCountQueries = map[string]string{
	"stars":            "SELECT count(*) FROM starred_sessions",
	"pins":             "SELECT count(*) FROM pinned_messages",
	"renamed":          "SELECT count(*) FROM sessions WHERE coalesce(display_name,'')<>''",
	"trashed":          "SELECT count(*) FROM sessions WHERE deleted_at IS NOT NULL",
	"deleted":          "SELECT count(*) FROM excluded_sessions",
	"has_origin":       "SELECT count(*) FROM pg_sync_state WHERE key='artifact_origin_id'",
	"artifact_imports": "SELECT count(*) FROM artifact_imported_sessions",
	"qualified_ids":    "SELECT count(*) FROM sessions WHERE instr(id,'~')>0",
	"insights":         "SELECT count(*) FROM insights",
	"manual_projects":  "SELECT count(*) FROM session_project_assignments",
	"worktree_rules":   "SELECT count(*) FROM worktree_project_mappings",
}

func capturePreflight(ctx context.Context, path string) (CapturePreflight, error) {
	result := CapturePreflight{Counts: map[string]*int64{}, Unknown: map[string]string{}}
	file, err := inventoryFile(ctx, path, "application", "sessions.db", "sqlite-online-backup")
	if errors.Is(err, os.ErrNotExist) {
		for key := range captureCountQueries {
			result.Counts[key] = nil
			result.Unknown[key] = "database absent"
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.DatabaseSHA256 = file.SHA256
	conn, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro&immutable=1")
	if err != nil {
		return result, err
	}
	defer conn.Close()
	for key, query := range captureCountQueries {
		var count int64
		if err := conn.QueryRowContext(ctx, query).Scan(&count); err != nil {
			result.Counts[key] = nil
			result.Unknown[key] = err.Error()
		} else {
			result.Counts[key] = new(count)
		}
	}
	return result, ctx.Err()
}

func (p CapturePreflight) projectionError() error {
	if p.DatabaseSHA256 == "" || len(p.Unknown) > 0 {
		return errors.New("capture preflight is unknown; inspect the captured database before importing")
	}
	for _, key := range []string{"trashed", "deleted", "has_origin", "artifact_imports", "qualified_ids"} {
		count, ok := p.Counts[key]
		if !ok || count == nil {
			return errors.New("capture preflight is incomplete")
		}
		if *count != 0 {
			return errors.New("capture contains deletion or artifact evidence; projection requires source mapping support before import")
		}
	}
	return nil
}
