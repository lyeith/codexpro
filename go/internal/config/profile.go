package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func profileRootIdentity(root string) string {
	absolute, e := filepath.Abs(root)
	if e != nil {
		absolute = root
	}
	if real, e := filepath.EvalSymlinks(absolute); e == nil {
		return real
	}
	return absolute
}
func privateProfilePath(home, root string) string {
	sum := sha256.Sum256([]byte(profileRootIdentity(root)))
	return filepath.Join(home, "profiles", hex.EncodeToString(sum[:])[:24]+".json")
}
func decodeObject(data []byte) (map[string]any, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(data))
	if e := d.Decode(&value); e != nil {
		return nil, e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return nil, errors.New("trailing profile data")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, nil
	}
	return object, nil
}
func readStartupProfile(home, projectsFile string) (map[string]any, string, string, error) {
	empty := map[string]any{}
	if e := protectedPath(projectsFile); e != nil {
		return nil, "", "", e
	}
	catalogData, e := readBounded(projectsFile, 1000000)
	if os.IsNotExist(e) {
		return empty, "", "", nil
	}
	if e != nil {
		return nil, "", "", errors.New("cannot read private project catalog for startup profile")
	}
	var catalog struct {
		DefaultProject string `json:"defaultProject"`
		Projects       []struct {
			ID   string `json:"id"`
			Root string `json:"root"`
		} `json:"projects"`
	}
	if e = json.Unmarshal(catalogData, &catalog); e != nil {
		return nil, "", "", errors.New("cannot decode private project catalog for startup profile")
	}
	if len(catalog.Projects) == 0 {
		return empty, "", "", nil
	}
	selected := catalog.DefaultProject
	if selected == "" {
		selected = catalog.Projects[0].ID
	}
	root := ""
	for _, project := range catalog.Projects {
		if project.ID == selected {
			root = project.Root
			break
		}
	}
	if root == "" {
		return empty, "", "", nil
	}
	root = expandedPath(root, filepath.Dir(projectsFile))
	root = profileRootIdentity(root)
	path := privateProfilePath(home, root)
	homeReal, e := canonicalFuture(home)
	if e != nil {
		return nil, "", "", e
	}
	pathReal, e := canonicalFuture(path)
	if e != nil {
		return nil, "", "", e
	}
	if !within(homeReal, pathReal) {
		return nil, "", "", errors.New("startup profile escapes the private state directory")
	}
	if e = protectedPath(pathReal); e != nil {
		return nil, "", "", e
	}
	data, e := readBounded(path, 1024*1024)
	if os.IsNotExist(e) {
		return empty, "", root, nil
	}
	if e != nil {
		return nil, "", "", errors.New("cannot read private startup profile")
	}
	profile, e := decodeObject(data)
	if e != nil {
		return nil, "", "", errors.New("cannot decode private startup profile")
	}
	if profile == nil {
		return empty, "", root, nil
	}
	if raw, present := profile["root"]; present && raw != nil && raw != "" {
		savedRoot, ok := raw.(string)
		if !ok {
			return nil, "", "", errors.New("private startup profile root must be a path")
		}
		if profileRootIdentity(savedRoot) != root {
			return empty, "", root, nil
		}
	}
	// A profile is settings-only. Its token fields are deliberately never copied
	// into Config or used to authenticate this independent process.
	delete(profile, "token")
	delete(profile, "cloudflareToken")
	delete(profile, "profilePath")
	return profile, path, root, nil
}
