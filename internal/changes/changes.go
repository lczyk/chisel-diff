// Package changes reads the package slices files of two snapshots of a
// release and says, for each slice, whether it was removed, added, changed
// or is the same, and which slices are to be cut. It reads yaml and nothing
// else: what a slice delivers is chisel's to say.
package changes

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The directories chisel reads package slices files from.
var sliceDirs = []string{"slices", "bin-slices"}

// State is what became of a slice between the base and the head.
type State string

const (
	Removed State = "removed" // at the base only
	Added   State = "added"   // at the head only
	Changed State = "changed" // on both sides with another definition
	Same    State = "same"    // on both sides with the same definition
)

// Release is one snapshot's package slices files.
type Release struct {
	Dir        string
	chiselYAML []byte
	files      map[string]*file // by package
}

// file is one package slices file. A package is its file's basename, as
// chisel takes it, wherever under the slice dirs the file sits.
type file struct {
	raw    []byte
	top    map[string]any      // every key but slices, decoded
	names  []string            // the slices, in file order
	defs   map[string]any      // slice -> its decoded definition
	paths  map[string][]string // slice -> the paths its contents list
	prefer map[string]bool     // the paths some slice gives away with prefer
}

// Load reads the package slices files of the release at dir.
func Load(dir string) (*Release, error) {
	r := &Release{Dir: dir, files: map[string]*file{}}
	var err error
	if r.chiselYAML, err = os.ReadFile(filepath.Join(dir, "chisel.yaml")); err != nil {
		return nil, err
	}
	for _, sub := range sliceDirs {
		root := filepath.Join(dir, sub)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root && errors.Is(err, fs.ErrNotExist) {
					return fs.SkipAll
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
				return nil
			}
			pkg := strings.TrimSuffix(filepath.Base(path), ".yaml")
			rel, _ := filepath.Rel(dir, path)
			if _, dup := r.files[pkg]; dup {
				return fmt.Errorf("%s: package %s has a second file", rel, pkg)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f, err := parse(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			r.files[pkg] = f
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

func parse(raw []byte) (*file, error) {
	f := &file{raw: raw, top: map[string]any{}, defs: map[string]any{}, paths: map[string][]string{}, prefer: map[string]bool{}}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return f, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("not a mapping")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, root.Content[i+1]
		if key != "slices" {
			var v any
			if err := value.Decode(&v); err != nil {
				return nil, err
			}
			f.top[key] = v
			continue
		}
		// Decoded as yaml decodes it for chisel, so that an alias for the
		// whole mapping and a merge key inside it are what they stand for.
		var nodes map[string]yaml.Node
		if err := value.Decode(&nodes); err != nil {
			return nil, fmt.Errorf("slices: %w", err)
		}
		names := make([]string, 0, len(nodes))
		for name := range nodes {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			a, b := nodes[names[i]], nodes[names[j]]
			if a.Line != b.Line {
				return a.Line < b.Line
			}
			return names[i] < names[j]
		})
		for _, name := range names {
			node := nodes[name]
			var def any
			if err := node.Decode(&def); err != nil {
				return nil, fmt.Errorf("slice %s: %w", name, err)
			}
			var body struct {
				Contents map[string]struct {
					Prefer string `yaml:"prefer"`
				} `yaml:"contents"`
			}
			if err := node.Decode(&body); err != nil {
				return nil, fmt.Errorf("slice %s: %w", name, err)
			}
			f.names = append(f.names, name)
			f.defs[name] = def
			for path, entry := range body.Contents {
				f.paths[name] = append(f.paths[name], path)
				if entry.Prefer != "" {
					f.prefer[path] = true
				}
			}
		}
	}
	return f, nil
}

// Slice is one slice of a package and what the comparison makes of it.
type Slice struct {
	Package string
	Name    string
	State   State
	// Picked is set for a slice --slice named, which is reported and cut
	// whatever its state.
	Picked bool
	// Prefer is set for a slice that is cut for no other reason than that
	// it lists a path its package gives away with prefer.
	Prefer bool
}

// ID is the slice as chisel names it, <package>_<slice>.
func (s Slice) ID() string { return s.Package + "_" + s.Name }

// CutBase and CutHead say on which sides the slice is cut. A changed slice
// is cut on both and so is any slice picked or cut for prefer that both
// sides have; a picked slice one side has is cut there. A removed or added
// slice that was not picked is named and not cut.
func (s Slice) CutBase() bool {
	return s.State != Added && (s.Picked || s.Prefer || s.State == Changed)
}

func (s Slice) CutHead() bool {
	return s.State != Removed && (s.Picked || s.Prefer || s.State == Changed)
}

// Package is one package considered.
type Package struct {
	Name   string
	File   string  // what became of its file: added, removed, modified or same
	Slices []Slice // the base's in file order, then the ones only the head has
	// ArchiveDiffers is set when the file names another archive on the two
	// sides, so that another version of the package is the point.
	ArchiveDiffers bool
}

// Result is the comparison of two releases.
type Result struct {
	ChiselYAMLDiffers bool
	// Packages are the ones considered, as named or else sorted by name,
	// each with every slice it has on either side.
	Packages []Package
	// Prefer are the slices outside of what is reported that are cut on
	// both sides when anything else is, see Compare.
	Prefer []Slice
	// ArchiveDiffers holds every package of the release whose file names
	// another archive on the two sides, considered or not.
	ArchiveDiffers map[string]bool
}

// Reported are the slices a report names: the picked ones when --slice was
// given, else the removed, added and changed ones.
func (r *Result) Reported() []Slice {
	var out []Slice
	for _, p := range r.Packages {
		for _, s := range p.Slices {
			if s.Picked || (!r.picked() && s.State != Same) {
				out = append(out, s)
			}
		}
	}
	return out
}

func (r *Result) picked() bool {
	for _, p := range r.Packages {
		for _, s := range p.Slices {
			if s.Picked {
				return true
			}
		}
	}
	return false
}

// Compare says what state every slice of the packages considered is in.
// The packages considered are the named ones, or else every package whose
// file differs between the two releases, byte for byte, and with either the
// package of any <package>_<slice> pick. picks are --slice values: a bare name
// picks the slices of that name in the packages considered, "all" every
// slice of them.
func Compare(base, head *Release, named, picks []string) (*Result, error) {
	res := &Result{ChiselYAMLDiffers: !bytes.Equal(base.chiselYAML, head.chiselYAML), ArchiveDiffers: map[string]bool{}}
	for pkg, b := range base.files {
		if h := head.files[pkg]; h != nil && !reflect.DeepEqual(b.top["archive"], h.top["archive"]) {
			res.ArchiveDiffers[pkg] = true
		}
	}
	all := slices.Contains(picks, "all")
	bare := map[string]bool{} // slice names picked in every package
	full := map[string]bool{} // <package>_<slice> picks
	considered := slices.Clone(named)
	if len(considered) == 0 {
		for pkg, b := range base.files {
			if h := head.files[pkg]; h == nil || !bytes.Equal(b.raw, h.raw) {
				considered = append(considered, pkg)
			}
		}
		for pkg := range head.files {
			if base.files[pkg] == nil {
				considered = append(considered, pkg)
			}
		}
		sort.Strings(considered)
	}
	for _, pick := range picks {
		if pick == "all" {
			continue
		}
		if pkg, _, ok := strings.Cut(pick, "_"); ok {
			full[pick] = true
			considered = append(considered, pkg)
		} else {
			bare[pick] = true
		}
	}

	seen := map[string]bool{}
	matched := map[string]bool{} // the picks that named a slice
	for _, name := range considered {
		if seen[name] {
			continue
		}
		seen[name] = true
		b, h := base.files[name], head.files[name]
		p := Package{Name: name}
		switch {
		case b == nil && h == nil:
			return nil, fmt.Errorf("package %s has no file on either side", name)
		case b == nil:
			p.File = "added"
		case h == nil:
			p.File = "removed"
		case bytes.Equal(b.raw, h.raw):
			p.File = "same"
		default:
			p.File = "modified"
			p.ArchiveDiffers = !reflect.DeepEqual(b.top["archive"], h.top["archive"])
		}
		// A key of the file itself, a package-level essential say, changes
		// every slice, and so does anything in chisel.yaml.
		whole := res.ChiselYAMLDiffers || (b != nil && h != nil && !reflect.DeepEqual(b.top, h.top))
		var names []string
		if b != nil {
			names = append(names, b.names...)
		}
		if h != nil {
			for _, n := range h.names {
				if b == nil || !has(b, n) {
					names = append(names, n)
				}
			}
		}
		for _, n := range names {
			s := Slice{Package: name, Name: n, State: Same}
			switch {
			case b == nil || !has(b, n):
				s.State = Added
			case h == nil || !has(h, n):
				s.State = Removed
			case whole || !reflect.DeepEqual(b.defs[n], h.defs[n]):
				s.State = Changed
			}
			if all || bare[n] || full[s.ID()] {
				s.Picked = true
				matched[n], matched[s.ID()] = true, true
			}
			p.Slices = append(p.Slices, s)
		}
		res.Packages = append(res.Packages, p)
	}
	var unknown []string
	for name := range bare {
		if !matched[name] {
			unknown = append(unknown, name)
		}
	}
	for id := range full {
		if !matched[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("--slice %s: no such slice on either side", strings.Join(unknown, ", "))
	}

	// Chisel keeps prefer per package and path, not per slice: once another
	// package it prefers is selected, a package's own entry for the path is
	// skipped in every one of its slices. With until: mutate on the
	// preferred side the path then goes from a slice nobody touched
	// (canonical/chisel#354), and no changed slice shows the loss. So every
	// slice on both sides that lists such a path is cut as well, whenever
	// anything else is. This finds the loss where the slice that lists the
	// path reaches the preferred package. It misses a slice that only
	// reaches the listing slice through an essential, which would take
	// reading essentials here.
	cut := false
	covered := map[string]bool{}
	for _, s := range res.Reported() {
		covered[s.ID()] = true
		cut = cut || s.CutBase() || s.CutHead()
	}
	if cut {
		res.Prefer = preferring(base, head, covered)
	}
	return res, nil
}

// preferring are the slices on both sides that list a path their package
// gives away with prefer on either side, less the ones already covered.
func preferring(base, head *Release, covered map[string]bool) []Slice {
	var out []Slice
	for pkg, b := range base.files {
		h := head.files[pkg]
		if h == nil || (len(b.prefer) == 0 && len(h.prefer) == 0) {
			continue
		}
		for _, name := range b.names {
			s := Slice{Package: pkg, Name: name, State: Same, Prefer: true}
			if !has(h, name) || covered[s.ID()] {
				continue
			}
			lists := func(f *file) bool {
				return slices.ContainsFunc(f.paths[name], func(p string) bool { return b.prefer[p] || h.prefer[p] })
			}
			if lists(b) || lists(h) {
				if !reflect.DeepEqual(b.defs[name], h.defs[name]) || !reflect.DeepEqual(b.top, h.top) {
					s.State = Changed
				}
				out = append(out, s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

func has(f *file, slice string) bool {
	_, ok := f.defs[slice]
	return ok
}
