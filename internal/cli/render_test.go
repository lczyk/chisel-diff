package cli

import (
	"bytes"
	"io/fs"
	"testing"

	"github.com/lczyk/assert"

	"github.com/lczyk/chisel-diff/internal/changes"
	"github.com/lczyk/chisel-diff/internal/diff"
	"github.com/lczyk/chisel-diff/internal/listing"
)

func compared(arch, version string, findings ...diff.Finding) diff.Cut {
	return diff.Cut{Arch: arch, Outcome: diff.Compared, Findings: findings,
		Base: diff.Side{Status: diff.WasCut, Version: version}, Head: diff.Side{Status: diff.WasCut, Version: version}}
}

func lost(path string) diff.Finding   { return diff.Finding{Path: path, Change: diff.Lost} }
func gained(path string) diff.Finding { return diff.Finding{Path: path, Change: diff.Gained} }
func dir(f diff.Finding) diff.Finding { f.Dir = true; return f }

func sl(pkg, name string, state changes.State, cuts ...diff.Cut) diff.Slice {
	return diff.Slice{Slice: changes.Slice{Package: pkg, Name: name, State: state}, Cuts: cuts}
}

func TestRenderText(t *testing.T) {
	gone := diff.Side{Status: diff.NotAvailable, Missing: "bash"}
	res := &diff.Result{Slices: []diff.Slice{
		// The architectures agree once the triplet is normalised, and one
		// of them lost more. A directory is left out when something under
		// it is in the same state.
		sl("bash", "bins", changes.Changed,
			compared("amd64", "5.2-1", lost("/usr/lib/x86_64-linux-gnu/b.so"), dir(lost("/etc/bash")), lost("/etc/bash/rc"), dir(gained("/var/empty")), gained("/usr/bin/new")),
			compared("arm64", "5.2-1", lost("/usr/lib/aarch64-linux-gnu/b.so"), dir(lost("/etc/bash")), lost("/etc/bash/rc"), dir(gained("/var/empty")), gained("/usr/bin/new"), lost("/usr/bin/only-arm")),
			diff.Cut{Arch: "riscv64", Outcome: diff.Unavailable, Base: gone, Head: gone},
		),
		// Nothing differs: nothing printed for the architecture compared.
		sl("bash", "config", changes.Changed,
			compared("amd64", "5.2-1"),
			compared("arm64", "5.2-1"),
			diff.Cut{Arch: "riscv64", Outcome: diff.Unavailable, Base: gone, Head: diff.Side{Status: diff.Skipped, Missing: "bash"}},
		),
		sl("bash", "scripts", changes.Added),
		sl("bash", "old", changes.Removed),
		sl("ruby", "libs", changes.Changed,
			diff.Cut{Arch: "amd64", Outcome: diff.HeadNotAvailable, Base: diff.Side{Status: diff.WasCut, Version: "3.3"},
				Head: diff.Side{Status: diff.NotAvailable, Missing: "libfoo", Ignored: []string{"esm-apps", "esm-infra"}}},
			diff.Cut{Arch: "arm64", Outcome: diff.BaseNotAvailable, Base: diff.Side{Status: diff.NotAvailable, Missing: "ruby"}, Head: diff.Side{Status: diff.WasCut}},
			diff.Cut{Arch: "s390x", Outcome: diff.Error, Base: diff.Side{Status: diff.Failed}, Head: diff.Side{Status: diff.WasCut}},
			diff.Cut{Arch: "i386", Outcome: diff.Compared, Findings: []diff.Finding{gained("/x")},
				Base: diff.Side{Status: diff.WasCut, Version: "3.3"}, Head: diff.Side{Status: diff.WasCut, Version: "3.4"}},
		),
	}}
	var out bytes.Buffer
	renderText(&out, res, true, style{})
	assert.Equal(t, out.String(), `bash_bins 5.2-1 [amd64, arm64]
-/etc/bash/rc
-/usr/lib/<arch>-linux-<gnu>/b.so
+/usr/bin/new
+/var/empty/
bash_bins 5.2-1 [arm64]
-/usr/bin/only-arm
bash [riscv64]: not available
bash_scripts: added
bash_old: removed
ruby_libs 3.3 -> 3.4 [i386]
+/x
ruby_libs [amd64]: head not available: libfoo (skipped archives: esm-apps, esm-infra)
ruby_libs [arm64]: base not available
ruby_libs [s390x]: error
`)

	// A package that is missing says so once, with the archives skipped,
	// though only its lead slice was told of them first hand.
	pro := diff.Side{Status: diff.NotAvailable, Missing: "pro", Ignored: []string{"esm-apps"}}
	skipped := diff.Side{Status: diff.Skipped, Missing: "pro", Ignored: []string{"esm-apps"}}
	out.Reset()
	renderText(&out, &diff.Result{Slices: []diff.Slice{
		sl("pro", "one", changes.Changed, diff.Cut{Arch: "amd64", Outcome: diff.Unavailable, Base: pro, Head: pro}),
		sl("pro", "two", changes.Changed, diff.Cut{Arch: "amd64", Outcome: diff.Unavailable, Base: skipped, Head: skipped}),
	}}, true, style{})
	assert.Equal(t, out.String(), "pro [amd64]: not available (skipped archives: esm-apps)\n")

	// Ungrouped: a block per architecture with its real paths.
	out.Reset()
	renderText(&out, &diff.Result{Slices: res.Slices[:1]}, false, style{})
	assert.Equal(t, out.String(), `bash_bins 5.2-1 [amd64]
-/etc/bash/rc
-/usr/lib/x86_64-linux-gnu/b.so
+/usr/bin/new
+/var/empty/
bash_bins 5.2-1 [arm64]
-/etc/bash/rc
-/usr/bin/only-arm
-/usr/lib/aarch64-linux-gnu/b.so
+/usr/bin/new
+/var/empty/
bash [riscv64]: not available
`)

	// A path is counted once a slice, however many arches have it and
	// whether or not they agree: rc and b.so on both, only-arm on one.
	assert.Equal(t, summary(res, style{}), "2/3 slices differ on 3/3 arches, 3 paths gained(+), 3 paths lost(-), 0 changed(~)")
	// Coloured by whether a count is good news, the sign left plain.
	assert.Equal(t, summary(res, colours), "\x1b[33m2/3 slices differ on 3/3 arches\x1b[0m, \x1b[32m3 paths gained\x1b[0m(+), \x1b[31m3 paths lost\x1b[0m(-), \x1b[32m0 changed\x1b[0m(~)")
	// The arches any slice differs on, of those cut.
	assert.Equal(t, summary(&diff.Result{Slices: []diff.Slice{sl("x", "y", changes.Changed, compared("amd64", "1"), compared("arm64", "1", lost("/z")))}}, style{}),
		"1/1 slice differs on 1/2 arches, 0 paths gained(+), 1 path lost(-), 0 changed(~)")
	// The verb follows the noun.
	assert.Equal(t, summary(&diff.Result{Slices: res.Slices[:2]}, style{})[:22], "1/2 slices differ on 2")
	assert.Equal(t, summary(&diff.Result{Slices: res.Slices[2:3]}, colours), "\x1b[32m0/0 slices differ\x1b[0m, \x1b[33m0 paths gained\x1b[0m(+), \x1b[32m0 paths lost\x1b[0m(-), \x1b[32m0 changed\x1b[0m(~)")
}

