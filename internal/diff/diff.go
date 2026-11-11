// Package diff makes the comparison: it cuts the slices that are to be cut
// from both releases on every architecture, with the real chisel, and says
// what each lost and gained.
package diff

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lczyk/chisel-coverage/pkg/cut"
	"github.com/lczyk/chisel-coverage/pkg/pool"

	"github.com/lczyk/chisel-diff/internal/changes"
	"github.com/lczyk/chisel-diff/internal/listing"
)

// Options configures a run.
type Options struct {
	Base, Head string   // the two release dirs
	Arches     []string // the first is cut alone before the rest
	// OneArchive says both sides are expected to see the same debs: it is
	// not set when chisel.yaml differs, where another archive is the point.
	OneArchive bool
	Keep       bool
	Jobs       int
	Cache      string
	Runner     *cut.Runner
	Log        io.Writer
	Verbose    io.Writer
}

// Status is how one side's cut of a slice on an architecture ended.
type Status string

const (
	WasCut       Status = "cut"
	NotAvailable Status = "not-available" // chisel found no such package, the slice's own or one it reaches
	Failed       Status = "failed"
	Skipped      Status = "skipped" // not cut: the lead of its package found the package missing
	None         Status = "none"    // not cut: the slice is not on this side, or is only named
)

// Side is one side's cut of a slice on an architecture.
type Side struct {
	Status  Status
	Pruned  []string // the contents entries deleted before the base would cut
	Entries int      // in the rootfs
	Version string   // of the slice's own package
	Missing string   // the package chisel did not find
	Ignored []string // the archives chisel skipped for want of credentials
	Err     error
	entries []listing.Entry
}

// Outcome is what a slice's two cuts on an architecture add up to.
type Outcome string

const (
	Compared         Outcome = "compared"
	Listed           Outcome = "listed"             // cut on the one side that has the slice
	Unavailable      Outcome = "not available"      // on neither side
	BaseNotAvailable Outcome = "base not available" // nothing to compare with, nothing lost
	HeadNotAvailable Outcome = "head not available" // the slice no longer cuts here
	Error            Outcome = "error"
)

// Change is what became of an entry.
type Change string

const (
	Lost   Change = "lost"
	Gained Change = "gained"
	// NoContent is an entry deleted from the base because the archive no
	// longer has what it names. It is neither lost nor gained.
	NoContent Change = "no-content"
	// Changed is a path on both sides that is another kind of thing, a
	// regular file with another mode, or a symlink to another target. It is
	// reported and never fails the check.
	Changed Change = "changed"
)

// Finding is one entry that is on one side only.
type Finding struct {
	Path   string // with no trailing slash
	Dir    bool   // a directory on the side it is reported for, the base's for a changed one
	Change Change
	// Base and Head are the two sides of a changed entry.
	Base, Head listing.Entry
}

// Cut is one slice on one architecture.
type Cut struct {
	Arch       string
	Base, Head Side
	Outcome    Outcome
	Findings   []Finding // lost before gained, each by path
}

// Slice is one slice that was reported or cut.
type Slice struct {
	changes.Slice
	Cuts []Cut // one per architecture, in order; none for a slice that is only named
}

// Result is a finished comparison.
type Result struct {
	Slices []Slice // the reported ones in order, then the ones cut for prefer
	// Errors are what kept the comparison from being whole: a cut that
	// failed, an archive that moved.
	Errors []error
}

// Missing reports whether something a slice delivered at the base is not
// delivered at the head: an entry lost, a slice removed, or a slice that no
// longer cuts on an architecture.
func (r *Result) Missing() bool {
	for _, s := range r.Slices {
		if s.State == changes.Removed {
			return true
		}
		for _, c := range s.Cuts {
			if c.Outcome == HeadNotAvailable {
				return true
			}
			for _, f := range c.Findings {
				if f.Change == Lost {
					return true
				}
			}
		}
	}
	return false
}

const (
	base = iota
	head
)

var sideNames = [2]string{"base", "head"}

// unit is one side's cut of one slice on one architecture.
type unit struct{ si, ai, side int }

type run struct {
	opts   Options
	slices []Slice
	mu     sync.Mutex
	// per side and architecture, the versions each deb was fetched in
	debs [2][]map[string]map[string]bool
}

