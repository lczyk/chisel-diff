package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lczyk/chisel-coverage/pkg/fold"

	"github.com/lczyk/chisel-diff/internal/diff"
	"github.com/lczyk/chisel-diff/internal/listing"
)

// renderText writes the report: nothing indented and no blank lines. A slice
// that was compared is one block per set of architectures that agree, a
// header "<package>_<slice> <version> [arches]" and under it "-<path>" for
// an entry lost and "+<path>" for one gained, so grep '^-' is the list of
// what went missing. A slice with nothing to show under a header is one
// line, with what is to be said of it after a colon. "~<path>: <base> ->
// <head>" is an entry on both sides that changed, "!<path>: no content"
// is an entry deleted from the base because the archive no longer has it. With group unset every
// architecture is a block of its own with its real paths.
func renderText(w io.Writer, res *diff.Result, group, tree bool, st style) {
	absent := map[string]bool{} // packages whose own absence was told
	for _, s := range res.Slices {
		if len(s.Cuts) == 0 {
			fmt.Fprintf(w, "%s: %s\n", s.Shown(), s.State)
			continue
		}
		var arches []string
		var items [][]fold.Item
		rawTags := map[string]string{} // arch and raw path -> the raw tag
		versions := map[string]string{}
		notes := map[string][]string{} // what is said of the slice -> the architectures
		var order []string
		for _, c := range s.Cuts {
			versions[c.Arch] = version(c)
			switch c.Outcome {
			case diff.Compared, diff.Listed:
				arches = append(arches, c.Arch)
				items = append(items, cutItems(c))
				for _, it := range items[len(items)-1] {
					rawTags[c.Arch+"\x00"+it.Raw] = it.RawTag
				}
				continue
			}
			// A base pruned and cut opposite a head that is not: what
			// was deleted from it is still told.
			if len(c.Findings) > 0 {
				arches = append(arches, c.Arch)
				items = append(items, cutItems(c))
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
			fmt.Fprintf(w, "%s%s%s%s [%s]\n", st.name, s.Shown(), st.reset, versions[g.Arches[0]], strings.Join(g.Arches, ", "))
			if tree {
				for _, line := range treeLines(entries(g, rawTags), st) {
					fmt.Fprintln(w, line)
				}
				continue
			}
			for _, e := range entries(g, rawTags) {
				fmt.Fprintln(w, st.line(e.flat()))
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
			fmt.Fprintf(w, "%s [%s]: %s\n", s.Shown(), strings.Join(notes[n], ", "), n)
		}
	}
}

// style colours the text report. The zero value colours nothing.
type style struct {
	name, red, green, yellow, dim, reset string
}

// colours is the style for a terminal: a little, not a lot.
var colours = style{name: "\x1b[1;34m", red: "\x1b[31m", green: "\x1b[32m", yellow: "\x1b[33m", dim: "\x1b[2m", reset: "\x1b[0m"}

// styleFor picks colours when w is a terminal and NO_COLOR is unset or empty
// (https://no-color.org), else nothing.
func styleFor(w io.Writer) style {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return style{}
	}
	f, ok := w.(*os.File)
	if !ok {
		return style{}
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return style{}
	}
	return colours
}

// line colours a finding by its sign: red lost, green gained, yellow
// changed or deleted from the base.
func (st style) line(l string) string {
	return st.paint(l[0], l)
}

// paint colours s as the sign says, and leaves it plain for any other.
func (st style) paint(sign byte, s string) string {
	c := map[byte]string{'-': st.red, '+': st.green, '~': st.yellow, '!': st.yellow}[sign]
	if c == "" {
		return s
	}
	return c + s + st.reset
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
		// A pair is told under its own name, whichever package is missing.
		if side.Missing == s.Package && s.HeadName == "" {
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
		out = append(out, item(f, c.Arch))
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

// rline is one line of a block: its sign, its path as shown (a directory
// with a trailing slash), and what is said after it, "" for a lost or gained
// entry.
type rline struct {
	sign byte
	path string
	desc string
}

// flat is the entry as the report prints it, a line of its own.
func (e rline) flat() string {
	if e.desc == "" {
		return string(e.sign) + e.path
	}
	return string(e.sign) + e.path + ": " + e.desc
}

// entries are a group's entries as the report prints them: lost, gained,
// changed, deleted from the base, each sorted by path. A changed entry's
// descriptions are each architecture's own when they all agree, so a group
// of one shows its real symlink target, and the normalised ones otherwise.
func entries(g fold.Group, rawTags map[string]string) []rline {
	sorted := slices.Clone(g.Entries)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Shown() < sorted[j].Shown() })
	var lost, gained, changed, pruned []rline
	for _, e := range sorted {
		change, b, h := splitTag(e.Tag)
		switch change {
		case diff.Lost:
			lost = append(lost, rline{'-', e.Shown(), ""})
		case diff.Gained:
			gained = append(gained, rline{'+', e.Shown(), ""})
		case diff.Changed:
			if raw := rawTags[g.Arches[0]+"\x00"+e.Raw[0]]; raw != "" {
				same := true
				for i, arch := range g.Arches {
					same = same && rawTags[arch+"\x00"+e.Raw[i]] == raw
				}
				if same {
					_, b, h = splitTag(raw)
				}
			}
			changed = append(changed, rline{'~', e.Shown(), b + " -> " + h})
		default:
			pruned = append(pruned, rline{'!', e.Shown(), "no content"})
		}
	}
	return append(append(append(lost, gained...), changed...), pruned...)
}

// splitTag reads a finding's tag: the change and, for a changed entry, the
// two descriptions. A NUL keeps them apart, since a symlink target may
// itself hold " -> ".
func splitTag(tag string) (diff.Change, string, string) {
	parts := strings.SplitN(tag, "\x00", 3)
	if len(parts) < 3 {
		return diff.Change(parts[0]), "", ""
	}
	return diff.Change(parts[0]), parts[1], parts[2]
}

// item is a finding as fold takes it. The tag is the change, and for a
// changed entry the two descriptions, so that only architectures where the
// entry changed the same way fold; a symlink target is normalised like a
// path.
func item(f diff.Finding, arch string) fold.Item {
	path := f.Path
	if f.Dir {
		path += "/"
	}
	it := fold.Item{Key: fold.Normalise(path, arch), Tag: string(f.Change), Raw: path}
	if f.Change == diff.Changed {
		describe := func(e listing.Entry, norm bool) string {
			if norm && e.Kind == listing.Symlink {
				e.Target = fold.Normalise(e.Target, arch)
			}
			return e.String()
		}
		it.Tag += "\x00" + describe(f.Base, true) + "\x00" + describe(f.Head, true)
		it.RawTag = string(f.Change) + "\x00" + describe(f.Base, false) + "\x00" + describe(f.Head, false)
	}
	return it
}

// noContent is how many of a slice's counted findings are no content ones.
func noContent(seen map[string]bool) int {
	n := 0
	for key := range seen {
		if strings.HasPrefix(key, string(diff.NoContent)+"\x00") {
			n++
		}
	}
	return n
}

// summary is the line -v ends with, shaped like git diff --shortstat:
// "4/4 slices differ on 2/7 arches, 0 paths gained(+), 124 paths lost(-), 0
// changed(~)", and "1 no content(!)" when there is any. Slices are counted
// once each whatever the number of architectures, one that differs on any of
// them differs, and the arches are those any slice differs on, of those cut.
// Paths are counted once a slice, however many architectures have them and
// however the report splits them into blocks, a path that names the
// architecture normalised and a directory left out when something under it
// is in the same state. A phrase is coloured by whether it is good news.
func summary(res *diff.Result, st style) string {
	compared, differ, lost, gained, changed, pruned := 0, 0, 0, 0, 0, 0
	cut, differing := map[string]bool{}, map[string]bool{}
	for _, s := range res.Slices {
		was := false
		seen := map[string]bool{} // change and normalised path
		for _, c := range s.Cuts {
			if c.Outcome == diff.Compared || c.Outcome == diff.Listed {
				was = true
				cut[c.Arch] = true
			}
			for _, f := range c.Findings {
				if f.Dir && under(c.Findings, f) {
					continue
				}
				if f.Change != diff.NoContent {
					differing[c.Arch] = true
				}
				path := f.Path
				if f.Dir {
					path += "/"
				}
				key := string(f.Change) + "\x00" + fold.Normalise(path, c.Arch)
				if seen[key] {
					continue
				}
				seen[key] = true
				switch f.Change {
				case diff.Lost:
					lost++
				case diff.Gained:
					gained++
				case diff.Changed:
					changed++
				default:
					pruned++
				}
			}
		}
		if was {
			compared++
		}
		if len(seen) > noContent(seen) {
			differ++
		}
	}
	// paint colours a phrase by its count: zero's colour at none, more's
	// else. The sign after it stays plain.
	paint := func(n int, zero, more, phrase string) string {
		c := more
		if n == 0 {
			c = zero
		}
		if c == "" {
			return phrase
		}
		return c + phrase + st.reset
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	}
	arches := ""
	if len(cut) > 1 {
		arches = fmt.Sprintf(" on %d/%d arches", len(differing), len(cut))
	}
	paths := func(n int) string { return strconv.Itoa(n) + " " + plural(n, "path", "paths") }
	out := strings.Join([]string{
		paint(differ, st.green, st.yellow, fmt.Sprintf("%d/%d %s %s", differ, compared, plural(compared, "slice", "slices"), plural(compared, "differs", "differ"))+arches),
		paint(gained, st.yellow, st.green, paths(gained)+" gained") + "(+)",
		paint(lost, st.green, st.red, paths(lost)+" lost") + "(-)",
		paint(changed, st.green, st.yellow, strconv.Itoa(changed)+" changed") + "(~)",
	}, ", ")
	if pruned > 0 {
		out += ", " + paint(pruned, "", st.yellow, strconv.Itoa(pruned)+" no content") + "(!)"
	}
	return out
}