func TestRenderChanged(t *testing.T) {
	file := func(mode fs.FileMode) listing.Entry {
		return listing.Entry{Kind: listing.File, Mode: mode}
	}
	link := func(target string) listing.Entry { return listing.Entry{Kind: listing.Symlink, Target: target} }
	changed := func(path string, dir bool, b, h listing.Entry) diff.Finding {
		return diff.Finding{Path: path, Dir: dir, Change: diff.Changed, Base: b, Head: h}
	}
	res := &diff.Result{Slices: []diff.Slice{sl("coreutils", "bins", changes.Changed,
		compared("amd64", "9.4", lost("/usr/bin/basename"), changed("/usr/bin/su", false, file(0755|fs.ModeSetuid), file(0755)),
			changed("/var/run", true, listing.Entry{Kind: listing.Dir}, link("/run")),
			changed("/usr/bin/md5sum", false, file(0755), link("../lib/x86_64-linux-gnu/md5sum"))),
		compared("arm64", "9.4", lost("/usr/bin/basename"), changed("/usr/bin/su", false, file(0755|fs.ModeSetuid), file(0755)),
			changed("/var/run", true, listing.Entry{Kind: listing.Dir}, link("/run")),
			changed("/usr/bin/md5sum", false, file(0755), link("../lib/aarch64-linux-gnu/md5sum"))),
	)}}
	var out bytes.Buffer
	renderText(&out, res, true, style{})
	// A symlink target that names the architecture folds like a path.
	assert.Equal(t, out.String(), `coreutils_bins 9.4 [amd64, arm64]
-/usr/bin/basename
~/usr/bin/md5sum: file 0755 -> symlink to ../lib/<arch>-linux-<gnu>/md5sum
~/usr/bin/su: file 4755 -> file 0755
~/var/run/: dir -> symlink to /run
`)
	assert.Equal(t, summary(res, style{}), "1/1 slice differs on 2/2 arches, 0 paths gained(+), 1 path lost(-), 3 changed(~)")

	// One architecture: its own symlink target, and paths sorted as paths.
	// A target holding the arrow does not fold with one that does not.
	out.Reset()
	renderText(&out, &diff.Result{Slices: []diff.Slice{sl("x", "bins", changes.Changed,
		compared("amd64", "1", changed("/a-foo", false, file(0644), file(0755)), changed("/a", false, file(0644), file(0755)),
			changed("/l", false, link("/usr/lib/x86_64-linux-gnu/x"), link("b -> symlink to c"))),
		compared("arm64", "1", changed("/l", false, link("/usr/lib/aarch64-linux-gnu/x -> symlink to b"), link("c"))),
	)}}, true, style{})
	assert.Equal(t, out.String(), `x_bins 1 [amd64]
~/a: file 0644 -> file 0755
~/a-foo: file 0644 -> file 0755
~/l: symlink to /usr/lib/x86_64-linux-gnu/x -> symlink to b -> symlink to c
x_bins 1 [arm64]
~/l: symlink to /usr/lib/aarch64-linux-gnu/x -> symlink to b -> symlink to c
`)

	// A pruned base opposite a head that is not there: the deletion and
	// the note.
	out.Reset()
	renderText(&out, &diff.Result{Slices: []diff.Slice{sl("apparmor", "profiles", changes.Changed, diff.Cut{
		Arch: "amd64", Outcome: diff.HeadNotAvailable, Findings: []diff.Finding{{Path: "/etc/apparmor.d/busybox", Change: diff.NoContent}},
		Base: diff.Side{Status: diff.WasCut, Version: "4.0", Pruned: []string{"/etc/apparmor.d/busybox"}}, Head: diff.Side{Status: diff.NotAvailable, Missing: "apparmor"},
	})}}, true, style{})
	assert.Equal(t, out.String(), "apparmor_profiles 4.0 [amd64]\n!/etc/apparmor.d/busybox: no content\napparmor_profiles [amd64]: head not available\n")

	out.Reset()
	assert.NoError(t, renderJSON(&out, jsonRun{}, &changes.Result{}, res.Slices[:1], true))
	assert.ContainsString(t, out.String(), `{"path":"/usr/bin/su","change":"changed","arches":["amd64","arm64"],"base":{"kind":"file","mode":"4755"},"head":{"kind":"file","mode":"0755"}}`)
	assert.ContainsString(t, out.String(), `{"path":"/var/run/","change":"changed","arches":["amd64","arm64"],"base":{"kind":"dir"},"head":{"kind":"symlink","target":"/run"}}`)
}

