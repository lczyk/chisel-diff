package changes

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lczyk/assert"
)

func TestAddManifest(t *testing.T) {
	r := release(t, map[string]string{
		"slices/demo.yaml": `package: demo
# a comment
common: &opts {arch: [amd64]}
essential:
  - demo_copyright
slices:
  bins:
    contents:
      /usr/bin/demo: *opts
  copyright:
    contents:
      /usr/share/doc/demo/copyright:
`,
		"slices/bare.yaml": "package: bare\n",
	})
	before, err := Load(r.Dir)
	assert.NoError(t, err)
	assert.NoError(t, AddManifest(r.Dir, "demo"))
	raw, err := os.ReadFile(filepath.Join(r.Dir, "slices", "demo.yaml"))
	assert.NoError(t, err)
	assert.ContainsString(t, string(raw), "# a comment")
	assert.ContainsString(t, string(raw), "&opts")
	after, err := Load(r.Dir)
	assert.NoError(t, err)
	// The slices that were there are as they were, and the new one is last.
	assert.EqualArrays(t, after.files["demo"].names, []string{"bins", "copyright", ManifestSlice})
	for _, n := range before.files["demo"].names {
		assert.That(t, reflect.DeepEqual(after.files["demo"].defs[n], before.files["demo"].defs[n]), n)
	}
	assert.That(t, reflect.DeepEqual(after.files["demo"].top, before.files["demo"].top))
	assert.That(t, reflect.DeepEqual(after.files["demo"].defs[ManifestSlice],
		map[string]any{"contents": map[string]any{ManifestDir + "/**": map[string]any{"generate": "manifest"}}}))

	// Twice is once.
	assert.NoError(t, AddManifest(r.Dir, "demo"))
	again, err := os.ReadFile(filepath.Join(r.Dir, "slices", "demo.yaml"))
	assert.NoError(t, err)
	assert.Equal(t, string(again), string(raw))

	// A file with no slices gets them.
	assert.NoError(t, AddManifest(r.Dir, "bare"))
	after, err = Load(r.Dir)
	assert.NoError(t, err)
	assert.EqualArrays(t, after.files["bare"].names, []string{ManifestSlice})

	// Slices that are an alias of a mapping get the slice there.
	r2 := release(t, map[string]string{"slices/al.yaml": "package: al\ndefs: &defs\n  bins:\n    contents:\n      /x:\nslices: *defs\n"})
	assert.NoError(t, AddManifest(r2.Dir, "al"))
	after, err = Load(r2.Dir)
	assert.NoError(t, err)
	assert.EqualArrays(t, after.files["al"].names, []string{"bins", ManifestSlice})

	assert.Error(t, AddManifest(r.Dir, "ghost"), "package ghost has no file")
}
