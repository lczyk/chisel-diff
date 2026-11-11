// Package cli parses arguments, runs the comparison and renders the report.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	goflags "github.com/jessevdk/go-flags"
	ver "github.com/lczyk/version/go"

	"github.com/lczyk/chisel-coverage/pkg/cache"
	"github.com/lczyk/chisel-coverage/pkg/cut"
	"github.com/lczyk/chisel-coverage/pkg/packageslicesfile"
	"github.com/lczyk/chisel-coverage/pkg/pool"

	chiseldiff "github.com/lczyk/chisel-diff"
	"github.com/lczyk/chisel-diff/internal/changes"
	"github.com/lczyk/chisel-diff/internal/diff"
	"github.com/lczyk/chisel-diff/internal/sides"
)

type flags struct {
	Release     string   `long:"release" value-name:"<dir|name>" description:"chisel-releases checkout, or a release name like ubuntu-26.04 whose branch of canonical/chisel-releases is fetched (default: .)"`
	MergeBase   bool     `long:"merge-base" description:"compare from the merge base of <a> with HEAD; with no <a>, of the release branch chisel.yaml names"`
	Cached      bool     `long:"cached" description:"with a single <a>: compare to the index, not the working tree"`
	Staged      bool     `long:"staged" description:"the same as --cached"`
	NoIndex     bool     `long:"no-index" description:"compare two release dirs, without git"`
	XRelease    bool     `short:"x" long:"x-release" description:"allow a comparison across ubuntu releases, which cuts only the packages or --slice named"`
	Arches      []string `long:"arch" value-name:"<a>[,<a>...]" description:"architectures to compare; \"all\" for every one (default: host)"`
	Slices      []string `long:"slice" value-name:"<s>[,<s>...]" description:"these slices, bare or <package>_<slice>, changed or not; \"all\" for every slice; <a>:<b> slice a at the base against slice b at the head (default: removed, added and changed)"`
	FailMissing bool     `long:"fail-missing" description:"exit 1 when a slice lost an entry or went away"`
	NameStatus  bool     `long:"name-status" description:"name the slices and their states, cut nothing"`
	NoGroup     bool     `long:"no-group" description:"one block per architecture with its real paths"`
	Tree        bool     `short:"T" long:"tree" description:"draw each block's entries as a tree of their paths"`
	Packages    string   `short:"P" long:"packages" optional:"yes" optional-value:"packages" choice:"packages" choice:"slices" description:"end each entry with the packages it comes from, as chisel's manifest says; =slices for the slices"`
	JSON        bool     `long:"json" description:"machine-readable output, folded"`
	Keep        bool     `long:"keep" description:"keep the snapshots and rootfs dirs"`
	Jobs        int      `short:"j" long:"jobs" value-name:"<n>" default:"-1" description:"cuts to run at once; -1 for one per cpu"`
	Verbose     []bool   `short:"v" long:"verbose" description:"one line per cut on stderr; twice also streams chisel's output"`
	Version     bool     `long:"version" description:"print the version and exit"`

	help   bool // -h or --help was given
	noRevs bool // "--" came before any positional, so there are no revisions
}

// Main runs the CLI and returns a process exit code: 0 the comparison was
// made, 1 with --fail-missing and something missing, 2 the comparison could
// not be made, 130 interrupted.
func Main(args []string) int {
	switch first(args) {
	case "version":
		args = []string{"--version"}
	case "help":
		args = []string{"--help"}
	}
	f, rest, err := parseArgs(args, os.Stdout)
	if err != nil {
		errf(err)
		return 2
	}
	if f.help {
		return 0
	}
	if f.Version {
		fmt.Println(ver.Read(fileVersion(chiseldiff.VersionFile)))
		return 0
	}
	spec, pkgs, err := f.spec(rest)
	if err != nil {
		errf(err)
		return 2
	}
	arches, err := pool.ResolveArches(f.Arches)
	if err != nil {
		errf(err)
		return 2
	}
	if f.Jobs == 0 || f.Jobs < -1 {
		errf(fmt.Errorf("--jobs must be at least 1, or -1 for one per cpu"))
		return 2
	}

	// On the first interrupt the cuts stop and the temp dirs go; a second
	// one ends the process at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	code, err := run(ctx, f, spec, pkgs, arches, os.Stdout)
	if ctx.Err() != nil {
		errf(errors.New("interrupted"))
		return 130
	}
	if err != nil {
		errf(err)
		return 2
	}
	return code
}

