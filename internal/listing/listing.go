// Package listing reads a rootfs as the entries a cut delivered: each path
// with what kind of thing is there, as lstat sees it.
package listing

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Kind is what an entry is.
type Kind string

const (
	File    Kind = "file"
	Dir     Kind = "dir"
	Symlink Kind = "symlink"
	Other   Kind = "other"
)

// Entry is one path of a rootfs. A symlink is an entry of its own and is
// not followed.
type Entry struct {
	Path   string // absolute, with no trailing slash
	Kind   Kind
	Mode   fs.FileMode // of a regular file: the permission, setuid, setgid and sticky bits
	Target string      // of a symlink, as written
}

// Walk lists every entry under root, sorted by path. root itself is not
// listed.
func Walk(root string) ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		e := Entry{Path: "/" + filepath.ToSlash(rel), Kind: Other}
		switch {
		case d.IsDir():
			e.Kind = Dir
		case d.Type()&fs.ModeSymlink != 0:
			e.Kind = Symlink
			if e.Target, err = os.Readlink(p); err != nil {
				return err
			}
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			e.Kind = File
			e.Mode = info.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}