func TestRenderColoured(t *testing.T) {
	res := &diff.Result{Slices: []diff.Slice{sl("bash", "bins", changes.Changed,
		compared("amd64", "5.2", lost("/a"), gained("/b"), diff.Finding{Path: "/c", Change: diff.NoContent}))}}
	var out bytes.Buffer
	renderText(&out, res, true, colours)
	assert.Equal(t, out.String(), "\x1b[1;34mbash_bins\x1b[0m 5.2 [amd64]\n\x1b[31m-/a\x1b[0m\n\x1b[32m+/b\x1b[0m\n\x1b[33m!/c: no content\x1b[0m\n")
	assert.Equal(t, summary(res, style{}), "1/1 slice differs, 1 path gained(+), 1 path lost(-), 0 changed(~), 1 no content(!)")
	// Not a terminal: no colour.
	assert.Equal(t, styleFor(&out), style{})
}

func TestRenderJSON(t *testing.T) {
	ref := "main"
	run := jsonRun{Chisel: "v1.2.0", Release: ".", Revisions: "main", Base: jsonSide{Kind: "rev", Ref: &ref, Tip: &ref, Rev: &ref}, Head: jsonSide{Kind: "worktree"}}
	plan := &changes.Result{Packages: []changes.Package{{Name: "bash", File: "modified"}}}
	slices := []diff.Slice{
		sl("bash", "bins", changes.Changed,
			compared("amd64", "5.2-1", lost("/usr/lib/x86_64-linux-gnu/b.so"), dir(lost("/etc/bash")), lost("/etc/bash/rc")),
			compared("arm64", "5.2-1", lost("/usr/lib/aarch64-linux-gnu/b.so"), dir(lost("/etc/bash")), lost("/etc/bash/rc")),
			diff.Cut{Arch: "riscv64", Outcome: diff.HeadNotAvailable, Base: diff.Side{Status: diff.WasCut, Entries: 3, Version: "5.2-1"},
				Head: diff.Side{Status: diff.NotAvailable, Missing: "libfoo", Ignored: []string{"esm-apps"}}},
		),
		sl("bash", "scripts", changes.Added),
		// Cut for prefer, in a package that was not considered.
		sl("s", "bins", changes.Same, compared("amd64", "1.0")),
	}
	var out bytes.Buffer
	assert.NoError(t, renderJSON(&out, run, plan, slices, true))
	assert.Equal(t, out.String(), `{"run":{"chisel":"v1.2.0","release":".","chisel-yaml-differs":false,"revisions":"main","merge-base":false,`+
		`"base":{"kind":"rev","ref":"main","tip":"main","rev":"main"},"head":{"kind":"worktree","ref":null,"tip":null,"rev":null}},`+
		`"packages":[{"name":"bash","file":"modified","slices":[{"name":"bins","state":"changed","forced":false,"cuts":[`+
		`{"arch":"amd64","outcome":"compared","base":{"status":"cut","entries":0,"version":"5.2-1","missing":null,"pruned":[],"skipped-archives":[],"error":null},"head":{"status":"cut","entries":0,"version":"5.2-1","missing":null,"pruned":[],"skipped-archives":[],"error":null}},`+
		`{"arch":"arm64","outcome":"compared","base":{"status":"cut","entries":0,"version":"5.2-1","missing":null,"pruned":[],"skipped-archives":[],"error":null},"head":{"status":"cut","entries":0,"version":"5.2-1","missing":null,"pruned":[],"skipped-archives":[],"error":null}},`+
		`{"arch":"riscv64","outcome":"head not available","base":{"status":"cut","entries":3,"version":"5.2-1","missing":null,"pruned":[],"skipped-archives":[],"error":null},"head":{"status":"not-available","entries":0,"version":null,"missing":"libfoo","pruned":[],"skipped-archives":["esm-apps"],"error":null}}],`+
		// Every entry is here, the directory too, and a path the
		// architectures spell differently has each one's own.
		`"findings":[{"path":"/etc/bash/","change":"lost","arches":["amd64","arm64"]},{"path":"/etc/bash/rc","change":"lost","arches":["amd64","arm64"]},`+
		`{"path":"/usr/lib/<arch>-linux-<gnu>/b.so","change":"lost","arches":["amd64","arm64"],"raw":{"amd64":"/usr/lib/x86_64-linux-gnu/b.so","arm64":"/usr/lib/aarch64-linux-gnu/b.so"}}]},`+
		`{"name":"scripts","state":"added","forced":false,"cuts":[],"findings":[]}]},`+
		`{"name":"s","file":"same","slices":[{"name":"bins","state":"same","forced":true,"cuts":[{"arch":"amd64","outcome":"compared","base":{"status":"cut","entries":0,"version":"1.0","missing":null,"pruned":[],"skipped-archives":[],"error":null},"head":{"status":"cut","entries":0,"version":"1.0","missing":null,"pruned":[],"skipped-archives":[],"error":null}}],"findings":[]}]}]}`+"\n")
}

