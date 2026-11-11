package listing

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lczyk/assert"
)

func TestWalk(t *testing.T) {
	root := t.TempDir()
	assert.NoError(t, os.MkdirAll(filepath.Join(root, "etc", "empty.d"), 0700))
	assert.NoError(t, os.MkdirAll(filepath.Join(root, "usr", "bin"), 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(root, "usr", "bin", "su"), []byte("x"), 0755))
	assert.NoError(t, os.Chmod(filepath.Join(root, "usr", "bin", "su"), 0755|fs.ModeSetuid))
	assert.NoError(t, os.WriteFile(filepath.Join(root, "usr", "bin", "a"), []byte("x"), 0644))
	assert.NoError(t, os.Symlink("usr/bin", filepath.Join(root, "bin")))
	assert.NoError(t, os.Symlink("/nowhere", filepath.Join(root, "usr", "bin", "dangling")))

	entries, err := Walk(root)
	assert.NoError(t, err)
	assert.EqualArrays(t, entries, []Entry{
		// A symlink to a directory is one entry and is not entered.
		{Path: "/bin", Kind: Symlink, Target: "usr/bin"},
		{Path: "/etc", Kind: Dir},
		{Path: "/etc/empty.d", Kind: Dir},
		{Path: "/usr", Kind: Dir},
		{Path: "/usr/bin", Kind: Dir},
		{Path: "/usr/bin/a", Kind: File, Mode: 0644},
		{Path: "/usr/bin/dangling", Kind: Symlink, Target: "/nowhere"},
		{Path: "/usr/bin/su", Kind: File, Mode: 0755 | fs.ModeSetuid},
	})

	_, err = Walk(filepath.Join(root, "missing"))
	assert.Error(t, err, "no such file")
}
