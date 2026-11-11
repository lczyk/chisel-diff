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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// Release is the chisel-releases checkout, maybe a subdirectory of its
	// repo, or a release name such as ubuntu-26.04, whose branch of Upstream
	// is then the checkout and its tip HEAD.
	Release string
	// Revisions are <a>, <a>..<b> or <a>...<b>. With none, as git diff:
	// the working tree against the index, or the index against HEAD with
	// Cached, and with MergeBase the files on disk against where HEAD
	// parted from the branch of the release chisel.yaml is of.
	Revisions string
	MergeBase bool // with a single <a>: compare from its merge base with HEAD
	Cached    bool // with a single <a>: compare to the index
	NoIndex   bool // compare Dirs, without git
	Dirs      [2]string
	// Unrelated lets a comparison that wants a merge base compare two tips
	// that share no history, as release branches of some ubuntu releases do.
	Unrelated bool
	Upstream  string // the chisel-releases repo a release name is a branch of
	Cache     string // where the clone of Upstream is kept
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
	// Release is the release the snapshot's chisel.yaml is of, ubuntu-26.04
	// say; "" when it names none.
	Release string
}

// Sides is a resolved comparison. Remove it when done.
type Sides struct {
	Base, Head Side
	MergeBase  bool     // the base is a merge base
	Notices    []string // what a reader should know of how the sides were resolved
	Warnings   []string // what looks unusual enough to be a mistake
	tmp        string
	named      string // the release name --release gave; "" for a checkout
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
	for _, side := range []*Side{&s.Base, &s.Head} {
		side.Release, _ = releaseOf(filepath.Join(side.Dir, "chisel.yaml"))
	}
	// A release name only says which branches the revisions are looked up
	// among: a sha or another branch cuts what its own chisel.yaml says.
	if s.named != "" {
		if s.Base.Release == s.Head.Release && s.Base.Release != "" && s.Base.Release != s.named {
			s.notef("the revisions are of %s, not %s: --release only says where they are looked up, and what is cut is the release their chisel.yaml names", s.Base.Release, s.named)
		} else if s.Base.Release != s.Head.Release {
			for i, side := range []Side{s.Base, s.Head} {
				if side.Release != "" && side.Release != s.named {
					s.notef("the %s is of %s, not %s: --release only says where the revisions are looked up", [2]string{"base", "head"}[i], side.Release, s.named)
				}
			}
		}
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
	r, prefix, err := s.open(ctx, spec)
	if err != nil {
		return err
	}
	g := r.g
	s.named = r.named
	revs, mergeBase := spec.Revisions, spec.MergeBase
	if r.named != "" && spec.Cached {
		return fmt.Errorf("--cached needs a checkout, and %s is a release name", r.named)
	}
	// With no revisions, as git diff: the working tree against the index,
	// or with --cached the index against HEAD. --merge-base with none is
	// what a pull request would change: the files on disk from where HEAD
	// parted from the branch of the release chisel.yaml is of.
	switch {
	case revs != "":
	case r.named != "":
		return fmt.Errorf("%s has no working tree to compare with: give <a>..<b>, HEAD~1..HEAD say for the last change merged", r.named)
	case mergeBase:
		name, err := releaseOf(filepath.Join(spec.Release, "chisel.yaml"))
		if err != nil {
			return fmt.Errorf("--merge-base with no revision, and no release branch to compare with: %w. give <a>", err)
		}
		revs, r.branch = name, name
		what := "the files on disk are"
		if spec.Cached {
			what = "the index is"
		}
		s.notef("--merge-base with no revision: %s compared from where HEAD parted from %s, the release chisel.yaml is of", what, name)
	case spec.Cached:
		revs = "HEAD"
	default:
		return s.unstaged(g, prefix, spec.Release)
	}
	a, b, dots, err := split(revs)
	if err != nil {
		return err
	}
	switch {
	case dots > 0 && mergeBase:
		return errors.New("--merge-base does not work with a range, use <a>...<b>")
	case dots > 0 && spec.Cached:
		return errors.New("--cached takes a single revision, not a range")
	}
	s.Base.Kind = Rev
	if s.Base.Tip, s.Base.Ref, err = r.commit(a); err != nil {
		return err
	}
	s.Base.Rev = s.Base.Tip
	s.Head.Tip = r.head
	switch {
	case dots > 0:
		s.Head.Kind = Rev
		if s.Head.Tip, s.Head.Ref, err = r.commit(b); err != nil {
			return err
		}
		s.Head.Rev = s.Head.Tip
	case r.named != "":
		s.Head.Kind, s.Head.Ref, s.Head.Rev = Rev, r.named, r.head
		b = r.named
	case spec.Cached:
		s.Head.Kind = Index
	default:
		s.Head.Kind = Worktree
	}
	a = s.Base.Ref
	if s.Head.Ref != "" {
		b = s.Head.Ref
	}

	// What the base is compared with when a merge base or an ancestor is
	// asked about: the head revision, or HEAD under a working tree or index.
	against := s.Head.Tip
	if dots == 3 || mergeBase {
		bases, err := g.lines("merge-base", "--all", s.Base.Tip, against)
		switch {
		case (err != nil || len(bases) == 0) && g.line1("rev-parse", "--is-shallow-repository") == "true":
			return fmt.Errorf("%s and %s have no merge base in this shallow clone: fetch more history (fetch-depth: 0 in a workflow), or compare the tips with %s..%s", a, b, a, b)
		case (err != nil || len(bases) == 0) && !spec.Unrelated:
			return fmt.Errorf("%s and %s share no history, so they have no merge base: compare the tips with %s..%s", a, b, a, b)
		case err != nil || len(bases) == 0:
			s.notef("%s and %s share no history, so the tips are compared", a, b)
		default:
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
		}
	} else if !g.ok("merge-base", "--is-ancestor", s.Base.Rev, against) {
		if !g.ok("merge-base", s.Base.Rev, against) {
			s.notef("%s and %s share no history, so the tips are compared", a, b)
		} else {
			instead := "--merge-base " + a
			if dots > 0 {
				instead = a + "..." + b
			}
			s.notef("%s is not an ancestor of %s, so what it gained since they parted reads as lost. %s compares from where they parted", a, b, instead)
		}
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

// unstaged is what git diff with no revisions compares: the index as the
// base, the files on disk as the head.
func (s *Sides) unstaged(g *git, prefix, release string) error {
	head, err := g.commit("HEAD")
	if err != nil {
		return err
	}
	s.Base.Kind, s.Base.Tip = Index, head
	s.Head.Kind, s.Head.Tip = Worktree, head
	if err := g.index(prefix, s.Base.Dir); err != nil {
		return err
	}
	if err := copyTree(release, s.Head.Dir); err != nil {
		return err
	}
	if same, err := sameTree(s.Base.Dir, s.Head.Dir); err == nil && same {
		s.notef("nothing unstaged in the release: --cached for what is staged, --merge-base for what the branch changes as a pull request")
	}
	return nil
}

// sameTree reports whether two snapshots hold the same files with the same
// bytes.
func sameTree(a, b string) (bool, error) {
	read := func(root string) (map[string]string, error) {
		files := map[string]string{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			files[rel] = string(data)
			return nil
		})
		return files, err
	}
	x, err := read(a)
	if err != nil {
		return false, err
	}
	y, err := read(b)
	if err != nil {
		return false, err
	}
	return maps.Equal(x, y), nil
}

func (s *Sides) notef(format string, args ...any) {
	s.Notices = append(s.Notices, fmt.Sprintf(format, args...))
}

func (s *Sides) warnf(format string, args ...any) {
	s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...))
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
	alt []string // object dirs read as well as the repo's own
}

