package changes

import (
	"errors"
	"os"

	"gopkg.in/yaml.v3"
)

// ManifestSlice is the slice AddManifest gives a package, and ManifestDir
// where it has chisel write its manifest.
const (
	ManifestSlice = "chisel-diff-manifest"
	ManifestDir   = "/var/lib/chisel-diff"
)

// AddManifest gives pkg, in the release at dir, a slice that has chisel
// write its manifest, which says what slices every path of a cut comes
// from. It is for a copy of a release: cut alongside a slice, it adds the
// manifest and the directories above it and nothing else. The file is
// rewritten from its yaml nodes, so anchors, aliases and comments stay.
// A package that has the slice already is left as it is.
func AddManifest(dir, pkg string) error {
	path, err := find(dir, pkg)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New(path + ": not a mapping")
	}
	root := doc.Content[0]
	var all *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "slices" {
			all = root.Content[i+1]
		}
	}
	if all == nil {
		all = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "slices"}, all)
	}
	if all.Kind == yaml.AliasNode {
		all = all.Alias
	}
	if all.Kind != yaml.MappingNode {
		return errors.New(path + ": slices is not a mapping")
	}
	for i := 0; i+1 < len(all.Content); i += 2 {
		if all.Content[i].Value == ManifestSlice {
			return nil
		}
	}
	var slice yaml.Node
	if err := yaml.Unmarshal([]byte("contents:\n  "+ManifestDir+"/**: {generate: manifest}\n"), &slice); err != nil {
		return err
	}
	all.Content = append(all.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: ManifestSlice}, slice.Content[0])
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}
