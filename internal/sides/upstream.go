package sides

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Upstream is where chisel-releases lives, and where a release name is
// fetched from.
const Upstream = "https://github.com/canonical/chisel-releases"

// A release name as chisel parses it: <label>-<version>, as in ubuntu-24.04.
var releaseName = regexp.MustCompile(`^[a-z](?:-?[a-z0-9]){2,}-[0-9]+(?:\.?[0-9])+$`)

// CacheDir is where the clone of Upstream is kept between runs:
// $XDG_CACHE_HOME/chisel-diff, else ~/.cache/chisel-diff, "" when neither is
// set.
func CacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "chisel-diff", "chisel-releases.git")
	}
	if d := os.Getenv("HOME"); d != "" {
		return filepath.Join(d, ".cache", "chisel-diff", "chisel-releases.git")
	}
	return ""
}

// resolver turns the revisions of a comparison into commits: of the checkout
// --release names, or of the clone of upstream when --release is a release
// name, whose branch tip is then HEAD.
type resolver struct {
	ctx    context.Context
	s      *Sides
	spec   Spec
	g      *git   // where revisions resolve and merge bases are found
	named  string // the release name --release gave; "" for a checkout
	head   string // the commit HEAD stands for
	remote string // the checkout's remote for upstream; "" for none
	branch string // a release name looked for as a branch only, never a tag
	clone  *git   // the clone of upstream, once fetched
}

// open finds what --release is: a checkout, or a release name whose branch
// is fetched into the clone of upstream. prefix is where the release sits in
// its repo.
func (s *Sides) open(ctx context.Context, spec Spec) (r *resolver, prefix string, err error) {
	r = &resolver{ctx: ctx, s: s, spec: spec}
	if info, err := os.Stat(spec.Release); err == nil && info.IsDir() {
		r.g = &git{ctx: ctx, bin: spec.Git, dir: spec.Release}
		if prefix, err = r.g.path("rev-parse", "--show-prefix"); err != nil {
			return nil, "", fmt.Errorf("%s is not in a git checkout: %w", spec.Release, err)
		}
		if r.head, err = r.g.commit("HEAD"); err != nil {
			return nil, "", err
		}
		r.remote = r.findRemote()
		return r, prefix, nil
	}
	if !releaseName.MatchString(spec.Release) {
		return nil, "", fmt.Errorf("%s is not a directory or a release name like ubuntu-26.04", spec.Release)
	}
	if r.g, err = r.upstream(); err != nil {
		return nil, "", err
	}
	r.named = spec.Release
	if r.head, err = r.g.commit("refs/heads/" + r.named); err != nil {
		return nil, "", fmt.Errorf("%s is not a branch of %s", r.named, spec.Upstream)
	}
	return r, "", nil
}

// commit is the commit rev names, and how to name it to the reader. In a
// checkout, a release name that is no revision there is looked for as a
// branch of the remote for upstream, and then of upstream itself, which is
// fetched into the clone and not into the checkout.
func (r *resolver) commit(rev string) (sha, shown string, err error) {
	if r.named != "" {
		// HEAD, and @ for it, is the branch tip, whatever the clone's own
		// HEAD is.
		if rev == "@" || strings.HasPrefix(rev, "@~") || strings.HasPrefix(rev, "@^") {
			rev = "HEAD" + rev[1:]
		} else if strings.HasPrefix(rev, "@{") {
			rev = "HEAD" + rev
		}
		if rest, ok := strings.CutPrefix(rev, "HEAD"); ok && strings.HasPrefix(rest, "@") {
			return "", "", fmt.Errorf("%q: the clone of %s keeps no reflog of %s", rev, r.spec.Upstream, r.named)
		} else if ok && (rest == "" || strings.ContainsAny(rest[:1], "~^")) {
			sha, err = r.g.commit(r.head + rest)
		} else {
			sha, err = r.g.commit(rev)
		}
		if err != nil {
			return "", "", fmt.Errorf("%q is not a revision of %s", rev, r.spec.Upstream)
		}
		if rev == "HEAD" {
			return sha, r.named, nil
		}
		return sha, rev, nil
	}
	lookup := rev
	if rev == r.branch {
		lookup = "refs/heads/" + rev
	}
	sha, err = r.g.commit(lookup)
	if err == nil {
		r.stale(rev, sha)
		return sha, rev, nil
	}
	if !releaseName.MatchString(rev) {
		return "", "", err
	}
	if r.remote != "" {
		ref := r.remote + "/" + rev
		if sha, err := r.g.commit("refs/remotes/" + ref); err == nil {
			date, _ := r.g.line("log", "-1", "--format=%cs", sha)
			r.s.notef("%s is not a local branch: using %s (%s, committed %s), which git fetch %s refreshes", rev, ref, short(sha), date, r.remote)
			return sha, ref, nil
		}
	}
	clone, cerr := r.upstream()
	if cerr != nil {
		return "", "", fmt.Errorf("%w, and %w", err, cerr)
	}
	if sha, cerr = clone.commit("refs/heads/" + rev); cerr != nil {
		return "", "", fmt.Errorf("%q is not a revision of this checkout, nor a branch of %s", rev, r.spec.Upstream)
	}
	// The checkout reads the clone's objects, for merge bases and for the
	// snapshot, without a ref or an object of its own changing.
	r.g.alt = append(r.g.alt, filepath.Join(clone.dir, "objects"))
	r.s.notef("%s is a branch of neither this checkout nor its remotes: using it from %s (%s), kept in %s", rev, r.spec.Upstream, short(sha), clone.dir)
	return sha, rev, nil
}

