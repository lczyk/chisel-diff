package cli

import (
	"bytes"
	"testing"

	"github.com/lczyk/assert"

	"github.com/lczyk/chisel-diff/internal/changes"
	"github.com/lczyk/chisel-diff/internal/diff"
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
	renderText(&out, res, true)
	assert.Equal(t, out.String(), `bash 5.2-1 bins [amd64, arm64]
-/etc/bash/rc
-/usr/lib/<arch>-linux-<gnu>/b.so
+/usr/bin/new
+/var/empty/
bash 5.2-1 bins [arm64]
-/usr/bin/only-arm
bash [riscv64]: not available
bash scripts: added
bash old: removed
ruby 3.3 -> 3.4 libs [i386]
+/x
ruby libs [amd64]: head not available: libfoo (skipped archives: esm-apps, esm-infra)
ruby libs [arm64]: base not available
ruby libs [s390x]: error
`)

	// Ungrouped: a block per architecture with its real paths.
	out.Reset()
	renderText(&out, &diff.Result{Slices: res.Slices[:1]}, false)
	assert.Equal(t, out.String(), `bash 5.2-1 bins [amd64]
-/etc/bash/rc
-/usr/lib/x86_64-linux-gnu/b.so
+/usr/bin/new
+/var/empty/
bash 5.2-1 bins [arm64]
-/etc/bash/rc
-/usr/bin/only-arm
-/usr/lib/aarch64-linux-gnu/b.so
+/usr/bin/new
+/var/empty/
bash [riscv64]: not available
`)

	assert.Equal(t, summary(res), "3 slices compared, 2 differ, 7 lost, 5 gained")
}
