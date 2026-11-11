package changes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lczyk/assert"
)

// release writes a release dir from path -> content and loads it.
func release(t *testing.T, files map[string]string) *Release {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["chisel.yaml"]; !ok {
		files["chisel.yaml"] = "format: v1\n"
	}
	for path, content := range files {
		full := filepath.Join(dir, path)
		assert.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		assert.NoError(t, os.WriteFile(full, []byte(content), 0644))
	}
	r, err := Load(dir)
	assert.NoError(t, err)
	return r
}

// states renders a result as "<package>_<slice>:<state>" with a + for a
// picked slice, in order.
func states(r *Result) string {
	var out []string
	for _, p := range r.Packages {
		for _, s := range p.Slices {
			line := s.ID() + ":" + string(s.State)
			if s.Picked {
				line += "+"
			}
			out = append(out, line)
		}
	}
	return strings.Join(out, " ")
}

func ids(slices []Slice) string {
	var out []string
	for _, s := range slices {
		out = append(out, s.ID())
	}
	return strings.Join(out, " ")
}

const bash = `package: bash

essential:
  - bash_copyright

slices:
  bins:
    essential:
      - libc6_libs
    contents:
      /usr/bin/bash:
      /usr/bin/rbash:
  config:
    contents:
      /etc/skel/.bashrc:
  copyright:
    contents:
      /usr/share/doc/bash/copyright:
`

func TestStates(t *testing.T) {
	base := release(t, map[string]string{
		"slices/bash.yaml": bash,
		"slices/dash.yaml": "package: dash\nslices:\n  bins:\n    contents:\n      /usr/bin/dash:\n",
		"slices/gone.yaml": "package: gone\nslices:\n  all:\n    contents:\n      /x:\n",
	})
	head := release(t, map[string]string{
		// rbash dropped from bins, config rewritten with the same meaning,
		// copyright gone, a new slice.
		"slices/bash.yaml": `package: bash
essential: [bash_copyright]
slices:
  # a comment
  config: {contents: {/etc/skel/.bashrc: }}
  bins:
    contents:
      /usr/bin/bash:
    essential:
      - libc6_libs
  scripts:
    contents:
      /usr/bin/bashbug:
`,
		"slices/dash.yaml":      "package: dash\nslices:\n  bins:\n    contents:\n      /usr/bin/dash:\n",
		"slices/extra/new.yaml": "package: new\nslices:\n  all:\n    contents:\n      /y:\n",
	})
	res, err := Compare(base, head, nil, nil)
	assert.NoError(t, err)
	assert.That(t, !res.ChiselYAMLDiffers)
	// dash's file is the same on both sides, so it is not considered.
	assert.Equal(t, states(res), "bash_bins:changed bash_config:same bash_copyright:removed bash_scripts:added gone_all:removed new_all:added")
	assert.Equal(t, res.Packages[0].File, "modified")
	assert.Equal(t, res.Packages[1].File, "removed")
	assert.Equal(t, res.Packages[2].File, "added")
	assert.Equal(t, ids(res.Reported()), "bash_bins bash_copyright bash_scripts gone_all new_all")
	// Only the changed slice is cut.
	for _, s := range res.Reported() {
		assert.Equal(t, s.CutBase(), s.ID() == "bash_bins", s.ID())
		assert.Equal(t, s.CutHead(), s.ID() == "bash_bins", s.ID())
	}

	// A named package is considered whatever became of its file.
	res, err = Compare(base, head, []string{"dash", "bash", "dash"}, nil)
	assert.NoError(t, err)
	assert.Equal(t, states(res), "dash_bins:same bash_bins:changed bash_config:same bash_copyright:removed bash_scripts:added")
	assert.Equal(t, res.Packages[0].File, "same")
	_, err = Compare(base, head, []string{"zsh"}, nil)
	assert.Error(t, err, "package zsh has no file on either side")
}

func TestPicks(t *testing.T) {
	base := release(t, map[string]string{
		"slices/bash.yaml": bash,
		"slices/dash.yaml": "package: dash\nslices:\n  bins:\n    contents:\n      /usr/bin/dash:\n",
	})
	head := release(t, map[string]string{
		"slices/bash.yaml": strings.Replace(bash, "      /usr/bin/rbash:\n", "", 1) + "  scripts:\n    contents:\n      /x:\n",
		"slices/dash.yaml": "package: dash\nslices:\n  bins:\n    contents:\n      /usr/bin/dash:\n",
	})
	// A bare name picks by name in the packages considered, whatever the
	// state, and is all that is reported.
	res, err := Compare(base, head, nil, []string{"config"})
	assert.NoError(t, err)
	assert.Equal(t, states(res), "bash_bins:changed bash_config:same+ bash_copyright:same bash_scripts:added")
	assert.Equal(t, ids(res.Reported()), "bash_config")
	assert.That(t, res.Reported()[0].CutBase() && res.Reported()[0].CutHead())

	// A full name brings its package in.
	res, err = Compare(base, head, nil, []string{"dash_bins", "scripts"})
	assert.NoError(t, err)
	assert.Equal(t, ids(res.Reported()), "bash_scripts dash_bins")
	_, err = Compare(base, head, []string{"bash"}, []string{"dash_bins", "scripts", "nope", "bash_nope"})
	assert.Error(t, err, "--slice bash_nope, nope: no such slice on either side")

	// A picked slice one side has is cut on that side.
	res, err = Compare(base, head, []string{"bash"}, []string{"scripts"})
	assert.NoError(t, err)
	s := res.Reported()[0]
	assert.Equal(t, s.State, Added)
	assert.That(t, !s.CutBase() && s.CutHead())

	res, err = Compare(base, head, []string{"bash", "dash"}, []string{"all"})
	assert.NoError(t, err)
	assert.Equal(t, states(res), "bash_bins:changed+ bash_config:same+ bash_copyright:same+ bash_scripts:added+ dash_bins:same+")
}