func (g *git) cmd(args ...string) *exec.Cmd {
	cmd := exec.CommandContext(g.ctx, g.bin, append([]string{"-C", g.dir}, args...)...)
	env := g.env
	if len(g.alt) > 0 {
		dirs := make([]string, len(g.alt))
		for i, d := range g.alt {
			dirs[i] = alternate(d)
		}
		if inherited := os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES"); inherited != "" {
			dirs = append(dirs, inherited)
		}
		env = append(slices.Clone(env), "GIT_ALTERNATE_OBJECT_DIRECTORIES="+strings.Join(dirs, ":"))
	}
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
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

// path is a path git prints: only the newline that ends it goes, since a
// name may start or end in a space.
func (g *git) path(args ...string) (string, error) {
	out, err := g.out(args...)
	return strings.TrimSuffix(string(out), "\n"), err
}

// line1 is git's answer, "" when it has none.
func (g *git) line1(args ...string) string {
	out, _ := g.line(args...)
	return out
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
	if env == nil {
		env = g.env
	}
	g = &git{ctx: g.ctx, bin: g.bin, dir: g.dir, env: env, alt: g.alt}
	listing, err := g.out("ls-tree", "-r", "-z", "--full-tree", treeish+":"+strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return err
	}
	type file struct{ sha, path string }
	var files []file
	for _, entry := range strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			continue
		}
		// Before anything is left out for not being yaml: a slice dir that
		// is itself a link would else read as an empty one.
		if (inRelease(path) || path == "chisel.yaml") && (fields[0] == "120000" || fields[1] != "blob") {
			return fmt.Errorf("%s in %s is a symlink or a submodule, which a release cannot be read through", path, short(treeish))
		}
		if !wanted(path) {
			continue
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
	reaped := false
	defer func() {
		// An early return leaves git mid-answer: stop it and collect it.
		if !reaped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	r := bufio.NewReader(stdout)
	for _, f := range files {
		header, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("git cat-file: %s: %w", f.path, err)
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
	reaped = true
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
		if paths[i], err = g.path("rev-parse", "--path-format=absolute", "--git-path", name); err != nil {
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
	staged := &git{ctx: g.ctx, bin: g.bin, dir: g.dir, env: []string{"GIT_INDEX_FILE=" + index, "GIT_OBJECT_DIRECTORY=" + objects}, alt: append([]string{paths[1]}, g.alt...)}
	tree, err := staged.line("write-tree")
	if err != nil {
		return fmt.Errorf("the index cannot be written as a tree: %w", err)
	}
	return staged.tree(tree, prefix, dst, nil)
}

// alternate is a path as one entry of GIT_ALTERNATE_OBJECT_DIRECTORIES,
// whose entries a colon separates: git takes one in double quotes with
// backslash escapes, which a path with a colon in it needs.
func alternate(path string) string {
	if !strings.ContainsAny(path, ":\"\\") {
		return path
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path) + `"`
}

// inRelease reports whether a path is one of the slice dirs or under one.
func inRelease(path string) bool {
	for _, dir := range sliceDirs {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
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
