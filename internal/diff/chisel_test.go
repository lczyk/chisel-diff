package diff

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lczyk/assert"
	"github.com/lczyk/chisel-coverage/pkg/cut"
	"github.com/lczyk/chisel-proxy/pkg/apt"
	"github.com/lczyk/chisel-proxy/pkg/deb"
)

// The debs of the archive the real chisel cuts from: package -> path ->
// content.
var debs = map[string]map[string]string{
	"demo": {
		"usr/bin/demo":                 "demo",
		"usr/bin/tool":                 "tool",
		"usr/lib/demo/plugin.so":       "plugin",
		"usr/share/doc/demo/copyright": "copyright",
		"etc/demo/conf":                "conf",
	},
	"libdemo": {"usr/lib/libdemo.so.1": "lib"},
	"cdx-s":   {"cdx/s-file": "s"},
	"cdx-u":   {"cdx/u-file": "u"},
	"cdx-v":   {"cdx/v-file": "v"},
}

// realChisel is a fixture whose releases the real chisel cuts, against an
// in-memory signed archive reached through http_proxy and with a throwaway
// cache. Skipped without a chisel binary.
func realChisel(t *testing.T) (*fixture, string) {
	t.Helper()
	bin := cut.Binary()
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("chisel binary not found (%v); set CHISEL=/path/to/chisel", err)
	}
	arch, err := deb.InferArch()
	assert.NoError(t, err)
	tmp := t.TempDir()
	var pkgs []*deb.Package
	for name, files := range debs {
		dir := filepath.Join(tmp, "debs", name)
		for path, content := range files {
			assert.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0755))
			assert.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(content), 0644))
		}
		pkg, err := deb.FromDir(dir, deb.Defaults{Arch: arch, Version: "1.0-1"})
		assert.NoError(t, err)
		pkgs = append(pkgs, pkg)
	}
	archive, err := apt.New("diff", arch, pkgs)
	assert.NoError(t, err)
	// chisel only knows archive.ubuntu.com and ports.ubuntu.com, and reaches
	// them through http_proxy; the proxy answers for the suite from memory.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ctype, ok := archive.Serve(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write(body)
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	work := filepath.Join(tmp, "tmpdir")
	assert.NoError(t, os.MkdirAll(work, 0755))
	t.Setenv("TMPDIR", work)

	armor, err := archive.PublicKey()
	assert.NoError(t, err)
	var y strings.Builder
	fmt.Fprintf(&y, "format: v3\n\nmaintenance:\n  standard: 2024-04-25\n  expanded: 2029-05-31\n  legacy: 2034-04-25\n  end-of-life: 2036-04-29\n\narchives:\n  diff:\n    version: 24.04\n    components: [diff]\n    suites: [diff]\n    priority: 10\n    public-keys: [diff-key]\n\n")
	fmt.Fprintf(&y, "public-keys:\n  diff-key:\n    id: %q\n    armor: |\n", archive.KeyID())
	for _, line := range strings.Split(strings.TrimRight(armor, "\n"), "\n") {
		fmt.Fprintf(&y, "      %s\n", line)
	}
	f := &fixture{t: t, base: filepath.Join(tmp, "base"), head: filepath.Join(tmp, "head"), log: filepath.Join(tmp, "log"), runner: &cut.Runner{Bin: bin}}
	for _, dir := range []string{f.base, f.head} {
		f.write(dir, "chisel.yaml", y.String())
	}
	return f, arch
}

// sides writes one package's file on both sides, the two given as the body
// under "slices:". An empty body leaves the package out of that side.
func (f *fixture) sides(pkg, baseSlices, headSlices string) {
	for dir, body := range map[string]string{f.base: baseSlices, f.head: headSlices} {
		if body != "" {
			f.write(dir, "slices/"+pkg+".yaml", "package: "+pkg+"\n"+strings.ReplaceAll(body, "\t", "  "))
		}
	}
}

// reset empties both sides of package files.
func (f *fixture) reset() {
	for _, dir := range []string{f.base, f.head} {
		assert.NoError(f.t, os.RemoveAll(filepath.Join(dir, "slices")))
		assert.NoError(f.t, os.MkdirAll(filepath.Join(dir, "slices"), 0755))
	}
}

// The slices the cases start from.
const (
	demoBins = "slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n\t\t\t/usr/bin/tool:\n"
	libs     = "slices:\n\tlibs:\n\t\tcontents:\n\t\t\t/usr/lib/libdemo.so.1:\n"
)