func TestWhatChangesEverySlice(t *testing.T) {
	base := release(t, map[string]string{"slices/bash.yaml": bash})
	// A key of the file itself.
	head := release(t, map[string]string{"slices/bash.yaml": strings.Replace(bash, "  - bash_copyright\n", "  - bash_copyright\n  - bash_config\n", 1)})
	res, err := Compare(base, head, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, states(res), "bash_bins:changed bash_config:changed bash_copyright:changed")
	assert.That(t, !res.Packages[0].ArchiveDiffers)

	head = release(t, map[string]string{"slices/bash.yaml": "archive: ubuntu-fips\n" + bash})
	res, err = Compare(base, head, nil, nil)
	assert.NoError(t, err)
	assert.That(t, res.Packages[0].ArchiveDiffers)

	// chisel.yaml: every slice of the packages considered, which are none
	// unless named.
	head = release(t, map[string]string{"chisel.yaml": "format: v2\n", "slices/bash.yaml": bash})
	res, err = Compare(base, head, nil, nil)
	assert.NoError(t, err)
	assert.That(t, res.ChiselYAMLDiffers)
	assert.Len(t, res.Packages, 0)
	res, err = Compare(base, head, []string{"bash"}, nil)
	assert.NoError(t, err)
	assert.Equal(t, states(res), "bash_bins:changed bash_config:changed bash_copyright:changed")

	// Moving a file between directories changes nothing.
	head = release(t, map[string]string{"bin-slices/deep/bash.yaml": bash})
	res, err = Compare(base, head, nil, nil)
	assert.NoError(t, err)
	assert.Len(t, res.Packages, 0)
}

func TestPreferSlicesAreCutToo(t *testing.T) {
	files := func(u string) map[string]string {
		return map[string]string{
			// policy spells the prefer, bins lists the path plainly, plain
			// lists another path.
			"slices/cdx-s.yaml": `package: cdx-s
slices:
  policy:
    contents:
      /cdx/p: {text: x, prefer: cdx-v, until: mutate}
  bins:
    essential:
      cdx-u_config:
    contents:
      /cdx/p: {text: x}
      /cdx/s-file:
  plain:
    contents:
      /cdx/s-file:
`,
			"slices/cdx-v.yaml": "package: cdx-v\nslices:\n  temp:\n    contents:\n      /cdx/p: {text: x, until: mutate}\n",
			"slices/cdx-u.yaml": u,
		}
	}
	base := release(t, files("package: cdx-u\nslices:\n  config:\n    contents:\n      /cdx/u-file:\n"))
	head := release(t, files("package: cdx-u\nslices:\n  config:\n    essential:\n      cdx-v_temp:\n    contents:\n      /cdx/u-file:\n"))
	res, err := Compare(base, head, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, ids(res.Reported()), "cdx-u_config")
	// Every slice of cdx-s that lists the path given away, whichever spells
	// the prefer, in a package that was not considered. Not cdx-v's, which
	// gives nothing away.
	assert.Equal(t, ids(res.Prefer), "cdx-s_bins cdx-s_policy")
	for _, s := range res.Prefer {
		assert.That(t, s.Prefer && s.CutBase() && s.CutHead() && s.State == Same, s.ID())
	}

	// Nothing else to cut: nothing cut for prefer either.
	res, err = Compare(base, base, nil, nil)
	assert.NoError(t, err)
	assert.Len(t, res.Prefer, 0)
	res, err = Compare(base, head, []string{"cdx-v"}, nil)
	assert.NoError(t, err)
	assert.Len(t, res.Reported(), 0)
	assert.Len(t, res.Prefer, 0)

	// A slice that is reported anyway is not listed twice.
	res, err = Compare(base, head, nil, []string{"cdx-s_bins"})
	assert.NoError(t, err)
	assert.Equal(t, ids(res.Reported()), "cdx-s_bins")
	assert.Equal(t, ids(res.Prefer), "cdx-s_policy")
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	assert.Error(t, err, "chisel.yaml")
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "chisel.yaml"), nil, 0644))
	r, err := Load(dir)
	assert.NoError(t, err)
	assert.Len(t, r.files, 0)

	assert.NoError(t, os.MkdirAll(filepath.Join(dir, "slices", "a"), 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "slices", "bash.yaml"), []byte("slices: [\n"), 0644))
	_, err = Load(dir)
	assert.Error(t, err, "slices/bash.yaml: ")
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "slices", "bash.yaml"), []byte(bash), 0644))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "slices", "a", "bash.yaml"), []byte(bash), 0644))
	_, err = Load(dir)
	assert.Error(t, err, "package bash has a second file")
}
