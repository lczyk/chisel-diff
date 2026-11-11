package sides

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lczyk/assert"
)

func chiselYAML(version string) string {
	return "format: v3\narchives:\n  ubuntu:\n    version: " + version + "\n"
}

// upstreamRepo is a stand-in for chisel-releases, with branches the way it
// has them: ubuntu-24.04 with a root of its own, and ubuntu-26.04
// unrelated to it.
//
//	a24 (ubuntu-24.04)
//	r26 -- t26 (ubuntu-26.04)
func upstreamRepo(t *testing.T) (r *repo, a24, r26, t26 string) {
	r = newRepo(t)
	r.git("switch", "-q", "--orphan", "ubuntu-24.04")
	r.write("chisel.yaml", chiselYAML("24.04"))
	r.write("slices/bash.yaml", "bash 24.04\n")
	a24 = r.commit("24.04")
	r.git("switch", "-q", "--orphan", "ubuntu-26.04")
	r.write("chisel.yaml", chiselYAML("26.04"))
	r.write("slices/bash.yaml", "bash r26\n")
	r26 = r.commit("26.04 root")
	r.write("slices/bash.yaml", "bash t26\n")
	t26 = r.commit("26.04 tip")
	return r, a24, r26, t26
}

func named(up *repo, cache, release, revisions string) Spec {
	return Spec{Release: release, Revisions: revisions, Upstream: up.dir, Cache: cache, Git: "git"}
}

func TestReleaseOf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chisel.yaml")
	for content, want := range map[string]string{
		chiselYAML("26.10"): "ubuntu-26.10", // not the float 26.1
		chiselYAML("20.04"): "ubuntu-20.04",
		"archives:\n  fips:\n    version: '22.04'\n  esm:\n    version: '22.04'\n": "ubuntu-22.04",
		"archives:\n  fips:\n    version: 22.04\n  esm:\n    version: 24.04\n":     "",
		"format: v1\n": "",
	} {
		assert.NoError(t, os.WriteFile(path, []byte(content), 0644))
		got, err := releaseOf(path)
		if want == "" {
			assert.Error(t, err, "names no")
			continue
		}
		assert.NoError(t, err)
		assert.Equal(t, got, want)
	}
}

func TestSameRepo(t *testing.T) {
	for _, url := range []string{
		"https://github.com/canonical/chisel-releases",
		"https://github.com/canonical/chisel-releases.git",
		"https://github.com/canonical/chisel-releases/",
		"git@github.com:canonical/chisel-releases.git",
		"ssh://git@github.com/Canonical/chisel-releases",
	} {
		assert.That(t, sameRepo(url, Upstream), url)
	}
	assert.That(t, !sameRepo("https://github.com/lczyk/chisel-releases", Upstream))
}