func TestRenderPair(t *testing.T) {
	s := sl("jdk", "awt", changes.Changed, compared("amd64", "17.0", lost("/b"), gained("/c")))
	s.HeadName = "awt-core"
	other := sl("old", "awt", changes.Changed, compared("amd64", "1"))
	other.HeadPackage, other.HeadName = "jdk", "awt-core"
	var out bytes.Buffer
	renderText(&out, &diff.Result{Slices: []diff.Slice{s, other}}, true, style{})
	assert.Equal(t, out.String(), "jdk_awt:awt-core 17.0 [amd64]\n-/b\n+/c\n")

	// Not available on both sides: told under the pair's name, not as its
	// package's absence.
	gone := sl("old", "a", changes.Changed, diff.Cut{Arch: "amd64", Outcome: diff.Unavailable,
		Base: diff.Side{Status: diff.NotAvailable, Missing: "old"}, Head: diff.Side{Status: diff.NotAvailable, Missing: "old"}})
	gone.HeadPackage, gone.HeadName = "new", "b"
	out.Reset()
	renderText(&out, &diff.Result{Slices: []diff.Slice{gone}}, true, style{})
	assert.Equal(t, out.String(), "old_a:new_b [amd64]: not available\n")

	out.Reset()
	assert.NoError(t, renderJSON(&out, jsonRun{}, &changes.Result{}, []diff.Slice{s}, true))
	assert.ContainsString(t, out.String(), `"name":"awt","state":"changed","forced":true,"head-package":"jdk","head-slice":"awt-core"`)
}
