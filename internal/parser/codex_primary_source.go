package parser

import (
	"path/filepath"
	"strings"
	"sync"
)

// Codex's thread index can bind a nonstandard source filename for recovery.
// It never excludes other rollouts of that thread from discovery.
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
		d, err := openSQLiteReadOnly(file, sqliteReadOptions{busyTimeoutMS: 1000})
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

// isBoundRollout authorizes a one-time repair only after the source's native
// identity is verified. The thread index is metadata, never a filter: a
// reverted thread's older rollouts remain valid archive sources.
func (s codexSourceSet) isBoundRollout(path, id string) bool {
	if s.agent != AgentCodex || id == "" {
		return false
	}
	if CodexThreadIDFromFilename(filepath.Base(path)) == id {
		return true
	}
	primary := s.primaryPath(id)
	return primary != "" && samePath(primary, path)
}
