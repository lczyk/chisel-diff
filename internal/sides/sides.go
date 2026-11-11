// Package sides says what the two sides of a comparison are and writes each
// out as a release dir chisel can read: a revision from git's objects, the
// index as git writes it into a tree, the working tree or a plain directory
// as chisel would read it. It is the only package that runs git.
package sides

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// The directories chisel reads package slices files from, beside chisel.yaml.
var sliceDirs = []string{"slices", "bin-slices"}

// Kind is what a side is.
type Kind string

const (
	Rev      Kind = "rev"
	Worktree Kind = "worktree"
	Index    Kind = "index"
	Dir      Kind = "dir"
)

// Spec is the comparison asked for, as the command line gives it.
type Spec struct {
	Release   string // the chisel-releases checkout, maybe a subdirectory of its repo
	Revisions string // <a>, <a>..<b> or <a>...<b>
	MergeBase bool   // with a single <a>: compare from its merge base with HEAD
	Cached    bool   // with a single <a>: compare to the index
	NoIndex   bool   // compare Dirs, without git
	Dirs      [2]string
	Git       string // the git binary
}

// Side is one side of the comparison.
type Side struct {
	Kind Kind
	Ref  string // the revision as given; "" for a side that is none
	Tip  string // what Ref resolves to; HEAD for the working tree and the index
	Rev  string // the revision compared: the merge base or Tip; "" for a side that is none
	Path string // the directory given, for a Dir side
	Dir  string // the snapshot: chisel.yaml and the slice dirs
}

// Sides is a resolved comparison. Remove it when done.
type Sides struct {
	Base, Head Side
	MergeBase  bool     // the base is a merge base
	Notices    []string // what a reader should know of how the sides were resolved
	tmp        string
}

// Remove deletes the snapshots.
func (s *Sides) Remove() {
	if s.tmp != "" {
		_ = os.RemoveAll(s.tmp)
	}
}

// Keep says where the snapshots are, for a run that leaves them behind.
func (s *Sides) Keep() string { return s.tmp }

// Resolve works out the two sides and writes both snapshots under a temp dir.
func Resolve(ctx context.Context, spec Spec) (*Sides, error) {
	s := &Sides{}
	var err error
	if s.tmp, err = os.MkdirTemp("", "chisel-diff-sides-*"); err != nil {
		return nil, err
	}
	s.Base.Dir, s.Head.Dir = filepath.Join(s.tmp, "base"), filepath.Join(s.tmp, "head")
	if spec.NoIndex {
		err = s.dirs(spec)
	} else {
		err = s.revisions(ctx, spec)
	}
	if err != nil {
		s.Remove()
		return nil, err
	}
	return s, nil
}

