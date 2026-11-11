package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/lczyk/chisel-coverage/pkg/fold"

	"github.com/lczyk/chisel-diff/internal/diff"
)

// renderText writes the report: nothing indented and no blank lines. A slice
// that was compared is one block per set of architectures that agree, a
// header "<package> <version> <slice> [arches]" and under it "-<path>" for
// an entry lost and "+<path>" for one gained, so grep '^-' is the list of
// what went missing. A slice with nothing to show under a header is one
// line, with what is to be said of it after a colon. With group unset every
// architecture is a block of its own with its real paths.
func renderText(w io.Writer, res *diff.Result, group bool) {
	absent := map[string]bool{} // packages whose own absence was told
	for _, s := range res.Slices {
		if len(s.Cuts) == 0 {
			fmt.Fprintf(w, "%s %s: %s\n", s.Package, s.Name, s.State)
			continue
		}
		var arches []string
		var items [][]fold.Item
		versions := map[string]string{}
		notes := map[string][]string{} // what is said of the slice -> the architectures
		var order []string
		for _, c := range s.Cuts {
			versions[c.Arch] = version(c)
			switch c.Outcome {
			case diff.Compared, diff.Listed:
				arches = append(arches, c.Arch)
				items = append(items, cutItems(c))
				continue
			}
			note := note(s, c)
			if _, ok := notes[note]; !ok {
				order = append(order, note)
			}
			notes[note] = append(notes[note], c.Arch)
		}
		for _, g := range groups(arches, items, group) {
			if len(g.Entries) == 0 {
				continue
			}
			fmt.Fprintf(w, "%s%s %s [%s]\n", s.Package, versions[g.Arches[0]], s.Name, strings.Join(g.Arches, ", "))
			for _, line := range lines(g) {
				fmt.Fprintln(w, line)
			}
		}
		for _, n := range order {
			// A package that is not there says so once, not once a slice.
			if own, ok := strings.CutPrefix(n, ownAbsence); ok {
				key := s.Package + " " + strings.Join(notes[n], ",")
				if !absent[key] {
					absent[key] = true
					fmt.Fprintf(w, "%s [%s]: not available%s\n", s.Package, strings.Join(notes[n], ", "), own)
				}
				continue
			}
			fmt.Fprintf(w, "%s %s [%s]: %s\n", s.Package, s.Name, strings.Join(notes[n], ", "), n)
		}
	}
}

// ownAbsence starts the note of a slice whose own package is on neither
// side for an architecture.
const ownAbsence = "\x00"

// note is what is said of a cut that was not compared.
func note(s diff.Slice, c diff.Cut) string {
	side := c.Head
	if c.Outcome == diff.BaseNotAvailable {
		side = c.Base
	}
	ignored := ""
	if len(side.Ignored) > 0 {
		ignored = " (skipped archives: " + strings.Join(side.Ignored, ", ") + ")"
	}
	switch c.Outcome {
	case diff.Error:
		return "error"
	case diff.Unavailable:
		if side.Status == diff.None {
			side = c.Base
		}
		if side.Missing == s.Package {
			return ownAbsence + ignored
		}
	}
	n := string(c.Outcome)
	if side.Missing != "" && side.Missing != s.Package {
		n += ": " + side.Missing
	}
	return n + ignored
}

// version is the header's version: the head's, or both sides' where they
// differ on purpose.
func version(c diff.Cut) string {
	b, h := c.Base.Version, c.Head.Version
	switch {
	case b != "" && h != "" && b != h:
		return " " + b + " -> " + h
	case h != "":
		return " " + h
	case b != "":
		return " " + b
	}
	return ""
}

// cutItems are the findings of one cut as fold takes them. A directory is
// left out when something under it is reported in the same state, so losing
// a file and with it the now empty directory around it is one line.
func cutItems(c diff.Cut) []fold.Item {
	var out []fold.Item
	for _, f := range c.Findings {
		if f.Dir && under(c.Findings, f) {
			continue
		}
		path := f.Path
		if f.Dir {
			path += "/"
		}
		out = append(out, fold.Item{Key: fold.Normalise(path, c.Arch), Tag: string(f.Change), Raw: path})
	}
	return out
}

func under(findings []diff.Finding, dir diff.Finding) bool {
	for _, f := range findings {
		if f.Change == dir.Change && strings.HasPrefix(f.Path, dir.Path+"/") {
			return true
		}
	}
	return false
}

// groups folds the architectures that agree, or with group unset keeps each
// one apart with its real paths.
func groups(arches []string, items [][]fold.Item, group bool) []fold.Group {
	if group {
		return fold.Fold(arches, items)
	}
	var out []fold.Group
	for i, arch := range arches {
		out = append(out, fold.Fold([]string{arch}, [][]fold.Item{items[i]})...)
	}
	return out
}

// lines are a group's entries as the report prints them: the lost ones,
// then the gained ones, each sorted by path.
func lines(g fold.Group) []string {
	var lost, gained []string
	for _, e := range g.Entries {
		if e.Tag == string(diff.Lost) {
			lost = append(lost, "-"+e.Shown())
		} else {
			gained = append(gained, "+"+e.Shown())
		}
	}
	sort.Strings(lost)
	sort.Strings(gained)
	return append(lost, gained...)
}

// summary is the line -v ends with: slices are counted once each whatever
// the number of architectures, one that differs on any of them differs, and
// entries are counted per architecture before any directory is left out.
func summary(res *diff.Result) string {
	compared, differ, lost, gained := 0, 0, 0, 0
	for _, s := range res.Slices {
		was, differs := false, false
		for _, c := range s.Cuts {
			if c.Outcome == diff.Compared || c.Outcome == diff.Listed {
				was = true
			}
			for _, f := range c.Findings {
				differs = true
				if f.Change == diff.Lost {
					lost++
				} else {
					gained++
				}
			}
		}
		if was {
			compared++
		}
		if differs {
			differ++
		}
	}
	return fmt.Sprintf("%d slices compared, %d differ, %d lost, %d gained", compared, differ, lost, gained)
}