func TestNamedRelease(t *testing.T) {
	up, a24, r26, t26 := upstreamRepo(t)
	cache := filepath.Join(t.TempDir(), "cache", "chisel-releases.git")

	// No revisions: the tip and its first parent, with a warning.
	s, err := Resolve(context.Background(), named(up, cache, "ubuntu-26.04", ""))
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Rev, r26)
	assert.Equal(t, s.Head.Kind, Rev)
	assert.Equal(t, s.Head.Rev, t26)
	assert.Equal(t, s.Head.Ref, "ubuntu-26.04")
	assert.Equal(t, read(t, s.Base.Dir, "slices/bash.yaml"), "bash r26\n")
	assert.Equal(t, read(t, s.Head.Dir, "slices/bash.yaml"), "bash t26\n")
	assert.Equal(t, s.Base.Release, "ubuntu-26.04")
	assert.Len(t, s.Warnings, 1)
	assert.ContainsString(t, s.Warnings[0], "the tip of ubuntu-26.04 is compared with its first parent")
	assert.ContainsString(t, s.Notices[0], "cloned "+up.dir+" into "+cache)
	s.Remove()

	// A later run fetches what upstream gained.
	up.write("slices/bash.yaml", "bash later\n")
	later := up.commit("later")
	s, err = Resolve(context.Background(), named(up, cache, "ubuntu-26.04", "HEAD~2"))
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Rev, r26)
	assert.Equal(t, s.Head.Rev, later)
	assert.Len(t, s.Notices, 0)
	assert.Len(t, s.Warnings, 0)
	s.Remove()

	// Two shas of upstream.
	s, err = Resolve(context.Background(), named(up, cache, "ubuntu-26.04", "@~2..@"))
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Rev, r26)
	assert.Equal(t, s.Head.Rev, later)
	s.Remove()

	s, err = Resolve(context.Background(), named(up, cache, "ubuntu-26.04", r26+".."+t26))
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Rev, r26)
	assert.Equal(t, s.Head.Rev, t26)
	s.Remove()

	// Across releases with no history in common.
	_, err = Resolve(context.Background(), named(up, cache, "ubuntu-26.04", "ubuntu-24.04..."))
	assert.Error(t, err, "ubuntu-24.04 and ubuntu-26.04 share no history")
	spec := named(up, cache, "ubuntu-26.04", "ubuntu-24.04")
	spec.MergeBase, spec.Unrelated = true, true
	s, err = Resolve(context.Background(), spec)
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Rev, a24)
	assert.Equal(t, s.Base.Release, "ubuntu-24.04")
	assert.Equal(t, s.Head.Release, "ubuntu-26.04")
	s.Remove()

	// A fetch that fails leaves the clone as it was, and says so.
	gone := named(up, cache, "ubuntu-26.04", "HEAD~1")
	gone.Upstream = filepath.Join(t.TempDir(), "gone")
	s, err = Resolve(context.Background(), gone)
	assert.NoError(t, err)
	assert.Len(t, s.Warnings, 1)
	assert.ContainsString(t, s.Warnings[0], "so its branches are as last fetched")
	s.Remove()

	for spec, want := range map[Spec]string{
		named(up, cache, "ubuntu-99.04", ""):         "ubuntu-99.04 is not a branch of",
		named(up, cache, "not-a-release", "a"):       "is not a directory or a release name like ubuntu-26.04",
		named(up, cache, "ubuntu-26.04", "nope"):     `"nope" is not a revision of`,
		named(up, cache, "ubuntu-26.04", "HEAD@{1}"): "keeps no reflog of ubuntu-26.04",
		named(up, cache, "ubuntu-26.04", "@{1}"):     "keeps no reflog of ubuntu-26.04",
		{Release: "ubuntu-26.04", Revisions: "HEAD", Cached: true, Upstream: up.dir, Cache: cache, Git: "git"}: "--cached needs a checkout",
		{Release: "ubuntu-26.04", MergeBase: true, Upstream: up.dir, Cache: cache, Git: "git"}:                 "--merge-base takes the revision",
		{Release: "ubuntu-26.04", Upstream: up.dir, Git: "git"}:                                                "no place to keep a clone",
	} {
		_, err := Resolve(context.Background(), spec)
		assert.Error(t, err, want)
	}
}

// checkout clones up as a contributor would, with upstream as the remote
// "canonical", on a topic branch off ubuntu-26.04 with an edit on disk.
func checkout(t *testing.T, up *repo) *repo {
	t.Helper()
	c := &repo{t: t, dir: filepath.Join(t.TempDir(), "checkout")}
	up.git("clone", "-q", "-o", "canonical", "-b", "ubuntu-26.04", up.dir, c.dir)
	c.git("switch", "-q", "-c", "topic")
	c.write("slices/bash.yaml", "bash topic\n")
	c.commit("topic")
	c.git("branch", "-q", "-D", "ubuntu-26.04")
	c.write("slices/bash.yaml", "bash on disk\n")
	return c
}

