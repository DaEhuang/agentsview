package parser

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func newKiroCrewProviderFactory(def AgentDef) ProviderFactory {
	caps := Capabilities{Source: jsonlFileProviderSourceCapabilities(), Content: ContentCapabilities{
		FirstMessage: CapabilitySupported, Cwd: CapabilitySupported, Model: CapabilitySupported,
	}, Sync: ProviderSyncSemantics{FingerprintHashRequiredForFreshness: true}}
	return NewSourceSetFactory(def, caps, func(cfg ProviderConfig) SourceSet {
		return NewJSONLSourceSet(AgentKiroCrew, cfg.Roots, WithRecursive(), WithContentHashing(), WithRejectSymlinkCompanions(),
			WithDescendPath(func(root, path string) bool {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return false
				}
				rel = filepath.ToSlash(rel)
				return rel == "." || rel == "sessions" || rel == "sessions/archive" || rel == "usage" || rel == "usage/tokens"
			}),
			WithIncludePath(crewIncludePath), WithSessionIDFromPath(crewSourceID),
			WithCompanionFiles(crewCompanions), WithCompanionTranscript(crewCompanionTranscript),
			WithParseFile(parseCrewFile))
	})
}

func crewPath(root, path string) (kind, stem string) {
	rel, err := filepath.Rel(root, path)
	if err != nil || !strings.HasSuffix(rel, ".jsonl") {
		return "", ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 3 && parts[0] == "usage" && parts[1] == "tokens" {
		return "usage", strings.TrimSuffix(parts[2], ".jsonl")
	}
	if len(parts) == 2 && parts[0] == "sessions" {
		return "dialogue", strings.TrimSuffix(parts[1], ".jsonl")
	}
	if len(parts) == 3 && parts[0] == "sessions" && parts[1] == "archive" {
		stem, _, found := strings.Cut(strings.TrimSuffix(parts[2], ".jsonl"), "__")
		if found {
			return "dialogue", stem
		}
	}
	return "", ""
}

func crewSourceID(root, path string) string {
	kind, stem := crewPath(root, path)
	if kind == "" {
		return ""
	}
	return kind + ":" + stem
}

func crewSessionFiles(root, stem string) []string {
	// ReadDir avoids interpreting user-controlled slot names as glob patterns.
	var files []string
	dir := filepath.Join(root, "sessions", "archive")
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), stem+"__") && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	live := filepath.Join(root, "sessions", stem+".jsonl")
	if info, err := os.Lstat(live); err == nil && info.Mode().IsRegular() {
		files = append(files, live)
	}
	return files
}

func crewIncludePath(root, path string) bool {
	kind, stem := crewPath(root, path)
	if kind == "usage" {
		return true
	}
	if kind != "dialogue" {
		return false
	}
	files := crewSessionFiles(root, stem)
	return len(files) > 0 && samePath(path, files[len(files)-1])
}

func crewRoot(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == "archive" || filepath.Base(dir) == "tokens" {
		dir = filepath.Dir(dir)
	}
	return filepath.Dir(dir)
}

func crewCompanions(path string) []string {
	root := crewRoot(path)
	kind, stem := crewPath(root, path)
	if kind != "dialogue" {
		return nil
	}
	var others []string
	for _, file := range crewSessionFiles(root, stem) {
		if !samePath(file, path) {
			others = append(others, file)
		}
	}
	return others
}

func crewCompanionTranscript(path string) (string, bool) {
	root := crewRoot(path)
	kind, stem := crewPath(root, path)
	if kind != "dialogue" {
		return "", false
	}
	files := crewSessionFiles(root, stem)
	if len(files) == 0 {
		return "", false
	}
	return files[len(files)-1], true
}

func parseCrewFile(ctx context.Context, path string, req ParseRequest) ([]ParseResult, []string, error) {
	root := req.Source.ConfiguredRoot
	if root == "" {
		root = crewRoot(path)
	}
	kind, stem := crewPath(root, path)
	var result ParseResult
	var err error
	if kind == "usage" {
		result, err = parseCrewBilling(ctx, path, stem, req.Machine)
	} else {
		if _, statErr := os.ReadDir(filepath.Join(root, "sessions", "archive")); statErr != nil && !os.IsNotExist(statErr) {
			return nil, nil, statErr
		}
		result, err = parseCrewDialogue(ctx, crewSessionFiles(root, stem), stem, req.Machine)
	}
	if err != nil {
		return nil, nil, err
	}
	result.Session.File = FileInfo{Path: path, Size: req.Fingerprint.Size, Mtime: req.Fingerprint.MTimeNS, Hash: req.Fingerprint.Hash}
	return []ParseResult{result}, nil, nil
}