// Run cuts what plan says is to be cut and compares the two sides. A cut
// that fails does not stop the others: it is in the result's Errors. The
// error returned is for a run that could not be made at all, ctx's when it
// was stopped.
func Run(ctx context.Context, opts Options, plan *changes.Result, archiveDiffers map[string]bool) (*Result, error) {
	r := &run{opts: opts}
	for _, s := range append(plan.Reported(), plan.Prefer...) {
		sl := Slice{Slice: s}
		if s.CutBase() || s.CutHead() {
			sl.Cuts = make([]Cut, len(opts.Arches))
			for ai, arch := range opts.Arches {
				sl.Cuts[ai] = Cut{Arch: arch, Base: Side{Status: None}, Head: Side{Status: None}}
			}
		}
		r.slices = append(r.slices, sl)
	}
	for side := range r.debs {
		r.debs[side] = make([]map[string]map[string]bool, len(opts.Arches))
		for ai := range opts.Arches {
			r.debs[side][ai] = map[string]map[string]bool{}
		}
	}

	p, err := pool.New(ctx, pool.Options{Jobs: opts.Jobs, Cache: opts.Cache, Runner: opts.Runner, Keep: opts.Keep,
		Name: "diff", Temp: "chisel-diff-*", Log: opts.Log, Verbose: opts.Verbose})
	if err != nil {
		return nil, err
	}
	defer p.Close()
	// The lead architecture alone first, so the rest find the arch: all
	// debs and the suite indexes cached.
	if err := p.Pass(r.groups(0, 1), r.done); err != nil {
		return nil, err
	}
	if err := p.Pass(r.groups(1, len(opts.Arches)), r.done); err != nil {
		return nil, err
	}
	if err := r.prune(p); err != nil {
		return nil, err
	}
	p.Summary()

	r.inherit()
	res := &Result{Slices: r.slices}
	for si := range r.slices {
		s := &r.slices[si]
		for ai := range s.Cuts {
			c := &s.Cuts[ai]
			r.settle(s, c)
			for _, side := range []*Side{&c.Base, &c.Head} {
				if side.Status == Failed {
					res.Errors = append(res.Errors, side.Err)
				}
			}
		}
	}
	res.Errors = append(res.Errors, r.moved(archiveDiffers)...)
	return res, nil
}

// groups are the units of the architectures [from, to): per side, package
// and architecture one group, led by the package's first slice, so that the
// others are not cut where the package itself is missing. One side never
// decides for the other.
func (r *run) groups(from, to int) []pool.Group {
	var out []pool.Group
	for ai := from; ai < to; ai++ {
		for side := range sideNames {
			index := map[string]int{}
			for si, s := range r.slices {
				if (side == base && !s.CutBase()) || (side == head && !s.CutHead()) {
					continue
				}
				u := r.unit(unit{si, ai, side})
				pkg, _ := s.On(side == base)
				if gi, ok := index[pkg]; ok {
					out[gi].Rest = append(out[gi].Rest, u)
					continue
				}
				index[pkg] = len(out)
				out = append(out, pool.Group{Lead: u})
			}
		}
	}
	return out
}

func (r *run) unit(u unit) pool.Unit {
	s, arch := r.slices[u.si], r.opts.Arches[u.ai]
	release := r.opts.Base
	if u.side == head {
		release = r.opts.Head
	}
	pkg, name := s.On(u.side == base)
	id := pkg + "_" + name
	// A slice may be cut twice on a side, picked and in a pair. No slice
	// name holds a "~".
	if s.HeadName != "" {
		name += "~" + strconv.Itoa(u.si)
	}
	return pool.Unit{
		Release: release,
		Arch:    arch,
		Slice:   id,
		Label:   id + "@" + sideNames[u.side],
		Dir:     filepath.Join(sideNames[u.side], pkg, arch, name),
		Tag:     u,
	}
}

