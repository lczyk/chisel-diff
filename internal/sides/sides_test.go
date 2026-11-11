package sides

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lczyk/assert"
)

// repo is a throwaway git checkout holding a release. Git runs with no
// config but the repo's own, so neither the tests nor the code under test
// see the user's signing, hooks or aliases.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.invalid")
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	out, err := cmd.CombinedOutput()
	assert.NoError(r.t, err, string(out))
	return strings.TrimSpace(string(out))
}

func (r *repo) write(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	assert.NoError(r.t, os.MkdirAll(filepath.Dir(full), 0755))
	assert.NoError(r.t, os.WriteFile(full, []byte(content), 0644))
}

// commit commits everything and returns the new revision.
func (r *repo) commit(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

func (r *repo) spec(revisions string) Spec {
	return Spec{Release: r.dir, Revisions: revisions, Git: "git"}
}

func read(t *testing.T, dir, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		return "<" + filepath.Base(path) + " missing>"
	}
	return string(b)
}

// history is main with two commits and a branch off the first:
//
//	one -- two (main)
//	   \-- side (topic, checked out)
func history(t *testing.T) (r *repo, one, two, side string) {
	r = newRepo(t)
	r.write("chisel.yaml", "format: v1\n")
	r.write("slices/bash.yaml", "bash one\n")
	r.write("README.md", "not part of a release\n")
	one = r.commit("one")
	r.write("slices/bash.yaml", "bash two\n")
	r.write("slices/extra/dash.yaml", "dash two\n")
	two = r.commit("two")
	r.git("switch", "-q", "-c", "topic", one)
	r.write("slices/bash.yaml", "bash side\n")
	side = r.commit("side")
	return r, one, two, side
}

func TestRevisionForms(t *testing.T) {
	r, one, two, side := history(t)
	r.write("slices/bash.yaml", "bash edited\n")
	r.write("slices/new.yaml", "untracked\n")

	for _, c := range []struct {
		spec      Spec
		baseRev   string
		headKind  Kind
		headRev   string
		mergeBase bool
		base      string // slices/bash.yaml at the base
		head      string // and at the head
	}{
		{r.spec("main"), two, Worktree, "", false, "bash two\n", "bash edited\n"},
		{r.spec("HEAD"), side, Worktree, "", false, "bash side\n", "bash edited\n"},
		{r.spec("main..topic"), two, Rev, side, false, "bash two\n", "bash side\n"},
		{r.spec("main.."), two, Rev, side, false, "bash two\n", "bash side\n"},
		{r.spec("..main"), side, Rev, two, false, "bash side\n", "bash two\n"},
		{r.spec("main...topic"), one, Rev, side, true, "bash one\n", "bash side\n"},
		{r.spec("main..."), one, Rev, side, true, "bash one\n", "bash side\n"},
		{Spec{Release: r.dir, Revisions: "main", MergeBase: true, Git: "git"}, one, Worktree, "", true, "bash one\n", "bash edited\n"},
		{Spec{Release: r.dir, Revisions: "main", Cached: true, Git: "git"}, two, Index, "", false, "bash two\n", "bash side\n"},
		{Spec{Release: r.dir, Revisions: "main", Cached: true, MergeBase: true, Git: "git"}, one, Index, "", true, "bash one\n", "bash side\n"},
		{r.spec(one[:10]), one, Worktree, "", false, "bash one\n", "bash edited\n"},
		{r.spec("HEAD~1"), one, Worktree, "", false, "bash one\n", "bash edited\n"},
	} {
		name := c.spec.Revisions
		s, err := Resolve(context.Background(), c.spec)
		assert.NoError(t, err, name)
		assert.Equal(t, s.Base.Kind, Rev, name)
		assert.Equal(t, s.Base.Rev, c.baseRev, name)
		assert.Equal(t, s.Head.Kind, c.headKind, name)
		assert.Equal(t, s.Head.Rev, c.headRev, name)
		assert.Equal(t, s.MergeBase, c.mergeBase, name)
		assert.Equal(t, read(t, s.Base.Dir, "slices/bash.yaml"), c.base, name)
		assert.Equal(t, read(t, s.Head.Dir, "slices/bash.yaml"), c.head, name)
		assert.Equal(t, read(t, s.Base.Dir, "chisel.yaml"), "format: v1\n", name)
		// Only what chisel reads is in a snapshot.
		_, err = os.Stat(filepath.Join(s.Base.Dir, "README.md"))
		assert.That(t, os.IsNotExist(err), name)
		// An untracked file is in the working tree and nowhere else.
		_, err = os.Stat(filepath.Join(s.Head.Dir, "slices", "new.yaml"))
		assert.Equal(t, err == nil, c.headKind == Worktree, name)
		tmp := s.Keep()
		s.Remove()
		_, err = os.Stat(tmp)
		assert.That(t, os.IsNotExist(err), name)
	}
}

