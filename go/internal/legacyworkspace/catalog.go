package legacyworkspace

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"unicode/utf16"
)

var projectID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func knownKeys(value map[string]any, allowed ...string) error {
	for key := range value {
		found := false
		for _, candidate := range allowed {
			found = found || candidate == key
		}
		if !found {
			return fmt.Errorf("legacy catalog contains unknown field: %s", key)
		}
	}
	return nil
}

func catalogObject(value any) (map[string]any, error) {
	result, ok := value.(map[string]any)
	if !ok || result == nil {
		return nil, fmt.Errorf("legacy catalog entry must be a JSON object")
	}
	return result, nil
}

func control(value string, includeSpace bool) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f || includeSpace && character == 0x20 {
			return true
		}
	}
	return false
}

func validateCatalogEntry(value any, creation bool) (string, string, error) {
	entry, err := catalogObject(value)
	if err != nil {
		return "", "", err
	}
	allowed := []string{"id", "label", "root"}
	if !creation {
		allowed = append(allowed, "baseRef", "maxWorktrees")
	}
	if err = knownKeys(entry, allowed...); err != nil {
		return "", "", err
	}
	id, ok := entry["id"].(string)
	if !ok || !projectID.MatchString(id) {
		return "", "", fmt.Errorf("legacy project id must be 1-64 lowercase letters, numbers, dots, underscores, or dashes")
	}
	root, ok := entry["root"].(string)
	if !ok || trimJS(root) == "" {
		return "", "", fmt.Errorf("legacy project root must be a non-empty path")
	}
	if value, present := entry["label"]; present {
		label, ok := value.(string)
		if !ok || trimJS(label) == "" || len(utf16.Encode([]rune(trimJS(label)))) > 120 || control(label, false) {
			return "", "", fmt.Errorf("legacy project label must be 1-120 characters without control characters")
		}
	}
	if value, present := entry["baseRef"]; present {
		ref, ok := value.(string)
		ref = trimJS(ref)
		if !ok || ref == "" || len(utf16.Encode([]rune(ref))) > 256 || ref[0] == '-' || control(ref, true) {
			return "", "", fmt.Errorf("legacy project baseRef must be a Git ref without whitespace, control characters, or a leading dash")
		}
	}
	if value, present := entry["maxWorktrees"]; present {
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number || number < 1 || number > 512 {
			return "", "", fmt.Errorf("legacy project maxWorktrees must be an integer from 1 to 512")
		}
	}
	return id, root, nil
}

// Validate the exact source catalog's JSON fields, IDs, labels, references and
// numeric bounds. Do not stat or resolve its recorded project/creation roots:
// they may name live repositories, and an archive has no admission authority.
// The receipt explicitly records catalog_roots_resolved=false. Canonical-root
// uniqueness, repository identity and v1 project promotion remain cutover gates.
func sourceCatalog(raw []byte) ([]string, error) {
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("invalid legacy catalog JSON: %w", err)
	}
	catalog, err := catalogObject(parsed)
	if err != nil {
		return nil, err
	}
	if err = knownKeys(catalog, "version", "defaultProject", "projects", "creationRoots"); err != nil {
		return nil, err
	}
	version, ok := catalog["version"].(float64)
	if !ok || version != 1 {
		return nil, fmt.Errorf("legacy catalog version must be 1")
	}
	projects, ok := catalog["projects"].([]any)
	if !ok || len(projects) < 1 || len(projects) > 256 {
		return nil, fmt.Errorf("legacy catalog must contain 1-256 projects")
	}
	creations := []any{}
	if value, present := catalog["creationRoots"]; present && value != nil {
		if creations, ok = value.([]any); !ok || len(creations) > 256 {
			return nil, fmt.Errorf("legacy catalog must contain 0-256 creationRoots")
		}
	}
	ids, literalRoots := map[string]bool{}, map[string]bool{}
	projectIDs := make([]string, 0, len(projects))
	for _, group := range []struct {
		entries  []any
		creation bool
	}{{projects, false}, {creations, true}} {
		for _, value := range group.entries {
			id, root, err := validateCatalogEntry(value, group.creation)
			if err != nil {
				return nil, err
			}
			if ids[id] {
				return nil, fmt.Errorf("duplicate legacy project or creation-root id: %s", id)
			}
			if literalRoots[root] {
				return nil, fmt.Errorf("duplicate literal legacy project or creation-root path: %s", root)
			}
			ids[id], literalRoots[root] = true, true
			if !group.creation {
				projectIDs = append(projectIDs, id)
			}
		}
	}
	if value, present := catalog["defaultProject"]; present {
		defaultID, ok := value.(string)
		found := false
		for _, id := range projectIDs {
			found = found || id == defaultID
		}
		if !ok || !found {
			return nil, fmt.Errorf("legacy defaultProject must name a configured project")
		}
	}
	return projectIDs, nil
}
