package parser

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
)

// Codex's own thread index selects a rollout when several retained files
// carry the same session metadata. Filenames alone never choose the winner.
// Lookups seek one primary key, including providers constructed for a single
// streaming parse; they must not walk every native thread for each source.
type codexPrimarySources struct {
	mu    sync.Mutex
	paths map[string]string
}

func (s codexSourceSet) primaryPath(id string) string {
	if s.agent != AgentCodex || s.primary == nil || id == "" {
		return ""
	}
	s.primary.mu.Lock()
	defer s.primary.mu.Unlock()
	if s.primary.paths == nil {
		s.primary.paths = map[string]string{}
	}
	if path, known := s.primary.paths[id]; known {
		return path
	}
	selected := ""
	seen := map[string]bool{}
	for _, root := range s.roots {
		if strings.HasPrefix(root, "s3://") {
			continue
		}
		base := filepath.Dir(root)
		if seen[base] {
			continue
		}
		seen[base] = true
		file := filepath.Join(base, "state_5.sqlite")
		if !IsRegularFile(file) {
			continue
		}
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(file), RawQuery: "mode=ro&_busy_timeout=1000"}
		d, err := sql.Open("sqlite3", u.String())
		if err != nil {
			continue
		}
		var path string
		err = d.QueryRow("SELECT rollout_path FROM threads WHERE id=?", id).Scan(&path)
		d.Close()
		if err != nil {
			continue
		}
		for _, configured := range s.roots {
			if pathUnderRoot(configured, path) && IsRegularFile(path) {
				selected = filepath.Clean(path)
				break
			}
		}
		if selected != "" {
			break
		}
	}
	s.primary.paths[id] = selected
	return selected
}

func (s codexSourceSet) selectsPath(path, id string) bool {
	primary := s.primaryPath(id)
	return primary == "" || samePath(primary, path)
}