func (s *Sides) dirs(spec Spec) error {
	if spec.Cached || spec.MergeBase {
		return errors.New("--no-index takes two directories and neither --cached nor --merge-base")
	}
	for i, side := range []*Side{&s.Base, &s.Head} {
		side.Kind, side.Path = Dir, spec.Dirs[i]
		if err := copyTree(spec.Dirs[i], side.Dir); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sides) revisions(ctx context.Context, spec Spec) error {
	g := &git{ctx: ctx, bin: spec.Git, dir: spec.Release}
	prefix, err := g.line("rev-parse", "--show-prefix")
	if err != nil {
		return fmt.Errorf("%s is not in a git checkout: %w", spec.Release, err)
	}
	a, b, dots, err := split(spec.Revisions)
	if err != nil {
		return err
	}
	switch {
	case dots > 0 && spec.MergeBase:
		return errors.New("--merge-base does not work with a range, use <a>...<b>")
	case dots > 0 && spec.Cached:
		return errors.New("--cached takes a single revision, not a range")
	}
	head, err := g.commit("HEAD")
	if err != nil {
		return err
	}
	s.Base.Kind, s.Base.Ref = Rev, a
	if s.Base.Tip, err = g.commit(a); err != nil {
		return err
	}
	s.Base.Rev = s.Base.Tip
	s.Head.Tip = head
	switch {
	case dots > 0:
		s.Head.Kind, s.Head.Ref = Rev, b
		if s.Head.Tip, err = g.commit(b); err != nil {
			return err
		}
		s.Head.Rev = s.Head.Tip
	case spec.Cached:
		s.Head.Kind = Index
	default:
		s.Head.Kind = Worktree
	}

	// What the base is compared with when a merge base or an ancestor is
	// asked about: the head revision, or HEAD under a working tree or index.
	against := s.Head.Tip
	if dots == 3 || spec.MergeBase {
		bases, err := g.lines("merge-base", "--all", s.Base.Tip, against)
		if err != nil || len(bases) == 0 {
			return fmt.Errorf("%s and %s have no merge base: fetch more history (fetch-depth: 0 in a workflow), or compare the tips with %s..%s", a, b, a, b)
		}
		if len(bases) > 1 {
			if spec.MergeBase {
				return fmt.Errorf("multiple merge bases found for %s and HEAD", a)
			}
			s.notef("%s and %s have %d merge bases, using %s", a, b, len(bases), short(bases[0]))
		}
		s.Base.Rev, s.MergeBase = bases[0], true
		if s.Base.Rev != s.Base.Tip {
			s.notef("base is %s, the merge base of %s and %s", short(s.Base.Rev), a, b)
		}
	} else if !g.ok("merge-base", "--is-ancestor", s.Base.Rev, against) {
		instead := "--merge-base " + a
		if dots > 0 {
			instead = a + "..." + b
		}
		s.notef("%s is not an ancestor of %s, so what it gained since they parted reads as lost. %s compares from where they parted", a, b, instead)
	}

	if err := g.tree(s.Base.Rev, prefix, s.Base.Dir, nil); err != nil {
		return err
	}
	switch s.Head.Kind {
	case Rev:
		return g.tree(s.Head.Rev, prefix, s.Head.Dir, nil)
	case Index:
		return g.index(prefix, s.Head.Dir)
	}
	return copyTree(spec.Release, s.Head.Dir)
}

func (s *Sides) notef(format string, args ...any) {
	s.Notices = append(s.Notices, fmt.Sprintf(format, args...))
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// split reads the revisions argument as git diff does: <a>...<b> first,
// then <a>..<b>, an empty side being HEAD. Git allows no ".." in a ref
// name, so the first one found is the separator.
func split(revs string) (a, b string, dots int, err error) {
	a, b = revs, "HEAD"
	if x, y, ok := strings.Cut(revs, "..."); ok {
		a, b, dots = x, y, 3
	} else if x, y, ok := strings.Cut(revs, ".."); ok {
		a, b, dots = x, y, 2
	}
	if a == "" {
		a = "HEAD"
	}
	if b == "" {
		b = "HEAD"
	}
	for _, rev := range []string{a, b} {
		if strings.HasPrefix(rev, "^") || strings.HasPrefix(rev, "-") || strings.Contains(rev, "..") {
			return "", "", 0, fmt.Errorf("cannot read %q as <a>, <a>..<b> or <a>...<b>", revs)
		}
	}
	return a, b, dots, nil
}

// git runs git in the release dir.
type git struct {
	ctx context.Context
	bin string
	dir string
	env []string // on top of the process's own
}

func (g *git) cmd(args ...string) *exec.Cmd {
	cmd := exec.CommandContext(g.ctx, g.bin, append([]string{"-C", g.dir}, args...)...)
	if g.env != nil {
		cmd.Env = append(os.Environ(), g.env...)
	}
	return cmd
}

// out is git's stdout, its stderr folded into the error.
func (g *git) out(args ...string) ([]byte, error) {
	cmd := g.cmd(args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}

func (g *git) line(args ...string) (string, error) {
	out, err := g.out(args...)
	return strings.TrimSpace(string(out)), err
}

func (g *git) lines(args ...string) ([]string, error) {
	out, err := g.out(args...)
	return strings.Fields(string(out)), err
}

func (g *git) ok(args ...string) bool { return g.cmd(args...).Run() == nil }

// commit is the commit rev names.
func (g *git) commit(rev string) (string, error) {
	sha, err := g.line("rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil || sha == "" {
		return "", fmt.Errorf("%q is not a revision of this checkout", rev)
	}
	return sha, nil
}

// tree writes the release under prefix of a tree-ish out to dst, from git's
// objects: the tree as committed, whatever export-ignore and the like say.
func (g *git) tree(treeish, prefix, dst string, env []string) error {
	g = &git{ctx: g.ctx, bin: g.bin, dir: g.dir, env: env}
	listing, err := g.out("ls-tree", "-r", "-z", "--full-tree", treeish+":"+strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return err
	}
	type file struct{ sha, path string }
	var files []file
	for _, entry := range strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !wanted(path) {
			continue
		}
		if fields[0] == "120000" || fields[1] != "blob" {
			return fmt.Errorf("%s in %s is a symlink or a submodule, which a release cannot be read through", path, short(treeish))
		}
		files = append(files, file{fields[2], path})
	}
	if len(files) == 0 {
		return fmt.Errorf("%s holds no chisel release at %q", short(treeish), prefix)
	}

	cmd := g.cmd("cat-file", "--batch")
	var ask, stderr bytes.Buffer
	for _, f := range files {
		ask.WriteString(f.sha + "\n")
	}
	cmd.Stdin, cmd.Stderr = &ask, &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r := bufio.NewReader(stdout)
	for _, f := range files {
		header, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("git cat-file: %s: %w", strings.TrimSpace(stderr.String()), err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			// A partial clone that cannot fetch, or a hash staged without
			// its content. Never made up from the working tree.
			return fmt.Errorf("git cannot give %s (%s): %s", f.path, short(f.sha), strings.TrimSpace(header))
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return fmt.Errorf("git cat-file: %q", strings.TrimSpace(header))
		}
		data := make([]byte, size+1) // and the newline that ends an object
		if _, err := io.ReadFull(r, data); err != nil {
			return fmt.Errorf("git cat-file: %s: %w", f.path, err)
		}
		if err := write(filepath.Join(dst, filepath.FromSlash(f.path)), data[:size]); err != nil {
			return err
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git cat-file: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return finish(dst)
}

// index writes what is staged out to dst. Git makes the index into a tree
// itself, which leaves out intent-to-add entries and refuses unmerged ones.
// It does so on a copy of the index file and with new objects going to a
// temp dir, so the checkout's index and object store are left as they are.
func (g *git) index(prefix, dst string) error {
	// Asked of git, so that a linked worktree gives its own index and the
	// repository's shared objects.
	var paths [2]string
	for i, name := range []string{"index", "objects"} {
		var err error
		if paths[i], err = g.line("rev-parse", "--path-format=absolute", "--git-path", name); err != nil {
			return err
		}
	}
	tmp, err := os.MkdirTemp("", "chisel-diff-index-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	index, objects := filepath.Join(tmp, "index"), filepath.Join(tmp, "objects")
	if err := os.Mkdir(objects, 0755); err != nil {
		return err
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		if err := os.WriteFile(index, raw, 0644); err != nil {
			return err
		}
	}
	env := []string{"GIT_INDEX_FILE=" + index, "GIT_OBJECT_DIRECTORY=" + objects, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + paths[1]}
	staged := &git{ctx: g.ctx, bin: g.bin, dir: g.dir, env: env}
	tree, err := staged.line("write-tree")
	if err != nil {
		return fmt.Errorf("the index cannot be written as a tree: %w", err)
	}
	return g.tree(tree, prefix, dst, env)
}

// wanted reports whether chisel reads a path of a release.
func wanted(path string) bool {
	if path == "chisel.yaml" {
		return true
	}
	if !strings.HasSuffix(path, ".yaml") {
		return false
	}
	for _, dir := range sliceDirs {
		if strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

// copyTree copies a release dir as chisel would read it: chisel.yaml and
// every .yaml under the slice dirs, tracked or not, a symlinked file by its
// bytes, a symlinked directory not entered.
func copyTree(src, dst string) error {
	data, err := os.ReadFile(filepath.Join(src, "chisel.yaml"))
	if err != nil {
		return fmt.Errorf("%s is not a chisel release: %w", src, err)
	}
	if err := write(filepath.Join(dst, "chisel.yaml"), data); err != nil {
		return err
	}
	for _, dir := range sliceDirs {
		root := filepath.Join(src, dir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root && errors.Is(err, fs.ErrNotExist) {
					return fs.SkipAll
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
				return nil
			}
			if info, err := os.Stat(path); err != nil {
				return err
			} else if info.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			return write(filepath.Join(dst, rel), data)
		})
		if err != nil {
			return err
		}
	}
	return finish(dst)
}

// finish gives a snapshot the slices dir chisel wants even when the release
// has no package yet.
func finish(dst string) error {
	if _, err := os.Stat(filepath.Join(dst, "chisel.yaml")); err != nil {
		return fmt.Errorf("the release has no chisel.yaml: %w", err)
	}
	return os.MkdirAll(filepath.Join(dst, "slices"), 0755)
}

func write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