func TestRevisionsRefused(t *testing.T) {
	r, _, _, _ := history(t)
	for spec, want := range map[Spec]string{
		r.spec("nope"):          `"nope" is not a revision`,
		r.spec("main..nope"):    `"nope" is not a revision`,
		r.spec("^main"):         "cannot read",
		r.spec("main^!"):        "is not a revision",
		r.spec("HEAD:slices"):   "is not a revision",
		r.spec("a..b..c"):       "cannot read",
		r.spec("main....topic"): "is not a revision",
		{Release: r.dir, Revisions: "main..topic", MergeBase: true, Git: "git"}: "--merge-base does not work with a range",
		{Release: r.dir, Revisions: "main...topic", Cached: true, Git: "git"}:   "--cached takes a single revision",
		{Release: t.TempDir(), Revisions: "main", Git: "git"}:                   "is not in a git checkout",
	} {
		_, err := Resolve(context.Background(), spec)
		assert.Error(t, err, want)
	}
}

func TestNotices(t *testing.T) {
	r, one, _, _ := history(t)
	// Tips compared across a fork: what main gained reads as lost.
	s, err := Resolve(context.Background(), r.spec("main"))
	assert.NoError(t, err)
	defer s.Remove()
	assert.Len(t, s.Notices, 1)
	assert.ContainsString(t, s.Notices[0], "main is not an ancestor of HEAD")
	assert.ContainsString(t, s.Notices[0], "--merge-base main compares from where they parted")

	s, err = Resolve(context.Background(), r.spec("main..topic"))
	assert.NoError(t, err)
	defer s.Remove()
	assert.ContainsString(t, s.Notices[0], "main...topic compares from where they parted")

	// A merge base that is not the tip is named.
	s, err = Resolve(context.Background(), r.spec("main..."))
	assert.NoError(t, err)
	defer s.Remove()
	assert.Len(t, s.Notices, 1)
	assert.ContainsString(t, s.Notices[0], "base is "+one[:12]+", the merge base of main and HEAD")

	// An ancestor needs no word.
	s, err = Resolve(context.Background(), r.spec("HEAD~1"))
	assert.NoError(t, err)
	defer s.Remove()
	assert.Len(t, s.Notices, 0)
}

func TestNoMergeBase(t *testing.T) {
	r, _, _, _ := history(t)
	r.git("switch", "-q", "--orphan", "other")
	r.write("chisel.yaml", "format: v1\n")
	r.commit("unrelated")
	_, err := Resolve(context.Background(), r.spec("main..."))
	assert.Error(t, err, "have no merge base: fetch more history")
	_, err = Resolve(context.Background(), Spec{Release: r.dir, Revisions: "main", MergeBase: true, Git: "git"})
	assert.Error(t, err, "have no merge base")
	// Tips still compare.
	s, err := Resolve(context.Background(), r.spec("main.."))
	assert.NoError(t, err)
	s.Remove()
}

