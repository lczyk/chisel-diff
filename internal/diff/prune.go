package diff

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lczyk/chisel-coverage/pkg/cut"
	"github.com/lczyk/chisel-coverage/pkg/pool"

	"github.com/lczyk/chisel-diff/internal/changes"
)

// pruning is a base unit being cut again in a release of its own.
type pruning struct {
	u       unit
	release string
	pruned  []string // the entries of the unit's own slice deleted so far
}

// prune deals with a base the archive has moved under. A slice that names a
// path its package no longer ships does not cut, and the PR that deletes
// the path is routine, so calling the base uncuttable would leave exactly
// those PRs unchecked. So each such base unit gets a copy of the base
// release with every entry chisel named deleted, and is cut again into an
// empty root, round after round. Each round must delete something, so it
// ends: when the cut passes, or with the unit's error when chisel names
// something no entry has as its source. Only chisel's plain no content
// failures are read this way: the hard link one is a failure. Chisel names
// paths, not slices, so the entries go from every slice of the package, and
// a unit reports those of its own slice.
func (r *run) prune(p *pool.Pool) error {
	var todo []*pruning
	for si := range r.slices {
		for ai := range r.slices[si].Cuts {
			if errors.Is(r.slices[si].Cuts[ai].Base.Err, cut.ErrNoContent) {
				todo = append(todo, &pruning{u: unit{si, ai, base}, release: r.opts.Base})
			}
		}
	}
	var made []string // every release copied, whatever became of its unit
	defer func() {
		for _, dir := range made {
			if r.opts.Keep {
				fmt.Fprintf(logOf(r.opts), "pruned release: %s\n", dir)
			} else {
				_ = os.RemoveAll(dir)
			}
		}
	}()
	for round := 1; len(todo) > 0; round++ {
		var groups []pool.Group
		byUnit := map[unit]*pruning{}
		var left []*pruning
		for _, t := range todo {
			side := &r.slices[t.u.si].Cuts[t.u.ai].Base
			var ce *cut.Error
			if !errors.As(side.Err, &ce) {
				continue
			}
			if t.release == r.opts.Base {
				dir, err := os.MkdirTemp("", "chisel-diff-pruned-*")
				if err != nil {
					return err
				}
				made = append(made, dir)
				if err := copyDir(r.opts.Base, dir); err != nil {
					return err
				}
				t.release = dir
			}
			removed, err := changes.Prune(t.release, ce.Package, ce.Paths)
			if err != nil {
				return err
			}
			if len(removed) == 0 {
				side.Err = fmt.Errorf("%w (no entry of %s has that as its source, so the base cannot be pruned)", side.Err, ce.Package)
				continue
			}
			for _, p := range removed {
				if p.Slice == r.slices[t.u.si].Name {
					t.pruned = append(t.pruned, p.Key)
				}
			}
			u := r.unit(t.u)
			u.Release = t.release
			u.Label += fmt.Sprintf("-pruned-%d", round)
			u.Dir += fmt.Sprintf("-pruned-%d", round)
			byUnit[t.u] = t
			groups = append(groups, pool.Group{Lead: u})
			left = append(left, t)
		}
		if len(groups) == 0 {
			break
		}
		if err := p.Pass(groups, func(c pool.Cut) bool {
			r.done(c)
			r.mu.Lock()
			defer r.mu.Unlock()
			t := byUnit[c.Tag.(unit)]
			r.slices[t.u.si].Cuts[t.u.ai].Base.Pruned = t.pruned
			return true
		}); err != nil {
			return err
		}
		todo = todo[:0]
		for _, t := range left {
			if errors.Is(r.slices[t.u.si].Cuts[t.u.ai].Base.Err, cut.ErrNoContent) {
				todo = append(todo, t)
			}
		}
	}
	return nil
}

func logOf(o Options) io.Writer {
	if o.Log == nil {
		return io.Discard
	}
	return o.Log
}

// copyDir copies a snapshot of a release, which holds plain files only.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}
