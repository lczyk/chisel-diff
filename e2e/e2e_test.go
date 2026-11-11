//go:build e2e

// Package e2e runs the tool for real: a real chisel and a real
// chisel-releases clone with an edit on top, since old commits stop cutting
// as the archive moves. Gated behind the e2e build tag and CHISEL_DIFF_E2E
// because it needs the chisel binary and network access.
package e2e

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lczyk/chisel-diff/internal/cli"
)

const releaseBranch = "ubuntu-24.04"

func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("CHISEL_DIFF_E2E") != "1" {
		t.Skip("set CHISEL_DIFF_E2E=1 to run (needs chisel + network)")
	}
	chisel := os.Getenv("CHISEL")
	if chisel == "" {
		chisel = "chisel"
	}
	if _, err := exec.LookPath(chisel); err != nil {
		t.Skipf("chisel binary not found (%v); set CHISEL=/path/to/chisel", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
}

// cloneRelease shallow-clones a real chisel-releases checkout under dir.
func cloneRelease(t *testing.T, dir string) string {
	t.Helper()
	release := filepath.Join(dir, "releases")
	clone := exec.Command("git", "clone", "--quiet", "--depth", "1", "-b", releaseBranch,
		"https://github.com/canonical/chisel-releases", release)
	clone.Stdout, clone.Stderr = os.Stderr, os.Stderr
	if err := clone.Run(); err != nil {
		t.Fatalf("clone chisel-releases: %v", err)
	}
	return release
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// it wrote.
func captureStdout(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	code := fn()
	os.Stdout = orig
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return code, string(out)
}

// TestNameStatus drops a path from a real slice in the working tree and has
// the real chisel accept both sides.
func TestNameStatus(t *testing.T) {
	requireE2E(t)
	release := cloneRelease(t, t.TempDir())
	path := filepath.Join(release, "slices", "dash.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), "      /usr/bin/sh:\n", "", 1)
	if edited == string(raw) {
		t.Fatalf("dash.yaml no longer lists /usr/bin/sh:\n%s", raw)
	}
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}
	code, out := captureStdout(t, func() int {
		return cli.Main([]string{"--release", release, "--name-status", "HEAD"})
	})
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if out != "dash bins: changed\n" {
		t.Fatalf("unexpected report:\n%s", out)
	}
}