func TestIndexIsWhatIsStaged(t *testing.T) {
	r, _, _, _ := history(t)
	r.write("slices/bash.yaml", "bash staged\n")
	r.write("slices/staged.yaml", "staged\n")
	r.git("add", "slices/bash.yaml", "slices/staged.yaml")
	r.write("slices/bash.yaml", "bash unstaged\n")
	// Intent to add is not staged, whether the file is still there or not.
	r.write("slices/intent.yaml", "intent\n")
	r.write("slices/gone.yaml", "gone\n")
	r.git("add", "-N", "slices/intent.yaml", "slices/gone.yaml")
	assert.NoError(t, os.Remove(filepath.Join(r.dir, "slices", "gone.yaml")))
	before := r.git("ls-files", "-s")

	s, err := Resolve(context.Background(), Spec{Release: r.dir, Revisions: "HEAD", Cached: true, Git: "git"})
	assert.NoError(t, err)
	defer s.Remove()
	assert.Equal(t, read(t, s.Head.Dir, "slices/bash.yaml"), "bash staged\n")
	assert.Equal(t, read(t, s.Head.Dir, "slices/staged.yaml"), "staged\n")
	for _, name := range []string{"intent.yaml", "gone.yaml"} {
		_, err := os.Stat(filepath.Join(s.Head.Dir, "slices", name))
		assert.That(t, os.IsNotExist(err), name)
	}
	// The checkout's own index is as it was.
	assert.Equal(t, r.git("ls-files", "-s"), before)
}

func TestIndexWithUnmergedEntriesIsRefused(t *testing.T) {
	r, _, _, _ := history(t)
	cmd := exec.Command("git", "-C", r.dir, "merge", "-q", "main")
	_ = cmd.Run() // conflicts on slices/bash.yaml
	_, err := Resolve(context.Background(), Spec{Release: r.dir, Revisions: "HEAD", Cached: true, Git: "git"})
	assert.Error(t, err, "the index cannot be written as a tree")
}

func TestReleaseInASubdirectory(t *testing.T) {
	r := newRepo(t)
	r.write("releases/24.04/chisel.yaml", "format: v1\n")
	r.write("releases/24.04/slices/bash.yaml", "bash one\n")
	r.write("releases/26.04/chisel.yaml", "format: v3\n")
	r.write("slices/stray.yaml", "not of this release\n")
	r.commit("one")
	r.write("releases/24.04/slices/bash.yaml", "bash edited\n")
	spec := Spec{Release: filepath.Join(r.dir, "releases", "24.04"), Revisions: "HEAD", Git: "git"}
	s, err := Resolve(context.Background(), spec)
	assert.NoError(t, err)
	defer s.Remove()
	assert.Equal(t, read(t, s.Base.Dir, "chisel.yaml"), "format: v1\n")
	assert.Equal(t, read(t, s.Base.Dir, "slices/bash.yaml"), "bash one\n")
	assert.Equal(t, read(t, s.Head.Dir, "slices/bash.yaml"), "bash edited\n")
	_, err = os.Stat(filepath.Join(s.Base.Dir, "slices", "stray.yaml"))
	assert.That(t, os.IsNotExist(err))

	// A revision that has no release there.
	spec.Release = filepath.Join(r.dir, "releases")
	_, err = Resolve(context.Background(), spec)
	assert.Error(t, err, "holds no chisel release")
}

func TestSymlinkInARevisionIsRefused(t *testing.T) {
	r := newRepo(t)
	r.write("chisel.yaml", "format: v1\n")
	r.write("elsewhere.yaml", "bash\n")
	assert.NoError(t, os.MkdirAll(filepath.Join(r.dir, "slices"), 0755))
	assert.NoError(t, os.Symlink("../elsewhere.yaml", filepath.Join(r.dir, "slices", "bash.yaml")))
	r.commit("one")
	s, err := Resolve(context.Background(), r.spec("HEAD"))
	if err == nil {
		s.Remove()
	}
	assert.Error(t, err, "slices/bash.yaml in")
	assert.Error(t, err, "is a symlink or a submodule")
}

func TestSymlinkedSliceDirInARevisionIsRefused(t *testing.T) {
	r := newRepo(t)
	r.write("chisel.yaml", "format: v1\n")
	r.write("real/bash.yaml", "bash\n")
	assert.NoError(t, os.Symlink("real", filepath.Join(r.dir, "slices")))
	r.commit("one")
	// Not an empty release: the link is what the revision holds.
	_, err := Resolve(context.Background(), r.spec("HEAD.."))
	assert.Error(t, err, "slices in")
	assert.Error(t, err, "is a symlink or a submodule")
}