// TestRealChisel pins what the design assumes of chisel, one case each.
func TestRealChisel(t *testing.T) {
	f, arch := realChisel(t)
	// compare runs both sides as they are and returns the findings of the
	// slice asked about, with the errors of the run.
	compare := func(id string) (string, *Result) {
		t.Helper()
		res := f.run([]string{arch}, nil, nil, Options{OneArchive: true})
		for _, s := range res.Slices {
			if s.ID() == id && len(s.Cuts) > 0 {
				return findings(s.Cuts[0]), res
			}
		}
		return "<" + id + " was not cut>", res
	}
	clean := func(res *Result) {
		t.Helper()
		for _, err := range res.Errors {
			t.Errorf("unexpected error: %v", err)
		}
	}

	t.Run("a path moved behind an essential is not lost", func(t *testing.T) {
		f.reset()
		f.sides("demo", demoBins, "slices:\n\tbins:\n\t\tessential:\n\t\t\tdemo_tool:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n\ttool:\n\t\tcontents:\n\t\t\t/usr/bin/tool:\n")
		got, res := compare("demo_bins")
		clean(res)
		assert.Equal(t, got, "")
		assert.That(t, !res.Missing())
	})

	t.Run("a dropped path is lost", func(t *testing.T) {
		f.reset()
		f.sides("demo", demoBins, "slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n")
		got, res := compare("demo_bins")
		clean(res)
		assert.Equal(t, got, "-/usr/bin/tool")
		assert.That(t, res.Missing())
	})

	t.Run("a glob for explicit paths loses nothing", func(t *testing.T) {
		f.reset()
		f.sides("demo", demoBins, "slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/*:\n")
		got, res := compare("demo_bins")
		clean(res)
		assert.Equal(t, got, "")
	})

	t.Run("a dropped essential takes what it brought", func(t *testing.T) {
		f.reset()
		f.sides("libdemo", libs, libs)
		f.sides("demo", "slices:\n\tbins:\n\t\tessential:\n\t\t\tlibdemo_libs:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n", "slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n")
		got, res := compare("demo_bins")
		clean(res)
		assert.Equal(t, got, "-/usr/lib/ -/usr/lib/libdemo.so.1")
	})

	t.Run("a package-level essential changes every slice", func(t *testing.T) {
		f.reset()
		body := "slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n\tcopyright:\n\t\tcontents:\n\t\t\t/usr/share/doc/demo/copyright:\n"
		f.sides("demo", "essential:\n\tdemo_copyright:\n"+body, body)
		got, res := compare("demo_bins")
		clean(res)
		assert.Equal(t, got, "-/usr/share/ -/usr/share/doc/ -/usr/share/doc/demo/ -/usr/share/doc/demo/copyright")
		// The slice the essential named changed too, and lost nothing.
		got, _ = compare("demo_copyright")
		assert.Equal(t, got, "")
	})

	t.Run("a directory the slice made is an entry", func(t *testing.T) {
		f.reset()
		f.sides("demo", "slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n\t\t\t/var/lib/demo/: {make: true}\n", "slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n")
		got, res := compare("demo_bins")
		clean(res)
		assert.Equal(t, got, "-/var/ -/var/lib/ -/var/lib/demo/")
	})

	t.Run("until: mutate takes the path out of what is delivered", func(t *testing.T) {
		f.reset()
		f.sides("demo", demoBins, "slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n\t\t\t/usr/bin/tool: {until: mutate}\n")
		got, res := compare("demo_bins")
		clean(res)
		assert.Equal(t, got, "-/usr/bin/tool")
	})

	t.Run("a directory turned symlink loses what was under it", func(t *testing.T) {
		f.reset()
		f.sides("demo", "slices:\n\tconfig:\n\t\tcontents:\n\t\t\t/etc/demo/conf:\n", "slices:\n\tconfig:\n\t\tcontents:\n\t\t\t/etc/demo: {symlink: /usr/lib/demo}\n")
		got, res := compare("demo_config")
		clean(res)
		// /etc/demo is on both sides, as another kind of thing.
		assert.Equal(t, got, "-/etc/demo/conf ~/etc/demo/")
		c := res.Slices[0].Cuts[0]
		assert.Equal(t, c.Findings[1].Head.String(), "symlink to /usr/lib/demo")
	})

	t.Run("a file mode and a symlink target are changed entries", func(t *testing.T) {
		f.reset()
		f.sides("demo",
			"slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/run: {text: x, mode: 0700}\n\t\t\t/usr/bin/sh: {symlink: demo}\n\t\t\t/usr/bin/demo:\n",
			"slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/run: {text: x, mode: 0755}\n\t\t\t/usr/bin/sh: {symlink: tool}\n\t\t\t/usr/bin/demo:\n\t\t\t/usr/bin/tool:\n")
		got, res := compare("demo_bins")
		clean(res)
		assert.Equal(t, got, "~/usr/bin/run ~/usr/bin/sh +/usr/bin/tool")
		c := res.Slices[0].Cuts[0]
		assert.Equal(t, c.Findings[0].Base.String()+" -> "+c.Findings[0].Head.String(), "file 0700 -> file 0755")
		assert.Equal(t, c.Findings[1].Base.String()+" -> "+c.Findings[1].Head.String(), "symlink to demo -> symlink to tool")
		assert.That(t, !res.Missing())
	})

	t.Run("a package no archive has is not available", func(t *testing.T) {
		f.reset()
		f.sides("ghost", "slices:\n\tall:\n\t\tcontents:\n\t\t\t/x:\n", "slices:\n\tall:\n\t\tcontents:\n\t\t\t/y:\n")
		_, res := compare("ghost_all")
		clean(res)
		c := res.Slices[0].Cuts[0]
		assert.Equal(t, c.Outcome, Unavailable)
		assert.Equal(t, c.Base.Missing, "ghost")
		assert.That(t, !res.Missing())
	})

	t.Run("an essential's package that is gone makes the head not available", func(t *testing.T) {
		f.reset()
		f.both("slices/ghost.yaml", "package: ghost\nslices:\n  all:\n    contents:\n      /x:\n")
		f.sides("demo", demoBins, "slices:\n\tbins:\n\t\tessential:\n\t\t\tghost_all:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n\t\t\t/usr/bin/tool:\n")
		_, res := compare("demo_bins")
		clean(res)
		c := res.Slices[0].Cuts[0]
		assert.Equal(t, c.Outcome, HeadNotAvailable)
		assert.Equal(t, c.Head.Missing, "ghost")
		assert.That(t, res.Missing())
	})

	t.Run("a base the archive moved under is pruned and compared", func(t *testing.T) {
		// One path, then two: chisel words them differently. The PR drops
		// them, and with them, unseen, /usr/bin/tool.
		for _, extra := range []string{"\t\t\t/nope:\n", "\t\t\t/nope:\n\t\t\t/nada/**:\n\t\t\t/usr/bin/renamed: {copy: /gone}\n"} {
			f.reset()
			f.sides("demo", demoBins+extra, "slices:\n\tbins:\n\t\tcontents:\n\t\t\t/usr/bin/demo:\n")
			got, res := compare("demo_bins")
			clean(res)
			c := res.Slices[0].Cuts[0]
			assert.Equal(t, c.Outcome, Compared)
			want := "-/usr/bin/tool !/nope"
			if strings.Count(extra, "\n") > 1 {
				want = "-/usr/bin/tool !/nada/** !/nope !/usr/bin/renamed"
			}
			assert.Equal(t, got, want)
			assert.That(t, len(c.Base.Pruned) == strings.Count(extra, "\n"), "%v", c.Base.Pruned)
			assert.That(t, res.Missing())
		}
	})

	// The prefer loss of canonical/chisel#354, in the three shapes of the
	// design note. cdx-u_config is what changes: at the head it reaches
	// cdx-v_temp, whose /cdx/p is preferred and goes after mutate.
	prefer := func(s string) {
		f.reset()
		f.both("slices/cdx-s.yaml", "package: cdx-s\n"+strings.ReplaceAll(s, "\t", "  "))
		f.both("slices/cdx-v.yaml", "package: cdx-v\nslices:\n  temp:\n    contents:\n      /cdx/p: {text: x, until: mutate}\n      /cdx/v-file:\n")
		f.sides("cdx-u", "slices:\n\tconfig:\n\t\tcontents:\n\t\t\t/cdx/u-file:\n", "slices:\n\tconfig:\n\t\tessential:\n\t\t\tcdx-v_temp:\n\t\tcontents:\n\t\t\t/cdx/u-file:\n")
	}

	t.Run("prefer: the slice that spells it loses the path, and is cut", func(t *testing.T) {
		prefer("slices:\n\tbins:\n\t\tessential:\n\t\t\tcdx-u_config:\n\t\tcontents:\n\t\t\t/cdx/p: {text: x, prefer: cdx-v}\n\t\t\t/cdx/s-file:\n")
		got, res := compare("cdx-s_bins")
		clean(res)
		assert.Equal(t, got, "-/cdx/p +/cdx/v-file")
		assert.That(t, res.Missing())
		// The slice that changed lost nothing itself.
		got, _ = compare("cdx-u_config")
		assert.Equal(t, got, "+/cdx/v-file")
	})

	t.Run("prefer: a sibling slice spells it, and the one that lists the path is cut", func(t *testing.T) {
		prefer("slices:\n\tpolicy:\n\t\tcontents:\n\t\t\t/cdx/p: {text: x, prefer: cdx-v, until: mutate}\n\tbins:\n\t\tessential:\n\t\t\tcdx-u_config:\n\t\tcontents:\n\t\t\t/cdx/p: {text: x}\n\t\t\t/cdx/s-file:\n")
		got, res := compare("cdx-s_bins")
		clean(res)
		assert.Equal(t, got, "-/cdx/p +/cdx/v-file")
		got, _ = compare("cdx-s_policy")
		assert.Equal(t, got, "")
	})

	t.Run("prefer: a slice that only reaches the listing one is missed", func(t *testing.T) {
		prefer("slices:\n\tdata:\n\t\tcontents:\n\t\t\t/cdx/p: {text: x, prefer: cdx-v}\n\tbins:\n\t\tessential:\n\t\t\tcdx-s_data:\n\t\t\tcdx-u_config:\n\t\tcontents:\n\t\t\t/cdx/s-file:\n")
		// Known gap: cdx-s_bins loses /cdx/p and is not cut, since it
		// lists nothing given away. What is cut loses nothing.
		got, res := compare("cdx-s_bins")
		clean(res)
		assert.Equal(t, got, "<cdx-s_bins was not cut>")
		got, _ = compare("cdx-s_data")
		assert.Equal(t, got, "")
		assert.That(t, !res.Missing())
	})
}