// run makes the comparison and renders it on out. The code is 1 with
// --fail-missing and something missing, 2 when a cut failed or the archive
// moved, which is said on stderr once the report is out.
func run(ctx context.Context, f flags, spec sides.Spec, pkgs, arches []string, out io.Writer) (int, error) {
	runner := &cut.Runner{Bin: cut.Binary()}
	if len(f.Verbose) >= 2 {
		runner.Verbose = os.Stderr
	}
	s, err := sides.Resolve(ctx, spec)
	if err != nil {
		return 0, err
	}
	if f.Keep {
		notef("snapshots: %s", s.Keep())
	} else {
		defer s.Remove()
	}
	// Refused before the notices, which would only be about how the sides
	// were found.
	cross := s.Base.Release != "" && s.Head.Release != "" && s.Base.Release != s.Head.Release
	if cross && !f.XRelease {
		return 0, fmt.Errorf("the base is %s and the head %s: -x / --x-release compares across releases", s.Base.Release, s.Head.Release)
	}
	if cross && len(pkgs) == 0 && len(f.Slices) == 0 {
		return 0, errors.New("a comparison across releases cuts only what is named: give packages, --slice=<s>, or --slice=all for every slice")
	}
	for _, n := range s.Notices {
		notef("%s", n)
	}
	for _, w := range s.Warnings {
		notef("warning: %s", w)
	}
	// Chisel reads and checks a whole release for a find that matches
	// nothing, without the network.
	for name, dir := range map[string]string{"base": s.Base.Dir, "head": s.Head.Dir} {
		if err := runner.Fetch(ctx, dir); err != nil {
			return 0, fmt.Errorf("the %s is not a release chisel accepts: %s", name, chiselSays(err))
		}
	}
	base, err := changes.Load(s.Base.Dir)
	if err != nil {
		return 0, fmt.Errorf("base: %w", err)
	}
	head, err := changes.Load(s.Head.Dir)
	if err != nil {
		return 0, fmt.Errorf("head: %w", err)
	}
	var picks []string
	if f.Slices != nil {
		picks = f.Slices
	}
	considered := pkgs
	if cross && len(pkgs) == 0 {
		// Across releases nearly every file differs, so what is picked is
		// looked for in every package.
		considered = changes.Packages(base, head)
	}
	res, err := changes.Compare(base, head, considered, picks)
	if err != nil {
		return 0, err
	}
	if cross {
		// Only what is named is cut, not the slices prefer would add.
		res.Prefer = nil
		n := 0
		for _, sl := range res.Reported() {
			if sl.CutBase() || sl.CutHead() {
				n++
			}
		}
		notef("%s against %s: %d slices to cut, for %s", s.Base.Release, s.Head.Release, n, strings.Join(arches, ", "))
	} else if res.ChiselYAMLDiffers {
		if len(res.Packages) == 0 {
			notef("chisel.yaml differs and no package was named, so nothing is compared")
		} else {
			notef("chisel.yaml differs, so every slice of the packages considered counts as changed")
		}
	}
	if f.NameStatus {
		if f.JSON {
			var named []diff.Slice
			for _, sl := range res.Reported() {
				named = append(named, diff.Slice{Slice: sl})
			}
			return 0, renderJSON(out, runInfo(runner.Bin, spec.Release, spec, s, res), res, named, !f.NoGroup, false)
		}
		for _, sl := range res.Reported() {
			fmt.Fprintf(out, "%s: %s\n", sl.Shown(), sl.State)
		}
		return 0, nil
	}
	opts := diff.Options{
		Base:       s.Base.Dir,
		Head:       s.Head.Dir,
		Arches:     arches,
		OneArchive: !res.ChiselYAMLDiffers,
		Origins:    f.Packages != "",
		Keep:       f.Keep,
		Jobs:       f.Jobs,
		Cache:      cache.Dir(),
		Runner:     runner,
		Log:        os.Stderr,
	}
	if len(f.Verbose) >= 1 {
		opts.Verbose = os.Stderr
	}
	result, err := diff.Run(ctx, opts, res, res.ArchiveDiffers)
	if err != nil {
		return 0, err
	}
	if f.JSON {
		if err := renderJSON(out, runInfo(runner.Bin, spec.Release, spec, s, res), res, result.Slices, !f.NoGroup, f.Packages != ""); err != nil {
			return 0, err
		}
	} else {
		renderText(out, result, !f.NoGroup, f.Tree, f.Packages, styleFor(out))
	}
	if len(f.Verbose) >= 1 {
		notef("%s", summary(result, styleFor(os.Stderr)))
	}
	for _, err := range result.Errors {
		errf(err)
	}
	switch {
	case len(result.Errors) > 0:
		return 2, nil
	case f.FailMissing && result.Missing():
		return 1, nil
	}
	return 0, nil
}

