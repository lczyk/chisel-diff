package diff

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lczyk/assert"
	"github.com/lczyk/chisel-coverage/pkg/cut"

	"github.com/lczyk/chisel-diff/internal/changes"
)

// fakeChisel cuts from what the release dir itself holds under fake/: the
// trees fake/tree/<slice>/all and fake/tree/<slice>/<arch> are copied into
// --root. A package with fake/na/<arch>/<package> is in no archive, a slice
// with fake/nocontent/<slice> names that path as one with no content for as
// long as the package's file lists it (fake/nocontent-always/<slice>
// whatever it lists), a slice
// with fake/na-dep/<slice> reaches a package that is in none (the file
// names it), a slice with fake/fail/<slice> fails. fake/version/<package>
// is the version of the deb fetched, 1.0 without. Every cut appends
// "<arch> <slice> <release dir's name>" to $FAKE_LOG.
const fakeChisel = `#!/bin/sh
while [ $# -gt 1 ]; do
  case "$1" in
    --release) release="$2"; shift 2;;
    --root) root="$2"; shift 2;;
    --arch) arch="$2"; shift 2;;
    *) shift;;
  esac
done
slice="$1"
pkg="${slice%%_*}"
printf '%s %s %s\n' "$arch" "$slice" "$(basename "$release")" >> "$FAKE_LOG"
if [ -f "$release/fake/na/$arch/$pkg" ]; then
  if [ -s "$release/fake/na/$arch/$pkg" ]; then
    echo "Archive \"$(cat "$release/fake/na/$arch/$pkg")\" ignored: credentials not found" >&2
  fi
  echo "error: cannot find package \"$pkg\" in archive(s)" >&2; exit 1
fi
if [ -f "$release/fake/na-dep/$slice" ]; then
  echo "Archive \"esm-apps\" ignored: credentials not found" >&2
  echo "error: cannot find package \"$(cat "$release/fake/na-dep/$slice")\" in archive(s)" >&2; exit 1
fi
if [ -f "$release/fake/fail/$slice" ]; then echo "boom" >&2; exit 1; fi
if [ -f "$release/fake/nocontent-always/$slice" ]; then
  echo "error: cannot extract from package \"$pkg\": no content at $(cat "$release/fake/nocontent-always/$slice")" >&2; exit 1
fi
if [ -f "$release/fake/nocontent/$slice" ] && grep -q "^ *$(cat "$release/fake/nocontent/$slice"):" "$release/slices/$pkg.yaml"; then
  echo "error: cannot extract from package \"$pkg\": no content at $(cat "$release/fake/nocontent/$slice")" >&2; exit 1
fi
v=1.0
[ -f "$release/fake/version/$pkg" ] && v="$(cat "$release/fake/version/$pkg")"
echo "Fetching pool/main/x/$pkg/${pkg}_${v}_$arch.deb..." >&2
for t in "$release/fake/tree/$slice/all" "$release/fake/tree/$slice/$arch"; do
  if [ -d "$t" ]; then cp -R "$t/." "$root/"; fi
done
exit 0
`

type fixture struct {
	t          *testing.T
	base, head string
	log        string
	runner     *cut.Runner
}

func setup(t *testing.T) *fixture {
	t.Helper()
	tmp := t.TempDir()
	f := &fixture{t: t, base: filepath.Join(tmp, "base"), head: filepath.Join(tmp, "head"), log: filepath.Join(tmp, "log")}
	bin := filepath.Join(tmp, "chisel")
	assert.NoError(t, os.WriteFile(bin, []byte(fakeChisel), 0755))
	f.runner = &cut.Runner{Bin: bin}
	work := filepath.Join(tmp, "tmpdir")
	assert.NoError(t, os.MkdirAll(work, 0755))
	t.Setenv("TMPDIR", work)
	t.Setenv("FAKE_LOG", f.log)
	for _, dir := range []string{f.base, f.head} {
		f.write(dir, "chisel.yaml", "format: v1\n")
	}
	return f
}

// write puts a file into a release dir. A path ending in a slash is an
// empty directory.
func (f *fixture) write(release, path, content string) {
	f.t.Helper()
	full := filepath.Join(release, path)
	if strings.HasSuffix(path, "/") {
		assert.NoError(f.t, os.MkdirAll(full, 0755))
		return
	}
	assert.NoError(f.t, os.MkdirAll(filepath.Dir(full), 0755))
	assert.NoError(f.t, os.WriteFile(full, []byte(content), 0644))
}

// both writes the same file into both releases.
func (f *fixture) both(path, content string) {
	f.write(f.base, path, content)
	f.write(f.head, path, content)
}

