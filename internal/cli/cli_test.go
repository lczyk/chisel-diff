package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lczyk/assert"

	"github.com/lczyk/chisel-diff/internal/sides"
)

func TestParseArgs(t *testing.T) {
	f, rest, err := parseArgs([]string{"--release", "/r", "--arch", "amd64,arm64", "--arch=s390x", "-v",
		"main...", "--slice", "bins,libs", "--json", "--fail-missing", "--keep", "--no-group", "bash", "--merge-base"}, io.Discard)
	assert.NoError(t, err)
	assert.EqualArrays(t, rest, []string{"main...", "bash"})
	assert.Equal(t, f.Release, "/r")
	assert.EqualArrays(t, f.Arches, []string{"amd64", "arm64", "s390x"})
	assert.EqualArrays(t, f.Slices, []string{"bins", "libs"})
	assert.That(t, f.JSON && f.FailMissing && f.Keep && f.NoGroup && f.MergeBase)
	assert.Len(t, f.Verbose, 1)
	assert.Equal(t, f.Jobs, -1)

	f, _, err = parseArgs([]string{"HEAD"}, io.Discard)
	assert.NoError(t, err)
	assert.Equal(t, f.Release, "")
	assert.Nil(t, f.Slices)
	assert.That(t, !f.noRevs)

	// "--" before any positional: no revisions, as for git diff.
	f, rest, err = parseArgs([]string{"-x", "--", "HEAD"}, io.Discard)
	assert.NoError(t, err)
	assert.That(t, f.noRevs && f.XRelease)
	assert.EqualArrays(t, rest, []string{"HEAD"})
	f, rest, err = parseArgs([]string{"main", "--", "bash"}, io.Discard)
	assert.NoError(t, err)
	assert.That(t, !f.noRevs)
	assert.EqualArrays(t, rest, []string{"main", "bash"})

	_, _, err = parseArgs([]string{"--nope", "HEAD"}, io.Discard)
	assert.Error(t, err, "unknown flag `nope'")

	var help bytes.Buffer
	f, _, err = parseArgs([]string{"HEAD", "-h"}, &help)
	assert.NoError(t, err)
	assert.That(t, f.help)
	assert.ContainsString(t, help.String(), "chisel-diff [flags] [<revisions>] [--] [<package>...]")
	assert.ContainsString(t, help.String(), "chisel-diff [flags] --no-index <dir> <dir> [<package>...]")
	assert.ContainsString(t, help.String(), "--fail-missing")
}

func TestSpec(t *testing.T) {
	t.Setenv("GIT", "")
	t.Setenv("XDG_CACHE_HOME", "/c")
	clone := "/c/chisel-diff/chisel-releases.git"
	spec, pkgs, err := flags{}.spec([]string{"main...topic", "bash", "slices/dash.yaml", "../rel/slices/extra/zsh.yaml"})
	assert.NoError(t, err)
	assert.Equal(t, spec, sides.Spec{Release: ".", Revisions: "main...topic", Upstream: sides.Upstream, Cache: clone, Git: "git"})
	// A package may be named by the path of its file.
	assert.EqualArrays(t, pkgs, []string{"bash", "dash", "zsh"})

	t.Setenv("GIT", "/opt/git")
	spec, pkgs, err = flags{Release: "/r", Staged: true, MergeBase: true, XRelease: true}.spec([]string{"main"})
	assert.NoError(t, err)
	assert.Equal(t, spec, sides.Spec{Release: "/r", Revisions: "main", Cached: true, MergeBase: true, Unrelated: true, Upstream: sides.Upstream, Cache: clone, Git: "/opt/git"})
	assert.Len(t, pkgs, 0)

	// No revisions: none given, a package by the path of its file first, or
	// "--" first.
	for _, c := range []struct {
		f    flags
		rest []string
		pkgs []string
	}{
		{flags{}, nil, []string{}},
		{flags{}, []string{"slices/bash.yaml", "dash"}, []string{"bash", "dash"}},
		{flags{noRevs: true}, []string{"HEAD"}, []string{"HEAD"}},
	} {
		spec, pkgs, err = c.f.spec(c.rest)
		assert.NoError(t, err)
		assert.Equal(t, spec.Revisions, "")
		assert.EqualArrays(t, pkgs, c.pkgs)
	}

	spec, pkgs, err = flags{NoIndex: true}.spec([]string{"/a", "/b", "bash"})
	assert.NoError(t, err)
	assert.That(t, spec.NoIndex)
	assert.Equal(t, spec.Dirs, [2]string{"/a", "/b"})
	assert.Equal(t, spec.Revisions, "")
	assert.EqualArrays(t, pkgs, []string{"bash"})

	for want, c := range map[string]struct {
		f    flags
		rest []string
	}{
		"--no-index takes two release dir":      {flags{NoIndex: true}, []string{"/a"}},
		"--no-index takes two directories and":  {flags{NoIndex: true, Release: "/r"}, []string{"/a", "/b"}},
		"--name-status cuts nothing, so it can": {flags{NameStatus: true, FailMissing: true}, []string{"HEAD"}},
		"--tree draws the text report":          {flags{Tree: true, JSON: true}, []string{"HEAD"}},
		"--tree draws the text report, so":      {flags{Tree: true, NameStatus: true}, []string{"HEAD"}},
		"nothing to say --packages of":          {flags{Packages: "packages", NameStatus: true}, []string{"HEAD"}},
	} {
		_, _, err := c.f.spec(c.rest)
		assert.Error(t, err, want)
	}
}