// spec reads the positionals: the revisions, or two directories with
// --no-index, and then the packages.
func (f flags) spec(rest []string) (sides.Spec, []string, error) {
	spec := sides.Spec{Release: f.Release, MergeBase: f.MergeBase, Cached: f.Cached || f.Staged, NoIndex: f.NoIndex, Git: gitBinary()}
	if f.Tree && (f.JSON || f.NameStatus) {
		return spec, nil, errors.New("--tree draws the text report, so it does not go with --json or --name-status")
	}
	if f.NameStatus && f.Packages != "" {
		return spec, nil, errors.New("--name-status cuts nothing, so there is nothing to say --packages of")
	}
	if f.NameStatus && f.FailMissing {
		return spec, nil, errors.New("--name-status cuts nothing, so it cannot be a check: drop --fail-missing")
	}
	if f.NoIndex {
		if f.Release != "" {
			return spec, nil, errors.New("--no-index takes two directories and no --release")
		}
		if len(rest) < 2 {
			return spec, nil, errors.New("--no-index takes two release directories")
		}
		spec.Dirs = [2]string{rest[0], rest[1]}
		rest = rest[2:]
	} else {
		if spec.Release == "" {
			spec.Release = "."
		}
		spec.Upstream, spec.Cache = sides.Upstream, sides.CacheDir()
		// The first positional is the revisions, unless "--" came first or
		// it is a package by the path of its file.
		if len(rest) > 0 && !f.noRevs && !strings.HasSuffix(rest[0], ".yaml") {
			spec.Revisions = rest[0]
			rest = rest[1:]
		}
	}
	spec.Unrelated = f.XRelease
	pkgs := make([]string, len(rest))
	for i, arg := range rest {
		pkgs[i] = packageslicesfile.Package(arg)
	}
	return spec, pkgs, nil
}

// chiselSays is what chisel said of a release it refused: its error lines,
// without the temp dir and the query it was run with.
func chiselSays(err error) string {
	var ce *cut.Error
	if !errors.As(err, &ce) {
		return err.Error()
	}
	var said []string
	for _, line := range strings.Split(ce.Output, "\n") {
		if msg, ok := strings.CutPrefix(strings.TrimSpace(line), "error: "); ok {
			said = append(said, msg)
		}
	}
	if len(said) == 0 {
		return err.Error()
	}
	return strings.Join(said, "; ")
}

// gitBinary returns the git binary to run: $GIT, else "git" on PATH.
func gitBinary() string {
	if b := os.Getenv("GIT"); b != "" {
		return b
	}
	return "git"
}

func newParser(f *flags) *goflags.Parser {
	p := goflags.NewNamedParser("chisel-diff", goflags.HelpFlag|goflags.PassDoubleDash)
	p.Usage = "[flags] [<revisions>] [--] [<package>...]\n  chisel-diff [flags] --no-index <dir> <dir> [<package>...]"
	p.LongDescription = strings.TrimSpace(`
git diff for what slices deliver. the slices whose definition changed between
two sides of a chisel-releases checkout are cut from both with the real
chisel, and the paths each lost, gained or changed are reported per
architecture.

the sides are as for git diff: nothing compares the working tree with the
index, <a> compares <a> with the working tree, <a>..<b> the two revisions,
<a>...<b> the merge base of the two with <b>, and --cached the index in place
of the working tree. --merge-base with no revision is what a pull request
would change: the working tree from where HEAD parted from the branch of the
release chisel.yaml is of, ubuntu-26.04 say, found as a local branch, else on
the remote for canonical/chisel-releases, else fetched.

--release=<name> takes that branch of canonical/chisel-releases instead of a
checkout, HEAD being its tip: HEAD~1..HEAD is the last change merged. a
comparison across ubuntu releases, by their chisel.yaml, needs -x.

a package may be given as the path of its file, slices/<package>.yaml, and
without any every package whose file differs is looked at. "--" before the
packages says there are no revisions. the "chisel" binary is found on PATH,
or set CHISEL=/path/to/chisel, and "git" likewise with GIT. "version" and
"help" as the first argument are --version and --help.`)
	if _, err := p.AddGroup("flags", "", f); err != nil {
		panic(err)
	}
	return p
}

// parseArgs parses the command line. List flags take comma-separated values
// and may repeat; -h, --help and --version work wherever they land on the
// line. Help is not an error: the usage goes to help, f.help is set and the
// caller stops. The usage is written by the parser that ran, since go-flags
// only renders defaults once it has parsed.
func parseArgs(args []string, help io.Writer) (f flags, rest []string, err error) {
	p := newParser(&f)
	rest, err = p.ParseArgs(args)
	if err != nil {
		var fe *goflags.Error
		if errors.As(err, &fe) && fe.Type == goflags.ErrHelp {
			p.WriteHelp(help)
			f.help = true
			return f, nil, nil
		}
		return f, nil, err
	}
	for _, l := range []*[]string{&f.Arches, &f.Slices} {
		*l = splitLists(*l)
	}
	if dash := slices.Index(args, "--"); dash >= 0 {
		f.noRevs = len(rest) == len(args)-dash-1
	}
	return f, rest, nil
}

// splitLists splits every value on commas and drops the empty parts, nil
// when no value was given.
func splitLists(values []string) []string {
	if values == nil {
		return nil
	}
	out := []string{}
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// fileVersion returns the version line of a VERSION file: the first line that
// is neither blank nor a # comment.
func fileVersion(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

func errf(err error) {
	fmt.Fprintf(os.Stderr, "chisel-diff: %v\n", err)
}

// notef writes a notice on stderr, stamped like the lines of a cut.
func notef(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", cut.Stamp(), fmt.Sprintf(format, args...))
}