// pkg writes a package slices file with the slices named, each listing one
// path that says which side it is on, so every slice is changed.
func (f *fixture) pkg(release, name string, slices ...string) {
	var b strings.Builder
	b.WriteString("package: " + name + "\nslices:\n")
	for _, s := range slices {
		b.WriteString("  " + s + ":\n    contents:\n      /" + filepath.Base(release) + ":\n")
	}
	f.write(release, "slices/"+name+".yaml", b.String())
}

func (f *fixture) run(arches []string, named, picks []string, opts Options) *Result {
	f.t.Helper()
	b, err := changes.Load(f.base)
	assert.NoError(f.t, err)
	h, err := changes.Load(f.head)
	assert.NoError(f.t, err)
	plan, err := changes.Compare(b, h, named, picks)
	assert.NoError(f.t, err)
	opts.Base, opts.Head, opts.Arches, opts.Runner = f.base, f.head, arches, f.runner
	res, err := Run(context.Background(), opts, plan, plan.ArchiveDiffers)
	assert.NoError(f.t, err)
	return res
}

func (f *fixture) cuts() []string {
	b, err := os.ReadFile(f.log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// findings renders a cut's findings as "-/lost +/gained/", a directory with
// its slash.
func findings(c Cut) string {
	var out []string
	for _, fd := range c.Findings {
		sign := map[Change]string{Lost: "-", Gained: "+", NoContent: "!"}[fd.Change]
		path := fd.Path
		if fd.Dir {
			path += "/"
		}
		out = append(out, sign+path)
	}
	return strings.Join(out, " ")
}

func slice(t *testing.T, res *Result, id string) Slice {
	t.Helper()
	for _, s := range res.Slices {
		if s.ID() == id {
			return s
		}
	}
	t.Fatalf("no slice %s in the result", id)
	return Slice{}
}

func TestLostAndGained(t *testing.T) {
	f := setup(t)
	f.pkg(f.base, "bash", "bins")
	f.pkg(f.head, "bash", "bins")
	f.write(f.base, "fake/tree/bash_bins/all/usr/bin/bash", "x")
	f.write(f.base, "fake/tree/bash_bins/all/usr/bin/rbash", "x")
	f.write(f.base, "fake/tree/bash_bins/all/etc/bash.d/", "")
	f.write(f.base, "fake/tree/bash_bins/arm64/usr/lib/aarch64/b.so", "x")
	f.write(f.head, "fake/tree/bash_bins/all/usr/bin/bash", "x")
	f.write(f.head, "fake/tree/bash_bins/all/usr/libexec/bash/", "")
	// A file that became a directory is on both sides: neither lost nor gained.
	f.write(f.base, "fake/tree/bash_bins/all/usr/thing", "x")
	f.write(f.head, "fake/tree/bash_bins/all/usr/thing/inner", "x")

	res := f.run([]string{"amd64", "arm64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, res.Errors, 0)
	assert.Len(t, res.Slices, 1)
	s := res.Slices[0]
	assert.Equal(t, s.ID(), "bash_bins")
	amd64, arm64 := s.Cuts[0], s.Cuts[1]
	assert.Equal(t, amd64.Outcome, Compared)
	assert.Equal(t, amd64.Base.Status, WasCut)
	assert.Equal(t, amd64.Base.Version, "1.0")
	assert.Equal(t, amd64.Base.Entries, 7)
	assert.Equal(t, amd64.Head.Entries, 7)
	assert.Equal(t, findings(amd64), "-/etc/ -/etc/bash.d/ -/usr/bin/rbash +/usr/libexec/ +/usr/libexec/bash/ +/usr/thing/inner")
	assert.Equal(t, arm64.Arch, "arm64")
	assert.Equal(t, findings(arm64), "-/etc/ -/etc/bash.d/ -/usr/bin/rbash -/usr/lib/ -/usr/lib/aarch64/ -/usr/lib/aarch64/b.so +/usr/libexec/ +/usr/libexec/bash/ +/usr/thing/inner")
	assert.That(t, amd64.Findings[0].Dir && !amd64.Findings[2].Dir)
	assert.That(t, res.Missing())

	// The lead architecture is cut first and alone, both of its sides.
	cuts := f.cuts()
	assert.Len(t, cuts, 4)
	for _, c := range cuts[:2] {
		assert.That(t, strings.HasPrefix(c, "amd64 bash_bins "), c)
	}
}

func TestNothingLostIsNotMissing(t *testing.T) {
	f := setup(t)
	f.pkg(f.base, "bash", "bins")
	f.pkg(f.head, "bash", "bins", "extra")
	f.write(f.base, "fake/tree/bash_bins/all/usr/bin/bash", "x")
	f.write(f.head, "fake/tree/bash_bins/all/usr/bin/bash", "x")
	f.write(f.head, "fake/tree/bash_bins/all/usr/bin/more", "x")
	res := f.run([]string{"amd64"}, nil, nil, Options{OneArchive: true})
	assert.Equal(t, findings(slice(t, res, "bash_bins").Cuts[0]), "+/usr/bin/more")
	// The added slice is named and not cut.
	added := slice(t, res, "bash_extra")
	assert.Equal(t, added.State, changes.Added)
	assert.Len(t, added.Cuts, 0)
	assert.That(t, !res.Missing())
	assert.Len(t, f.cuts(), 2)
}

func TestOutcomes(t *testing.T) {
	f := setup(t)
	// gone: on no side on riscv64. came: new on riscv64. went: gone from
	// riscv64 at the head. dep: its essential is gone at the head.
	for _, name := range []string{"gone", "came", "went"} {
		f.pkg(f.base, name, "one", "two")
		f.pkg(f.head, name, "one", "two")
	}
	f.pkg(f.base, "dep", "one")
	f.pkg(f.head, "dep", "one")
	f.both("fake/na/riscv64/gone", "")
	f.write(f.base, "fake/na/riscv64/came", "")
	f.write(f.head, "fake/na/riscv64/went", "")
	f.write(f.head, "fake/na-dep/dep_one", "libfoo")
	for _, s := range []string{"gone_one", "came_one", "went_one", "dep_one"} {
		f.both("fake/tree/"+s+"/all/x", "x")
	}

	res := f.run([]string{"amd64", "riscv64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, res.Errors, 0)
	for id, want := range map[string]Outcome{
		"gone_one": Unavailable, "gone_two": Unavailable,
		"came_one": BaseNotAvailable, "came_two": BaseNotAvailable,
		"went_one": HeadNotAvailable, "went_two": HeadNotAvailable,
	} {
		s := slice(t, res, id)
		assert.Equal(t, s.Cuts[0].Outcome, Compared, id)
		assert.Equal(t, s.Cuts[1].Outcome, want, id)
		assert.Len(t, s.Cuts[1].Findings, 0)
	}
	// The lead of a package found it missing: the package's other slices
	// were not cut on that side, and say so.
	went := slice(t, res, "went_two").Cuts[1]
	assert.Equal(t, went.Base.Status, WasCut)
	assert.Equal(t, went.Head.Status, Skipped)
	assert.Equal(t, went.Head.Missing, "went")
	assert.Equal(t, slice(t, res, "went_one").Cuts[1].Head.Status, NotAvailable)
	log := strings.Join(f.cuts(), "\n")
	assert.That(t, !strings.Contains(log, "riscv64 went_two head"))
	assert.That(t, strings.Contains(log, "riscv64 went_two base"))
	assert.That(t, !strings.Contains(log, "riscv64 gone_two"))

	// A package the slice reaches is what is missing, on every arch, and
	// the archives chisel skipped are told.
	dep := slice(t, res, "dep_one").Cuts[0]
	assert.Equal(t, dep.Outcome, HeadNotAvailable)
	assert.Equal(t, dep.Head.Status, NotAvailable)
	assert.Equal(t, dep.Head.Missing, "libfoo")
	assert.EqualArrays(t, dep.Head.Ignored, []string{"esm-apps"})
	assert.That(t, res.Missing())
}

func TestSkippedSlicesSayWhatTheirLeadFound(t *testing.T) {
	f := setup(t)
	f.pkg(f.base, "pro", "one", "two")
	f.pkg(f.head, "pro", "one", "two")
	// In no archive chisel could read: one was skipped for want of
	// credentials.
	f.both("fake/na/amd64/pro", "esm-apps")
	res := f.run([]string{"amd64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, res.Errors, 0)
	for _, id := range []string{"pro_one", "pro_two"} {
		c := slice(t, res, id).Cuts[0]
		assert.Equal(t, c.Outcome, Unavailable, id)
		for _, side := range []Side{c.Base, c.Head} {
			assert.Equal(t, side.Missing, "pro", id)
			assert.EqualArrays(t, side.Ignored, []string{"esm-apps"})
		}
	}
	assert.Equal(t, slice(t, res, "pro_two").Cuts[0].Head.Status, Skipped)
	assert.That(t, !res.Missing())
}

func TestAFailedCutIsAnError(t *testing.T) {
	f := setup(t)
	f.pkg(f.base, "bash", "bins", "config")
	f.pkg(f.head, "bash", "bins", "config")
	f.write(f.base, "fake/fail/bash_bins", "")
	f.both("fake/tree/bash_config/all/etc/x", "x")
	res := f.run([]string{"amd64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, res.Errors, 1)
	assert.Error(t, res.Errors[0], "bash_bins: exit status 1\n  boom")
	bins := slice(t, res, "bash_bins").Cuts[0]
	assert.Equal(t, bins.Outcome, Error)
	assert.Equal(t, bins.Base.Status, Failed)
	assert.Equal(t, bins.Head.Status, WasCut)
	assert.Len(t, bins.Findings, 0)
	// The package's other slices are still compared.
	assert.Equal(t, slice(t, res, "bash_config").Cuts[0].Outcome, Compared)
	assert.That(t, !res.Missing())
}

func TestABaseThatCannotBePrunedIsAnError(t *testing.T) {
	f := setup(t)
	f.pkg(f.base, "bash", "bins")
	f.pkg(f.head, "bash", "bins")
	// Chisel names a path no entry has as its source.
	f.write(f.base, "fake/nocontent-always/bash_bins", "/elsewhere")
	res := f.run([]string{"amd64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, res.Errors, 1)
	assert.Error(t, res.Errors[0], "no entry of bash has that as its source")
	assert.Equal(t, res.Slices[0].Cuts[0].Outcome, Error)

	// One it can prune: the base is cut again from a release of its own,
	// and the entry deleted is neither lost nor gained.
	assert.NoError(t, os.Remove(filepath.Join(f.base, "fake", "nocontent-always", "bash_bins")))
	f.write(f.base, "fake/nocontent/bash_bins", "/base")
	res = f.run([]string{"amd64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, res.Errors, 0)
	c := res.Slices[0].Cuts[0]
	assert.Equal(t, c.Outcome, Compared)
	assert.EqualArrays(t, c.Base.Pruned, []string{"/base"})
	assert.Equal(t, findings(c), "!/base")
	assert.That(t, !res.Missing())
}

func TestPickedSlices(t *testing.T) {
	f := setup(t)
	f.pkg(f.base, "bash", "bins", "old")
	f.pkg(f.head, "bash", "bins", "new")
	f.write(f.base, "fake/tree/bash_old/all/usr/old", "x")
	f.write(f.head, "fake/tree/bash_new/all/usr/new", "x")
	// Not picked: named, not cut, and a removed slice is itself missing.
	res := f.run([]string{"amd64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, slice(t, res, "bash_old").Cuts, 0)
	assert.Len(t, slice(t, res, "bash_new").Cuts, 0)
	assert.That(t, res.Missing())

	// Picked: cut on the side that has it, every entry lost or gained.
	res = f.run([]string{"amd64"}, nil, []string{"old", "new"}, Options{OneArchive: true})
	assert.Len(t, res.Slices, 2)
	old := slice(t, res, "bash_old").Cuts[0]
	assert.Equal(t, old.Outcome, Listed)
	assert.Equal(t, old.Head.Status, None)
	assert.Equal(t, findings(old), "-/usr/ -/usr/old")
	assert.Equal(t, findings(slice(t, res, "bash_new").Cuts[0]), "+/usr/ +/usr/new")
}

func TestPreferSlicesAreCut(t *testing.T) {
	f := setup(t)
	f.both("slices/s.yaml", "package: s\nslices:\n  bins:\n    contents:\n      /p: {text: x, prefer: v}\n")
	f.both("slices/v.yaml", "package: v\nslices:\n  temp:\n    contents:\n      /p: {text: x, until: mutate}\n")
	f.pkg(f.base, "u", "config")
	f.pkg(f.head, "u", "config")
	f.write(f.base, "fake/tree/s_bins/all/p", "x")
	res := f.run([]string{"amd64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, res.Slices, 2)
	bins := slice(t, res, "s_bins")
	assert.That(t, bins.Prefer)
	assert.Equal(t, bins.State, changes.Same)
	assert.Equal(t, findings(bins.Cuts[0]), "-/p")
	assert.That(t, res.Missing())
}

func TestArchiveThatMoved(t *testing.T) {
	f := setup(t)
	f.pkg(f.base, "bash", "bins")
	f.pkg(f.head, "bash", "bins")
	f.write(f.head, "fake/version/bash", "1.1")
	res := f.run([]string{"amd64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, res.Errors, 1)
	assert.Error(t, res.Errors[0], "the archive moved during the run: bash on amd64 was fetched as 1.0 for the base and 1.1 for the head")
	// The cuts are compared all the same, for the report.
	assert.Equal(t, res.Slices[0].Cuts[0].Outcome, Compared)

	// Another archive is the point when chisel.yaml differs, or when the
	// package's own file names another one.
	res = f.run([]string{"amd64"}, nil, nil, Options{})
	assert.Len(t, res.Errors, 0)
	f.write(f.head, "slices/bash.yaml", "package: bash\narchive: fips\nslices:\n  bins:\n    contents:\n      /head:\n")
	res = f.run([]string{"amd64"}, nil, nil, Options{OneArchive: true})
	assert.Len(t, res.Errors, 0)
}
