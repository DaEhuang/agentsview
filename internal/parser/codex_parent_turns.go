package parser

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/tidwall/gjson"
)

// A parent thread can retain several rollouts after a revert. Resolve the
// union of their native turn IDs, not an arbitrary first filename: a child
// can replay a turn absent from the currently selected parent branch.
func (p *codexProvider) parentTurnResolver(ctx context.Context, childPath string) codexParentTurnResolver {
	return func(parentID string) (map[string]struct{}, bool) {
		if !IsValidSessionID(parentID) || ctx.Err() != nil {
			return nil, false
		}
		turns := make(map[string]struct{})
		seen := make(map[string]bool)
		resolved := false
		for _, root := range p.sources.roots {
			for _, path := range p.sources.discoverSessionPaths(root) {
				if ctx.Err() != nil {
					return nil, false
				}
				path = filepath.Clean(path)
				if seen[path] || samePath(path, childPath) || CodexThreadIDFromFilename(filepath.Base(path)) != parentID {
					continue
				}
				seen[path] = true
				ids, valid := p.readParentTurns(ctx, path, parentID)
				if !valid {
					continue
				}
				resolved = true
				for id := range ids {
					turns[id] = struct{}{}
				}
			}
		}
		return turns, resolved
	}
}

func (p *codexProvider) readParentTurns(ctx context.Context, path, parentID string) (map[string]struct{}, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	key := codexParentTurnCacheKeyFor(path, info)
	if ids, ok := p.parentTurnCache.Get(key); ok {
		return ids, true
	}
	ids := make(map[string]struct{})
	owner := ""
	sawMeta := false
	_, err = readCodexJSONLFromContext(ctx, path, 0, func(line string) {
		kind := gjson.Get(line, "type").Str
		if kind == codexTypeSessionMeta && !sawMeta {
			owner = gjson.Get(line, "payload.id").Str
			sawMeta = true
		}
		if kind == codexTypeTurnContext {
			ids[gjson.Get(line, "payload.turn_id").Str] = struct{}{}
		}
	})
	if err != nil && !errors.Is(err, errCodexIncrementalNeedsFullParse) {
		return nil, false
	}
	// Filename matches alone never authorize suppressing child messages.
	if owner != parentID {
		return nil, false
	}
	after, err := os.Stat(path)
	if err != nil || codexParentTurnCacheKeyFor(path, after) != key {
		return nil, false
	}
	p.parentTurnCache.Put(key, ids)
	return ids, true
}