// fakeChisel accepts any release, unless its chisel.yaml says "bad". A cut
// delivers every path the package's file lists, whichever the slice.
const fakeChisel = `#!/bin/sh
cmd="$1"
while [ $# -gt 1 ]; do
  case "$1" in
    --release) release="$2"; shift 2;;
    --root) root="$2"; shift 2;;
    *) shift;;
  esac
done
if grep -q bad "$release/chisel.yaml"; then echo "error: chisel.yaml: no good" >&2; exit 1; fi
[ "$cmd" = cut ] || exit 0
pkg="${1%%_*}"
echo "Fetching pool/main/x/$pkg/${pkg}_5.2-1_amd64.deb..." >&2
sed -n 's|^      \(/[^:]*\):.*|\1|p' "$release/slices/$pkg.yaml" | while read -r path; do
  mkdir -p "$root$(dirname "$path")"
  echo x > "$root$path"
done
exit 0
`

// checkout is a git checkout of a release with one commit and an edit on
// top, and a fake chisel in $CHISEL.
func checkout(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT", "")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	for _, who := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+who+"_NAME", "t")
		t.Setenv("GIT_"+who+"_EMAIL", "t@example.invalid")
	}
	bin := filepath.Join(t.TempDir(), "chisel")
	assert.NoError(t, os.WriteFile(bin, []byte(fakeChisel), 0755))
	t.Setenv("CHISEL", bin)

	dir := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(dir, path)
		assert.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		assert.NoError(t, os.WriteFile(full, []byte(content), 0644))
	}
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		assert.NoError(t, err, string(out))
	}
	write("chisel.yaml", "format: v1\n")
	write("slices/bash.yaml", "package: bash\nslices:\n  bins:\n    contents:\n      /usr/bin/bash:\n      /usr/bin/rbash:\n  config:\n    contents:\n      /etc/skel/.bashrc:\n")
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	write("slices/bash.yaml", "package: bash\nslices:\n  bins:\n    contents:\n      /usr/bin/bash:\n  scripts:\n    contents:\n      /usr/bin/bashbug:\n")
	return dir
}

func TestNameStatus(t *testing.T) {
	dir := checkout(t)
	var out bytes.Buffer
	f := flags{Release: dir, NameStatus: true}
	spec, pkgs, err := f.spec([]string{"HEAD"})
	assert.NoError(t, err)
	code, err := run(context.Background(), f, spec, pkgs, []string{"amd64"}, &out)
	assert.NoError(t, err)
	assert.Equal(t, code, 0)
	assert.Equal(t, out.String(), "bash_bins: changed\nbash_config: removed\nbash_scripts: added\n")

	// The committed revision against itself: nothing to name.
	out.Reset()
	spec, pkgs, _ = f.spec([]string{"HEAD..HEAD"})
	_, err = run(context.Background(), f, spec, pkgs, []string{"amd64"}, &out)
	assert.NoError(t, err)
	assert.Equal(t, out.String(), "")

	// Picked slices are named whatever their state.
	out.Reset()
	f.Slices = []string{"config", "bins"}
	spec, pkgs, _ = f.spec([]string{"HEAD..HEAD", filepath.Join(dir, "slices", "bash.yaml")})
	_, err = run(context.Background(), f, spec, pkgs, []string{"amd64"}, &out)
	assert.NoError(t, err)
	assert.Equal(t, out.String(), "bash_bins: same\nbash_config: same\n")

	// A name no side has is an error.
	f.Slices = []string{"scripts"}
	_, err = run(context.Background(), f, spec, pkgs, []string{"amd64"}, &out)
	assert.Error(t, err, "--slice scripts: no such slice on either side")
}

