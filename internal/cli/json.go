package cli

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"

	"github.com/lczyk/chisel-coverage/pkg/fold"

	"github.com/lczyk/chisel-diff/internal/changes"
	"github.com/lczyk/chisel-diff/internal/diff"
	"github.com/lczyk/chisel-diff/internal/listing"
	"github.com/lczyk/chisel-diff/internal/sides"
)

type jsonReport struct {
	Run      jsonRun       `json:"run"`
	Packages []jsonPackage `json:"packages"`
}

type jsonRun struct {
	Chisel            string   `json:"chisel"`
	Release           string   `json:"release"`
	ChiselYAMLDiffers bool     `json:"chisel-yaml-differs"`
	Revisions         string   `json:"revisions"`
	MergeBase         bool     `json:"merge-base"`
	Base              jsonSide `json:"base"`
	Head              jsonSide `json:"head"`
}

// jsonSide is one side of the comparison: a revision, or what else it is.
type jsonSide struct {
	Kind sides.Kind `json:"kind"`
	Ref  *string    `json:"ref"`
	Tip  *string    `json:"tip"`
	Rev  *string    `json:"rev"`
	Dir  *string    `json:"dir,omitempty"`
}

type jsonPackage struct {
	Name   string      `json:"name"`
	File   string      `json:"file"`
	Slices []jsonSlice `json:"slices"`
}

type jsonSlice struct {
	Name  string        `json:"name"`
	State changes.State `json:"state"`
	// Forced is set for a slice that was cut without being changed: picked
	// by --slice, or for a path its package gives away with prefer.
	Forced   bool          `json:"forced"`
	Cuts     []jsonCut     `json:"cuts"`
	Findings []jsonFinding `json:"findings"`
}

type jsonCut struct {
	Arch    string       `json:"arch"`
	Outcome diff.Outcome `json:"outcome"`
	Base    jsonCutSide  `json:"base"`
	Head    jsonCutSide  `json:"head"`
}

type jsonCutSide struct {
	Status   diff.Status `json:"status"`
	Entries  int         `json:"entries"`
	Version  *string     `json:"version"`
	Missing  *string     `json:"missing"`
	Pruned   []string    `json:"pruned"`
	Archives []string    `json:"skipped-archives"`
	Error    *string     `json:"error"`
}

// jsonFinding is one entry lost or gained on a set of architectures. Path is
// the normalised one when the architectures spell it differently, and then
// Raw maps each to its own.
type jsonFinding struct {
	Path   string            `json:"path"`
	Change diff.Change       `json:"change"`
	Arches []string          `json:"arches"`
	Raw    map[string]string `json:"raw,omitempty"`
	// Base and Head are the two sides of a changed entry, as the first of
	// the arches has them.
	Base *jsonEntry `json:"base,omitempty"`
	Head *jsonEntry `json:"head,omitempty"`
}

type jsonEntry struct {
	Kind   listing.Kind `json:"kind"`
	Mode   string       `json:"mode,omitempty"`
	Target string       `json:"target,omitempty"`
}

func entry(e listing.Entry) *jsonEntry {
	j := &jsonEntry{Kind: e.Kind, Target: e.Target}
	if e.Kind == listing.File {
		j.Mode = strings.TrimPrefix(e.String(), "file ")
	}
	return j
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// renderJSON writes the report as one json document, folded as the text is
// unless group is unset. Every entry is in it, directories too. slices are
// the ones to report, with their cuts when any were made.
func renderJSON(w io.Writer, run jsonRun, plan *changes.Result, slices []diff.Slice, group bool) error {
	report := jsonReport{Run: run, Packages: []jsonPackage{}}
	index := map[string]int{}
	files := map[string]string{}
	for _, p := range plan.Packages {
		files[p.Name] = p.File
	}
	for _, s := range slices {
		pi, ok := index[s.Package]
		if !ok {
			pi = len(report.Packages)
			index[s.Package] = pi
			file := files[s.Package]
			if file == "" {
				file = "same"
			}
			report.Packages = append(report.Packages, jsonPackage{Name: s.Package, File: file})
		}
		js := jsonSlice{Name: s.Name, State: s.State, Forced: s.State != changes.Changed && len(s.Cuts) > 0, Cuts: []jsonCut{}, Findings: []jsonFinding{}}
		var arches []string
		var items [][]fold.Item
		byRaw := map[string]diff.Finding{} // arch and raw path -> the finding
		for _, c := range s.Cuts {
			js.Cuts = append(js.Cuts, jsonCut{Arch: c.Arch, Outcome: c.Outcome, Base: cutSide(c.Base), Head: cutSide(c.Head)})
			arches = append(arches, c.Arch)
			var its []fold.Item
			for _, f := range c.Findings {
				it := item(f, c.Arch)
				its = append(its, it)
				byRaw[c.Arch+"\x00"+it.Raw] = f
			}
			items = append(items, its)
		}
		for _, g := range groups(arches, items, group) {
			for _, e := range g.Entries {
				change, _, _ := strings.Cut(e.Tag, " ")
				f := jsonFinding{Path: e.Shown(), Change: diff.Change(change), Arches: g.Arches}
				if f.Change == diff.Changed {
					first := byRaw[g.Arches[0]+"\x00"+e.Raw[0]]
					f.Base, f.Head = entry(first.Base), entry(first.Head)
				}
				if f.Path != e.Raw[0] {
					f.Raw = map[string]string{}
					for i, arch := range g.Arches {
						f.Raw[arch] = e.Raw[i]
					}
				}
				js.Findings = append(js.Findings, f)
			}
		}
		report.Packages[pi].Slices = append(report.Packages[pi].Slices, js)
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(report)
}

func cutSide(s diff.Side) jsonCutSide {
	out := jsonCutSide{Status: s.Status, Entries: s.Entries, Version: ptr(s.Version), Missing: ptr(s.Missing), Archives: s.Ignored}
	if out.Archives == nil {
		out.Archives = []string{}
	}
	out.Pruned = s.Pruned
	if out.Pruned == nil {
		out.Pruned = []string{}
	}
	if s.Err != nil {
		out.Error = ptr(s.Err.Error())
	}
	return out
}

func runInfo(bin, release string, spec sides.Spec, s *sides.Sides, plan *changes.Result) jsonRun {
	side := func(x sides.Side) jsonSide {
		return jsonSide{Kind: x.Kind, Ref: ptr(x.Ref), Tip: ptr(x.Tip), Rev: ptr(x.Rev), Dir: ptr(x.Path)}
	}
	return jsonRun{
		Chisel:            chiselVersion(bin),
		Release:           release,
		ChiselYAMLDiffers: plan.ChiselYAMLDiffers,
		Revisions:         spec.Revisions,
		MergeBase:         s.MergeBase,
		Base:              side(s.Base),
		Head:              side(s.Head),
	}
}

// chiselVersion asks the chisel binary for its version, "" when it does not
// say.
func chiselVersion(bin string) string {
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
