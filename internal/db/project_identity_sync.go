package db

import (
	"encoding/json/v2"
	"slices"
	"sort"
	"strings"

	"go.kenn.io/agentsview/internal/export"
)

func SortProjectIdentityObservations(obs []export.ProjectIdentityObservation) {
	sort.SliceStable(obs, func(i, j int) bool {
		a, b := obs[i], obs[j]
		if a.SourceArchiveID != b.SourceArchiveID {
			return a.SourceArchiveID < b.SourceArchiveID
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.Machine != b.Machine {
			return a.Machine < b.Machine
		}
		if a.RootPath != b.RootPath {
			return a.RootPath < b.RootPath
		}
		return a.GitRemote < b.GitRemote
	})
}

func ObservationIdentityScope(
	observations []export.ProjectIdentityObservation,
) export.IdentityScope {
	unique := make(map[string]export.IdentityScope)
	for _, obs := range observations {
		scope := export.IdentityScope{
			ArchiveID:   strings.TrimSpace(obs.SourceArchiveID),
			ArchiveSalt: strings.TrimSpace(obs.SourceArchiveSalt),
		}
		if scope.ArchiveID == "" || scope.ArchiveSalt == "" {
			continue
		}
		unique[scope.ArchiveID+"\x00"+scope.ArchiveSalt] = scope
	}
	if len(unique) == 0 {
		return export.LegacySharedStoreIdentityScope()
	}
	scopes := make([]export.IdentityScope, 0, len(unique))
	for _, scope := range unique {
		scopes = append(scopes, scope)
	}
	if len(scopes) == 1 {
		return scopes[0]
	}
	return export.AggregateIdentityScope(scopes)
}

func MergeProjectIdentitySnapshots(
	base, refresh []export.ProjectIdentityObservation,
) []export.ProjectIdentityObservation {
	merged := make(map[string]export.ProjectIdentityObservation, len(base)+len(refresh))
	for _, snapshot := range base {
		merged[snapshot.SessionID] = snapshot
	}
	for _, snapshot := range refresh {
		merged[snapshot.SessionID] = snapshot
	}
	out := make([]export.ProjectIdentityObservation, 0, len(merged))
	for _, snapshot := range merged {
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// FilterIdentityScope limits full publications; LoadProjectIdentityPublicationDelta applies the scope in SQL.
func FilterIdentityScope(
	items []export.ProjectIdentityObservation, projects, excludeProjects []string,
) []export.ProjectIdentityObservation {
	if len(projects) == 0 && len(excludeProjects) == 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if ProjectMatchesPushScope(item.Project, projects, excludeProjects) {
			out = append(out, item)
		}
	}
	return out
}

func ProjectMatchesPushScope(project string, projects, excludeProjects []string) bool {
	if len(projects) > 0 && !slices.Contains(projects, project) {
		return false
	}
	return !slices.Contains(excludeProjects, project)
}

// CanonicalPushScope returns "" for an unfiltered scope, keeping the common case free of JSON.
func CanonicalPushScope(projects, excludeProjects []string) string {
	if len(projects) == 0 && len(excludeProjects) == 0 {
		return ""
	}
	scope := struct {
		Projects []string `json:"projects,omitempty"`
		Exclude  []string `json:"exclude,omitempty"`
	}{
		Projects: SortedCopy(projects),
		Exclude:  SortedCopy(excludeProjects),
	}
	data, err := json.Marshal(scope)
	if err != nil {
		return ""
	}
	return string(data)
}

type ProjectIdentityRootKey struct {
	ArchiveID string
	Project   string
	Machine   string
	RootPath  string
}

type ProjectIdentityObservationPlan struct {
	RealRemote []export.ProjectIdentityObservation
	Ambiguous  []export.ProjectIdentityObservation
	Fallbacks  []export.ProjectIdentityObservation
	RealRoots  []ProjectIdentityRootKey
}

func ObservationRootKey(
	obs export.ProjectIdentityObservation,
) ProjectIdentityRootKey {
	return ProjectIdentityRootKey{
		ArchiveID: obs.SourceArchiveID,
		Project:   obs.Project,
		Machine:   obs.Machine,
		RootPath:  obs.RootPath,
	}
}

// PlanProjectIdentityObservationSync keeps the last observation per conflict key, removes ordinary fallbacks shadowed by real remotes, and retains ambiguous evidence.
func PlanProjectIdentityObservationSync(
	observations []export.ProjectIdentityObservation,
) ProjectIdentityObservationPlan {
	type conflictKey struct {
		root      ProjectIdentityRootKey
		gitRemote string
	}
	keyOrder := make([]conflictKey, 0, len(observations))
	latest := make(map[conflictKey]export.ProjectIdentityObservation,
		len(observations))
	realRootSet := make(map[ProjectIdentityRootKey]bool)

	var plan ProjectIdentityObservationPlan
	for _, obs := range observations {
		key := conflictKey{
			root: ObservationRootKey(obs), gitRemote: obs.GitRemote,
		}
		previous, seen := latest[key]
		if !seen {
			keyOrder = append(keyOrder, key)
		} else if key.gitRemote == "" &&
			previous.RemoteResolution == export.ProjectResolutionAmbiguous &&
			obs.RemoteResolution != export.ProjectResolutionAmbiguous {
			continue
		}
		latest[key] = obs
		if obs.GitRemote != "" && !realRootSet[key.root] {
			realRootSet[key.root] = true
			plan.RealRoots = append(plan.RealRoots, key.root)
		}
	}
	for _, key := range keyOrder {
		obs := latest[key]
		if obs.GitRemote != "" {
			plan.RealRemote = append(plan.RealRemote, obs)
			continue
		}
		if obs.RemoteResolution == export.ProjectResolutionAmbiguous {
			plan.Ambiguous = append(plan.Ambiguous, obs)
			continue
		}
		if !realRootSet[key.root] {
			plan.Fallbacks = append(plan.Fallbacks, obs)
		}
	}
	return plan
}
