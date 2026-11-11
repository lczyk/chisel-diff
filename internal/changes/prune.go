package changes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Prune deletes, in the release at dir, every contents entry of pkg whose
// source is one of sources: the entry's key, or its copy value when it has
// one, compared as written, a glob as the pattern it is. It is for a copy of
// a release whose slice names paths its package no longer ships, which
// chisel refuses to cut. The keys of the entries deleted are returned, none
// when no entry has such a source.
func Prune(dir, pkg string, sources []string) ([]string, error) {
	path, err := find(dir, pkg)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	var removed []string
	all := value(doc.Content[0], "slices")
	for i := 1; all != nil && i < len(all.Content); i += 2 {
		contents := value(resolve(all.Content[i]), "contents")
		if contents == nil {
			continue
		}
		kept := contents.Content[:0]
		for j := 0; j+1 < len(contents.Content); j += 2 {
			key, entry := contents.Content[j], contents.Content[j+1]
			source := key.Value
			if c := value(resolve(entry), "copy"); c != nil && c.Kind == yaml.ScalarNode {
				source = c.Value
			}
			if slices.Contains(sources, source) {
				if !slices.Contains(removed, key.Value) {
					removed = append(removed, key.Value)
				}
				continue
			}
			kept = append(kept, key, entry)
		}
		contents.Content = kept
	}
	if len(removed) == 0 {
		return nil, nil
	}
	out, err := yaml.Marshal(&doc)
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
			if !d.IsDir() && strings.TrimSuffix(filepath.Base(path), ".yaml") == pkg && strings.HasSuffix(path, ".yaml") {
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

// resolve is the node an alias stands for, or the node itself.
func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// value is the value of key in a mapping, through aliases, or nil.
func value(m *yaml.Node, key string) *yaml.Node {
	m = resolve(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return resolve(m.Content[i+1])
		}
	}
	return nil
}