// stale warns when rev is a local release branch that is not where the
// remote for upstream has it.
func (r *resolver) stale(rev, sha string) {
	if r.remote == "" || !releaseName.MatchString(rev) {
		return
	}
	if local, err := r.g.line("rev-parse", "--verify", "--quiet", "refs/heads/"+rev); err != nil || local != sha {
		return
	}
	ref := r.remote + "/" + rev
	up, err := r.g.commit("refs/remotes/" + ref)
	if err != nil || up == sha {
		return
	}
	behind, _ := r.g.line("rev-list", "--count", sha+".."+up)
	ahead, _ := r.g.line("rev-list", "--count", up+".."+sha)
	commits := func(n string) string {
		if n == "1" {
			return "1 commit"
		}
		return n + " commits"
	}
	switch {
	case ahead == "0":
		r.s.warnf("the local branch %s is %s behind %s, so what landed there since may read as part of this change: use %s, or update the branch", rev, commits(behind), ref, ref)
	case behind == "0":
		r.s.warnf("the local branch %s has %s that %s does not", rev, commits(ahead), ref)
	default:
		r.s.warnf("the local branch %s and %s have parted, %s and %s on each: use %s, or update the branch", rev, ref, commits(ahead), commits(behind), ref)
	}
}

// findRemote is the checkout's remote whose url is upstream's, "" for none.
func (r *resolver) findRemote() string {
	lines, err := r.g.out("config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(lines)), "\n") {
		key, url, _ := strings.Cut(line, " ")
		if sameRepo(url, r.spec.Upstream) {
			return strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		}
	}
	return ""
}

// sameRepo reports whether two git urls name the same repo, over https or
// ssh, with or without .git.
func sameRepo(a, b string) bool {
	norm := func(u string) string {
		u = strings.ToLower(strings.TrimSpace(u))
		for _, p := range []string{"https://", "http://", "ssh://", "git://", "git@"} {
			u = strings.TrimPrefix(u, p)
		}
		u = strings.Replace(u, "github.com:", "github.com/", 1)
		return strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	}
	return norm(a) == norm(b)
}

// upstream is the clone of upstream, made or fetched once a run. A fetch
// that fails leaves the clone as it was, with a warning.
func (r *resolver) upstream() (*git, error) {
	if r.clone != nil {
		return r.clone, nil
	}
	dir := r.spec.Cache
	if dir == "" {
		return nil, fmt.Errorf("no place to keep a clone of %s: neither XDG_CACHE_HOME nor HOME is set", r.spec.Upstream)
	}
	g := &git{ctx: r.ctx, bin: r.spec.Git, dir: dir}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		if _, err := g.out("fetch", "--quiet", "--prune", "--no-tags", r.spec.Upstream, "+refs/heads/*:refs/heads/*"); err != nil {
			if r.ctx.Err() != nil {
				return nil, r.ctx.Err()
			}
			r.s.warnf("cannot fetch %s, so its branches are as last fetched: %v", r.spec.Upstream, err)
		}
		r.clone = g
		return g, nil
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return nil, err
	}
	// Cloned beside and moved in whole, so that a run cut short or another
	// run at the same time never leaves half a clone where one is looked for.
	tmp, err := os.MkdirTemp(parent, ".clone-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	in := &git{ctx: r.ctx, bin: r.spec.Git, dir: parent}
	if _, err := in.out("clone", "--quiet", "--bare", "--no-tags", r.spec.Upstream, tmp); err != nil {
		return nil, fmt.Errorf("cannot clone %s: %w", r.spec.Upstream, err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, serr := os.Stat(filepath.Join(dir, "HEAD")); serr != nil {
			return nil, err
		}
	}
	r.s.notef("cloned %s into %s", r.spec.Upstream, dir)
	r.clone = g
	return g, nil
}

// releaseOf is the release a chisel.yaml is of, ubuntu-26.04 say: the
// version of its ubuntu archive, or of every archive when they agree.
func releaseOf(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var c struct {
		Archives map[string]struct {
			Version string `yaml:"version"`
		} `yaml:"archives"`
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("chisel.yaml: %w", err)
	}
	if a, ok := c.Archives["ubuntu"]; ok && a.Version != "" {
		return "ubuntu-" + a.Version, nil
	}
	version := ""
	for _, a := range c.Archives {
		if a.Version == "" || (version != "" && a.Version != version) {
			return "", errors.New("chisel.yaml names no single ubuntu version")
		}
		version = a.Version
	}
	if version == "" {
		return "", errors.New("chisel.yaml names no ubuntu version")
	}
	return "ubuntu-" + version, nil
}
