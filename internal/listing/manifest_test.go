package listing

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/lczyk/assert"
)

func TestOrigins(t *testing.T) {
	// As chisel writes it: a header, then rows of every kind sorted.
	rows := `{"jsonwall":"1.0","schema":"1.0","count":6}
{"kind":"content","slice":"libc6_libs","path":"/usr/lib/x86_64-linux-gnu/libc.so.6"}
{"kind":"package","name":"libc6","version":"2.39","sha256":"ff","arch":"amd64"}
{"kind":"path","path":"/usr/lib/","mode":"0755","slices":["base-files_lib"]}
{"kind":"path","path":"/usr/lib/x86_64-linux-gnu/libc.so.6","mode":"0755","slices":["libc6_libs"],"sha256":"3a","size":2}
{"kind":"path","path":"/etc/shared","mode":"0644","slices":["a_one","b_two"]}
{"kind":"slice","name":"libc6_libs"}
`
	path := filepath.Join(t.TempDir(), "manifest.wall")
	f, err := os.Create(path)
	assert.NoError(t, err)
	enc, err := zstd.NewWriter(f)
	assert.NoError(t, err)
	_, err = enc.Write([]byte(rows))
	assert.NoError(t, err)
	assert.NoError(t, enc.Close())
	assert.NoError(t, f.Close())

	got, err := Origins(path)
	assert.NoError(t, err)
	assert.That(t, reflect.DeepEqual(got, map[string][]string{
		"/usr/lib":                            {"base-files_lib"},
		"/usr/lib/x86_64-linux-gnu/libc.so.6": {"libc6_libs"},
		"/etc/shared":                         {"a_one", "b_two"},
	}), got)

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