func TestPathsWithSpacesAndColons(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	for _, who := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+who+"_NAME", "t")
		t.Setenv("GIT_"+who+"_EMAIL", "t@example.invalid")
	}
	// The checkout sits under a name with a colon, which separates the
	// entries of git's alternates, and the release in a subdirectory whose
	// name starts and ends with a space.
	r := &repo{t: t, dir: filepath.Join(t.TempDir(), "work:next")}
	assert.NoError(t, os.MkdirAll(r.dir, 0755))
	r.git("init", "-q", "-b", "main")
	r.write(" rel /chisel.yaml", "format: v1\n")
	r.write(" rel /slices/bash.yaml", "bash one\n")
	r.commit("one")
	r.write(" rel /slices/bash.yaml", "bash staged\n")
	r.git("add", "-A")
	spec := Spec{Release: filepath.Join(r.dir, " rel "), Revisions: "HEAD", Cached: true, Git: "git"}
	s, err := Resolve(context.Background(), spec)
	assert.NoError(t, err)
	defer s.Remove()
	assert.Equal(t, read(t, s.Base.Dir, "slices/bash.yaml"), "bash one\n")
	assert.Equal(t, read(t, s.Head.Dir, "slices/bash.yaml"), "bash staged\n")
}

func TestWorkingTreeAsChiselReadsIt(t *testing.T) {
	r := newRepo(t)
	r.write("chisel.yaml", "format: v1\n")
	r.commit("one")
	// No slices dir at the base: the snapshot still has one.
	r.write("slices/bash.yaml", "bash\n")
	r.write("slices/notes.txt", "not yaml\n")
	r.write("bin-slices/deep/er/dash.yaml", "dash\n")
	r.write("elsewhere/zsh.yaml", "zsh\n")
	r.write("elsewhere/dir/fish.yaml", "fish\n")
	assert.NoError(t, os.Symlink("../elsewhere/zsh.yaml", filepath.Join(r.dir, "slices", "zsh.yaml")))
	assert.NoError(t, os.Symlink("../elsewhere/dir", filepath.Join(r.dir, "slices", "linked.yaml")))
	s, err := Resolve(context.Background(), r.spec("HEAD"))
	assert.NoError(t, err)
	defer s.Remove()
	info, err := os.Stat(filepath.Join(s.Base.Dir, "slices"))
	assert.NoError(t, err)
	assert.That(t, info.IsDir())
	assert.Equal(t, read(t, s.Head.Dir, "slices/bash.yaml"), "bash\n")
	assert.Equal(t, read(t, s.Head.Dir, "bin-slices/deep/er/dash.yaml"), "dash\n")
	// A symlinked file is read through, a symlinked directory is not entered.
	assert.Equal(t, read(t, s.Head.Dir, "slices/zsh.yaml"), "zsh\n")
	for _, gone := range []string{"slices/notes.txt", "slices/linked.yaml", "elsewhere"} {
		_, err := os.Stat(filepath.Join(s.Head.Dir, gone))
		assert.That(t, os.IsNotExist(err), gone)
	}
}

func TestNoIndex(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for dir, content := range map[string]string{a: "bash a\n", b: "bash b\n"} {
		assert.NoError(t, os.MkdirAll(filepath.Join(dir, "slices"), 0755))
		assert.NoError(t, os.WriteFile(filepath.Join(dir, "chisel.yaml"), []byte("format: v1\n"), 0644))
		assert.NoError(t, os.WriteFile(filepath.Join(dir, "slices", "bash.yaml"), []byte(content), 0644))
	}
	// No git is run: the binary named does not exist.
	s, err := Resolve(context.Background(), Spec{NoIndex: true, Dirs: [2]string{a, b}, Git: "/nonexistent/git"})
	assert.NoError(t, err)
	defer s.Remove()
	assert.Equal(t, s.Base.Kind, Dir)
	assert.Equal(t, s.Base.Path, a)
	assert.Equal(t, s.Head.Path, b)
	assert.Equal(t, read(t, s.Base.Dir, "slices/bash.yaml"), "bash a\n")
	assert.Equal(t, read(t, s.Head.Dir, "slices/bash.yaml"), "bash b\n")

	_, err = Resolve(context.Background(), Spec{NoIndex: true, Dirs: [2]string{a, t.TempDir()}, Git: "git"})
	assert.Error(t, err, "is not a chisel release")
	_, err = Resolve(context.Background(), Spec{NoIndex: true, Cached: true, Dirs: [2]string{a, b}, Git: "git"})
	assert.Error(t, err, "--no-index takes two directories")
}