func TestRunCutsAndReports(t *testing.T) {
	dir := checkout(t)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var out bytes.Buffer
	f := flags{Release: dir, Jobs: 1}
	spec, pkgs, err := f.spec([]string{"HEAD"})
	assert.NoError(t, err)
	code, err := run(context.Background(), f, spec, pkgs, []string{"amd64"}, &out)
	assert.NoError(t, err)
	// The working tree dropped rbash from bins and the config slice whole,
	// and gained a scripts slice. Only bins is cut.
	want := "bash_bins 5.2-1 [amd64]\n-/etc/skel/.bashrc\n-/usr/bin/rbash\n+/usr/bin/bashbug\nbash_config: removed\nbash_scripts: added\n"
	assert.Equal(t, out.String(), want)
	assert.Equal(t, code, 0)

	// The same as a check.
	out.Reset()
	f.FailMissing = true
	code, err = run(context.Background(), f, spec, pkgs, []string{"amd64"}, &out)
	assert.NoError(t, err)
	assert.Equal(t, code, 1)
	assert.Equal(t, out.String(), want)

	// Nothing lost between a revision and itself.
	out.Reset()
	spec, pkgs, _ = f.spec([]string{"HEAD..HEAD", "bash"})
	code, err = run(context.Background(), f, spec, pkgs, []string{"amd64"}, &out)
	assert.NoError(t, err)
	assert.Equal(t, code, 0)
	assert.Equal(t, out.String(), "")
}

func TestAcrossReleases(t *testing.T) {
	dir := checkout(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	yaml := func(version string) {
		content := "format: v1\narchives:\n  ubuntu:\n    version: " + version + "\n"
		assert.NoError(t, os.WriteFile(filepath.Join(dir, "chisel.yaml"), []byte(content), 0644))
	}
	yaml("24.04")
	for _, args := range [][]string{{"add", "chisel.yaml"}, {"commit", "-q", "-m", "24.04"}} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		assert.NoError(t, err, string(out))
	}
	yaml("26.04")
	run := func(f flags, args ...string) (string, error) {
		var out bytes.Buffer
		spec, pkgs, err := f.spec(args)
		assert.NoError(t, err)
		_, err = run(context.Background(), f, spec, pkgs, []string{"amd64"}, &out)
		return out.String(), err
	}

	_, err := run(flags{Release: dir, NameStatus: true}, "HEAD")
	assert.Error(t, err, "the base is ubuntu-24.04 and the head ubuntu-26.04: -x / --x-release compares across releases")
	_, err = run(flags{Release: dir, NameStatus: true, XRelease: true}, "HEAD")
	assert.Error(t, err, "a comparison across releases cuts only what is named")

	// What is picked is looked for in every package, and only that.
	out, err := run(flags{Release: dir, NameStatus: true, XRelease: true, Slices: []string{"bins"}}, "HEAD")
	assert.NoError(t, err)
	assert.Equal(t, out, "bash_bins: changed\n")
	out, err = run(flags{Release: dir, NameStatus: true, XRelease: true}, "HEAD", "bash")
	assert.NoError(t, err)
	assert.Equal(t, out, "bash_bins: changed\nbash_config: removed\nbash_scripts: added\n")
}

func TestASideChiselRefusesIsAnError(t *testing.T) {
	dir := checkout(t)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "chisel.yaml"), []byte("format: bad\n"), 0644))
	f := flags{Release: dir, NameStatus: true}
	spec, pkgs, _ := f.spec([]string{"HEAD"})
	_, err := run(context.Background(), f, spec, pkgs, []string{"amd64"}, io.Discard)
	assert.Error(t, err, "the head is not a release chisel accepts: chisel.yaml: no good")
	assert.That(t, !strings.Contains(err.Error(), "chisel-coverage-nothing"), err.Error())
}

func TestMainExitCodes(t *testing.T) {
	dir := checkout(t)
	devnull, err := os.Open(os.DevNull)
	assert.NoError(t, err)
	defer devnull.Close()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = devnull, devnull
	defer func() { os.Stdout, os.Stderr = stdout, stderr }()

	assert.Equal(t, Main([]string{"version"}), 0)
	assert.Equal(t, Main([]string{"help"}), 0)
	assert.Equal(t, Main([]string{"--nope"}), 2)
	// No arguments at all is the bare form, as git diff, not the help.
	assert.Equal(t, Main([]string{"--release", dir}), 0)
	wd, err := os.Getwd()
	assert.NoError(t, err)
	assert.NoError(t, os.Chdir(dir))
	defer func() { assert.NoError(t, os.Chdir(wd)) }()
	assert.Equal(t, Main(nil), 0)
	assert.NoError(t, os.Chdir(t.TempDir()))
	assert.Equal(t, Main(nil), 2)
	assert.Equal(t, Main([]string{"--release", dir, "--arch", "vax", "HEAD"}), 2)
	assert.Equal(t, Main([]string{"--release", dir, "--jobs", "0", "HEAD"}), 2)
	assert.Equal(t, Main([]string{"--release", dir, "--name-status", "nope"}), 2)
	assert.Equal(t, Main([]string{"--release", dir, "--name-status", "HEAD"}), 0)
	assert.Equal(t, Main([]string{"--no-index", "--name-status", dir, dir}), 0)
}

func TestFileVersion(t *testing.T) {
	assert.Equal(t, fileVersion("# a comment\n\n 1.2.3 \n4.5.6\n"), "1.2.3")
	assert.Equal(t, fileVersion("# only a comment\n"), "")
	assert.That(t, strings.Count(fileVersion("# VERSION\n0.1.0\n"), ".") == 2)
}