// done takes one finished cut: its rootfs is listed while it exists. It
// stops the unit's group when the package itself is not there.
func (r *run) done(c pool.Cut) bool {
	u := c.Tag.(unit)
	var s Side
	var ce *cut.Error
	switch {
	case c.Err == nil:
		entries, err := listing.Walk(c.Root)
		if err != nil {
			s = Side{Status: Failed, Err: err}
			break
		}
		s = Side{Status: WasCut, Entries: len(entries), Version: c.Result.Version, Ignored: c.Result.Skipped, entries: entries}
	case errors.As(c.Err, &ce) && (errors.Is(c.Err, cut.ErrNotAvailable) || errors.Is(c.Err, cut.ErrEssentialNotAvailable)):
		s = Side{Status: NotAvailable, Missing: ce.Package, Ignored: ce.Skipped}
	default:
		s = Side{Status: Failed, Err: c.Err}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.Err == nil {
		seen := r.debs[u.side][u.ai]
		for _, d := range c.Result.Debs {
			if seen[d.Name] == nil {
				seen[d.Name] = map[string]bool{}
			}
			seen[d.Name][d.Version] = true
		}
	}
	cell := &r.slices[u.si].Cuts[u.ai]
	if u.side == base {
		cell.Base = s
	} else {
		cell.Head = s
	}
	return !errors.Is(c.Err, cut.ErrNotAvailable)
}

// inherit gives the units that were never cut what their package's lead
// found: the package is missing, and which archives chisel skipped on the
// way to saying so.
func (r *run) inherit() {
	for ai := range r.opts.Arches {
		for side := range sideNames {
			lead := map[string]*Side{}
			for si := range r.slices {
				s := &r.slices[si]
				if (side == base && !s.CutBase()) || (side == head && !s.CutHead()) {
					continue
				}
				cell := &s.Cuts[ai].Base
				if side == head {
					cell = &s.Cuts[ai].Head
				}
				pkg, _ := s.On(side == base)
				first, ok := lead[pkg]
				if !ok {
					lead[pkg] = cell
					continue
				}
				if cell.Status == None && first.Status == NotAvailable && first.Missing == pkg {
					cell.Status, cell.Missing, cell.Ignored = Skipped, pkg, first.Ignored
				}
			}
		}
	}
}

// settle works out what the two cuts of a slice on an architecture add up
// to. A unit that was planned and never ran was skipped: its package's lead
// found the package missing.
func (r *run) settle(s *Slice, c *Cut) {
	planned := [2]bool{s.CutBase(), s.CutHead()}
	sides := [2]*Side{&c.Base, &c.Head}
	for i, side := range sides {
		if planned[i] && side.Status == None {
			side.Status, side.Missing = Skipped, s.Package
			if i == head {
				side.Missing, _ = s.On(false)
			}
		}
	}
	gone := func(side *Side) bool { return side.Status == NotAvailable || side.Status == Skipped }
	switch {
	case c.Base.Status == Failed || c.Head.Status == Failed:
		c.Outcome = Error
	case !planned[base] || !planned[head]:
		// A picked slice that only one side has: everything it delivers
		// there was gained, or lost.
		only, change := &c.Head, Gained
		if planned[base] {
			only, change = &c.Base, Lost
		}
		if gone(only) {
			c.Outcome = Unavailable
			return
		}
		c.Outcome = Listed
		for _, e := range only.entries {
			c.Findings = append(c.Findings, Finding{Path: e.Path, Dir: e.Kind == listing.Dir, Change: change})
		}
	case gone(&c.Base) && gone(&c.Head):
		c.Outcome = Unavailable
	case gone(&c.Base):
		c.Outcome = BaseNotAvailable
	case gone(&c.Head):
		c.Outcome = HeadNotAvailable
	default:
		c.Outcome = Compared
		c.Findings = compare(c.Base.entries, c.Head.entries)
	}
	if c.Base.Status == WasCut {
		for _, path := range c.Base.Pruned {
			c.Findings = append(c.Findings, Finding{Path: strings.TrimSuffix(path, "/"), Dir: strings.HasSuffix(path, "/"), Change: NoContent})
		}
	}
}

// compare matches the entries of two rootfs trees by path. An entry at the
// base only is lost, one at the head only gained, one on both sides that is
// described differently changed.
func compare(baseEntries, headEntries []listing.Entry) []Finding {
	at := func(entries []listing.Entry) map[string]listing.Entry {
		m := make(map[string]listing.Entry, len(entries))
		for _, e := range entries {
			m[e.Path] = e
		}
		return m
	}
	b, h := at(baseEntries), at(headEntries)
	var out []Finding
	for _, e := range baseEntries {
		if _, ok := h[e.Path]; !ok {
			out = append(out, Finding{Path: e.Path, Dir: e.Kind == listing.Dir, Change: Lost})
		}
	}
	for _, e := range headEntries {
		was, ok := b[e.Path]
		switch {
		case !ok:
			out = append(out, Finding{Path: e.Path, Dir: e.Kind == listing.Dir, Change: Gained})
		case was.String() != e.String():
			out = append(out, Finding{Path: e.Path, Dir: was.Kind == listing.Dir, Change: Changed, Base: was, Head: e})
		}
	}
	return out
}

// moved says where the archive was seen to move. A difference between two
// cuts is down to the revisions only when both saw the same archive, and
// chisel has no way to pin one, so the run watches the debs fetched: one deb
// on one architecture is expected in one version, across the units of a
// side and, with OneArchive, across the sides too, unless the package's own
// file names another archive on the two sides. This is a tripwire and not a
// proof: a pool filename has no epoch, no archive and no digest in it.
func (r *run) moved(archiveDiffers map[string]bool) []error {
	var errs []error
	for ai, arch := range r.opts.Arches {
		for side, name := range sideNames {
			for _, deb := range sorted(r.debs[side][ai]) {
				if vs := sorted(r.debs[side][ai][deb]); len(vs) > 1 {
					errs = append(errs, fmt.Errorf("the archive moved during the run: %s on %s was fetched as %s for the %s", deb, arch, strings.Join(vs, " and "), name))
				}
			}
		}
		if !r.opts.OneArchive {
			continue
		}
		for _, deb := range sorted(r.debs[base][ai]) {
			b, h := sorted(r.debs[base][ai][deb]), sorted(r.debs[head][ai][deb])
			if len(b) != 1 || len(h) != 1 || b[0] == h[0] || archiveDiffers[deb] {
				continue
			}
			errs = append(errs, fmt.Errorf("the archive moved during the run: %s on %s was fetched as %s for the base and %s for the head", deb, arch, b[0], h[0]))
		}
	}
	return errs
}

func sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
