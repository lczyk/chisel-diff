package changes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Pruned is a contents entry Prune deleted.
type Pruned struct {
	Slice string
	Key   string
}

// Prune deletes, in the release at dir, every contents entry of pkg whose
// source is one of sources: the entry's key, or its copy value when it has
// one, compared as written, a glob as the pattern it is. It is for a copy of
// a release whose slice names paths its package no longer ships, which
// chisel refuses to cut, so the file is rewritten as chisel reads it:
// aliases and merge keys are resolved, and comments and key order go. The
// entries deleted are returned, none when no entry has such a source.
func Prune(dir, pkg string, sources []string) ([]Pruned, error) {
	path, err := find(dir, pkg)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	all, _ := doc["slices"].(map[string]any)
	var removed []Pruned
	for name, def := range all {
		body, _ := def.(map[string]any)
		contents, _ := body["contents"].(map[string]any)
		for key, entry := range contents {
			source := key
			if e, ok := entry.(map[string]any); ok {
				if c, ok := e["copy"].(string); ok {
					source = c
				}
			}
			if slices.Contains(sources, source) {
				delete(contents, key)
				removed = append(removed, Pruned{Slice: name, Key: key})
			}
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	sort.Slice(removed, func(i, j int) bool {
		if removed[i].Slice != removed[j].Slice {
			return removed[i].Slice < removed[j].Slice
		}
		return removed[i].Key < removed[j].Key
	})
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return removed, os.WriteFile(path, out, 0644)
}

// find is the file of pkg under the slice dirs of the release at dir.
func find(dir, pkg string) (string, error) {
	var found string
	for _, sub := range sliceDirs {
		root := filepath.Join(dir, sub)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root && errors.Is(err, fs.ErrNotExist) {
					return fs.SkipAll
				}
				return err
			}
			if !d.IsDir() && strings.HasSuffix(path, ".yaml") && strings.TrimSuffix(filepath.Base(path), ".yaml") == pkg {
				found = path
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if found != "" {
			return found, nil
		}
	}
	return "", fmt.Errorf("package %s has no file in %s", pkg, dir)
}
