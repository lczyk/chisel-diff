package changes

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lczyk/assert"
)

func TestPrune(t *testing.T) {
	r := release(t, map[string]string{
		"slices/deep/demo.yaml": `package: demo
slices:
  bins:
    contents:
      /usr/bin/demo:
      /usr/bin/gone:
      /usr/bin/renamed: {copy: /usr/bin/gone}
      /usr/share/man/**:
  config:
    contents:
      /usr/bin/gone: {arch: amd64}
      /etc/demo:
  scripts:
    mutate: |
      pass
`,
		"slices/other.yaml": "package: other\nslices:\n  all:\n    contents:\n      /usr/bin/gone:\n",
	})
	removed, err := Prune(r.Dir, "demo", []string{"/usr/bin/gone", "/usr/share/man/**", "/not/there"})
	assert.NoError(t, err)
	// By source: the key, or the copy value. In every slice of the package
	// and in no other package.
	assert.EqualArrays(t, removed, []Pruned{{"bins", "/usr/bin/gone"}, {"bins", "/usr/bin/renamed"}, {"bins", "/usr/share/man/**"}, {"config", "/usr/bin/gone"}})
	after, err := Load(r.Dir)
	assert.NoError(t, err)
	assert.EqualArrays(t, after.files["demo"].paths["bins"], []string{"/usr/bin/demo"})
	assert.EqualArrays(t, after.files["demo"].paths["config"], []string{"/etc/demo"})
	assert.EqualArrays(t, after.files["demo"].names, []string{"bins", "config", "scripts"})
	assert.EqualArrays(t, after.files["other"].paths["all"], []string{"/usr/bin/gone"})

	// Nothing with such a source: nothing removed, the file as it was.
	before, err := os.ReadFile(filepath.Join(r.Dir, "slices", "other.yaml"))
	assert.NoError(t, err)
	removed, err = Prune(r.Dir, "other", []string{"/nope"})
	assert.NoError(t, err)
	assert.Len(t, removed, 0)
	now, err := os.ReadFile(filepath.Join(r.Dir, "slices", "other.yaml"))
	assert.NoError(t, err)
	assert.Equal(t, string(now), string(before))

	_, err = Prune(r.Dir, "ghost", []string{"/x"})
	assert.Error(t, err, "package ghost has no file")
}

func TestPruneThroughAliasesAndMerges(t *testing.T) {
	// An entry's options are an anchor another entry uses, and a slice
	// takes its contents through a merge key. Deleting the anchor's entry
	// must leave the rest readable.
	r := release(t, map[string]string{"slices/demo.yaml": `package: demo
common: &common
  /usr/bin/inherited:
  /usr/bin/gone-too:
slices:
  bins:
    contents:
      /usr/bin/gone: &opts {arch: [amd64]}
      /usr/bin/kept: *opts
  more:
    contents:
      <<: *common
      /usr/bin/own:
`})
	removed, err := Prune(r.Dir, "demo", []string{"/usr/bin/gone", "/usr/bin/gone-too"})
	assert.NoError(t, err)
	assert.EqualArrays(t, removed, []Pruned{{"bins", "/usr/bin/gone"}, {"more", "/usr/bin/gone-too"}})
	after, err := Load(r.Dir)
	assert.NoError(t, err)
	assert.EqualArrays(t, after.files["demo"].paths["bins"], []string{"/usr/bin/kept"})
	got := append([]string{}, after.files["demo"].paths["more"]...)
	sort.Strings(got)
	assert.EqualArrays(t, got, []string{"/usr/bin/inherited", "/usr/bin/own"})
}