func TestBareInACheckout(t *testing.T) {
	up, _, r26, t26 := upstreamRepo(t)
	c := checkout(t, up)
	cache := filepath.Join(t.TempDir(), "clone.git")

	// No local branch: the remote's, by the release chisel.yaml is of.
	s, err := Resolve(context.Background(), named(up, cache, c.dir, ""))
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Ref, "canonical/ubuntu-26.04")
	assert.Equal(t, s.Base.Rev, t26)
	assert.Equal(t, s.Head.Kind, Worktree)
	assert.Equal(t, read(t, s.Head.Dir, "slices/bash.yaml"), "bash on disk\n")
	assert.Len(t, s.Notices, 2)
	assert.ContainsString(t, s.Notices[0], "from where HEAD parted from ubuntu-26.04")
	assert.ContainsString(t, s.Notices[1], "ubuntu-26.04 is not a local branch: using canonical/ubuntu-26.04 ("+t26[:12])
	assert.Len(t, s.Warnings, 0)
	s.Remove()
	_, err = os.Stat(cache)
	assert.That(t, os.IsNotExist(err), "nothing is fetched while the remote has the branch")

	// A tag of the release's name is not the branch.
	c.git("tag", "ubuntu-26.04", r26)
	s, err = Resolve(context.Background(), named(up, cache, c.dir, ""))
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Ref, "canonical/ubuntu-26.04")
	s.Remove()
	c.git("tag", "-d", "ubuntu-26.04")

	// A local branch behind the remote's is used, with a warning.
	c.git("branch", "-q", "ubuntu-26.04", r26)
	s, err = Resolve(context.Background(), named(up, cache, c.dir, ""))
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Ref, "ubuntu-26.04")
	assert.Equal(t, s.Base.Rev, r26)
	assert.Len(t, s.Warnings, 1)
	assert.ContainsString(t, s.Warnings[0], "the local branch ubuntu-26.04 is 1 commits behind canonical/ubuntu-26.04")
	s.Remove()

	// Ahead of it, and parted from it.
	c.git("branch", "-q", "-f", "ubuntu-26.04", "HEAD")
	s, err = Resolve(context.Background(), named(up, cache, c.dir, "ubuntu-26.04"))
	assert.NoError(t, err)
	assert.ContainsString(t, s.Warnings[0], "has 1 commits that canonical/ubuntu-26.04 does not")
	s.Remove()
	c.git("branch", "-q", "-f", "ubuntu-26.04", r26)
	c.git("checkout", "-q", "--", "slices/bash.yaml")
	c.git("switch", "-q", "ubuntu-26.04")
	c.write("slices/bash.yaml", "bash parted\n")
	c.commit("parted")
	c.git("switch", "-q", "topic")
	c.write("slices/bash.yaml", "bash on disk\n")
	s, err = Resolve(context.Background(), named(up, cache, c.dir, "ubuntu-26.04..HEAD"))
	assert.NoError(t, err)
	assert.ContainsString(t, s.Warnings[0], "have parted, 1 and 1 commits each")
	s.Remove()
}

func TestFetchedBesideACheckout(t *testing.T) {
	up, a24, _, t26 := upstreamRepo(t)
	// A checkout with neither the branch nor a remote for upstream.
	c := newRepo(t)
	c.git("fetch", "-q", up.dir, "ubuntu-26.04:topic")
	c.git("switch", "-q", "topic")
	c.write("slices/bash.yaml", "bash on disk\n")
	cache := filepath.Join(t.TempDir(), "clone.git")

	s, err := Resolve(context.Background(), named(up, cache, c.dir, ""))
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Rev, t26)
	assert.ContainsString(t, s.Notices[1], "cloned "+up.dir)
	assert.ContainsString(t, s.Notices[2], "ubuntu-26.04 is a branch of neither this checkout nor its remotes")
	s.Remove()

	// A base only the clone has is written out from it.
	spec := named(up, cache, c.dir, "ubuntu-24.04")
	s, err = Resolve(context.Background(), spec)
	assert.NoError(t, err)
	assert.Equal(t, s.Base.Rev, a24)
	assert.Equal(t, read(t, s.Base.Dir, "slices/bash.yaml"), "bash 24.04\n")
	assert.ContainsString(t, s.Notices[len(s.Notices)-1], "share no history, so the tips are compared")
	s.Remove()

	// The index against a base only the clone has.
	c.git("add", "slices/bash.yaml")
	spec.Cached = true
	s, err = Resolve(context.Background(), spec)
	assert.NoError(t, err)
	assert.Equal(t, s.Head.Kind, Index)
	assert.Equal(t, read(t, s.Head.Dir, "slices/bash.yaml"), "bash on disk\n")
	assert.Equal(t, read(t, s.Base.Dir, "slices/bash.yaml"), "bash 24.04\n")
	s.Remove()

	// The checkout gained neither a ref nor an object.
	assert.That(t, exec.Command("git", "-C", c.dir, "rev-parse", "--verify", "--quiet", "ubuntu-24.04").Run() != nil)
	assert.That(t, exec.Command("git", "-C", c.dir, "cat-file", "-e", a24).Run() != nil)
}
