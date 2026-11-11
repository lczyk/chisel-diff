package listing

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/canonical/chisel/public/jsonwall"
	"github.com/canonical/chisel/public/manifest"
	"github.com/klauspost/compress/zstd"
	"github.com/lczyk/assert"
)

// wall writes rows as chisel writes a manifest, a zstd jsonwall of schema.
func wall(t *testing.T, schema string, rows ...any) string {
	t.Helper()
	w := jsonwall.NewDBWriter(&jsonwall.DBWriterOptions{Schema: schema})
	for _, r := range rows {
		assert.NoError(t, w.Add(r))
	}
	path := filepath.Join(t.TempDir(), "manifest.wall")
	f, err := os.Create(path)
	assert.NoError(t, err)
	enc, err := zstd.NewWriter(f)
	assert.NoError(t, err)
	_, err = w.WriteTo(enc)
	assert.NoError(t, err)
	assert.NoError(t, enc.Close())
	assert.NoError(t, f.Close())
	return path
}

func TestOrigins(t *testing.T) {
	path := wall(t, manifest.Schema,
		&manifest.Content{Kind: "content", Slice: "libc6_libs", Path: "/usr/lib/x86_64-linux-gnu/libc.so.6"},
		&manifest.Package{Kind: "package", Name: "libc6", Version: "2.39", Digest: "ff", Arch: "amd64"},
		&manifest.Path{Kind: "path", Path: "/usr/lib/", Mode: "0755", Slices: []string{"base-files_lib"}},
		&manifest.Path{Kind: "path", Path: "/usr/lib/x86_64-linux-gnu/libc.so.6", Mode: "0755", Slices: []string{"libc6_libs"}},
		&manifest.Path{Kind: "path", Path: "/etc/shared", Mode: "0644", Slices: []string{"a_one", "b_two"}},
		&manifest.Slice{Kind: "slice", Name: "libc6_libs"},
	)
	got, err := Origins(path)
	assert.NoError(t, err)
	assert.That(t, reflect.DeepEqual(got, map[string][]string{
		"/usr/lib":                            {"base-files_lib"},
		"/usr/lib/x86_64-linux-gnu/libc.so.6": {"libc6_libs"},
		"/etc/shared":                         {"a_one", "b_two"},
	}), got)

	// A schema chisel's reader does not know is an error, not a guess.
	_, err = Origins(wall(t, "9.9", &manifest.Path{Kind: "path", Path: "/x", Slices: []string{"a_b"}}))
	assert.Error(t, err, `unknown schema version "9.9"`)

	_, err = Origins(filepath.Join(t.TempDir(), "none"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestWithout(t *testing.T) {
	paths := func(es []Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Path)
		}
		return out
	}
	const file = "/var/lib/chisel-diff/manifest.wall"
	es := []Entry{{Path: "/usr", Kind: Dir}, {Path: "/usr/bin", Kind: Dir}, {Path: "/var", Kind: Dir}, {Path: "/var/lib", Kind: Dir},
		{Path: "/var/lib/chisel-diff", Kind: Dir}, {Path: file, Kind: File}}
	// Nothing else under /var: the whole chain goes.
	assert.EqualArrays(t, paths(Without(es, file, nil)), []string{"/usr", "/usr/bin"})

	// Something else under /var/lib keeps it and /var.
	more := append(append([]Entry{}, es...), Entry{Path: "/var/lib/dpkg", Kind: Dir})
	assert.EqualArrays(t, paths(Without(more, file, nil)), []string{"/usr", "/usr/bin", "/var", "/var/lib", "/var/lib/dpkg"})

	// A directory a slice delivered stays, and so does what is above it.
	assert.EqualArrays(t, paths(Without(es, file, map[string][]string{"/var/lib": {"base-files_var"}})), []string{"/usr", "/usr/bin", "/var", "/var/lib"})
}
