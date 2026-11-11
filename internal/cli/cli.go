// Package cli parses arguments, runs the comparison and renders the report.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	goflags "github.com/jessevdk/go-flags"
	ver "github.com/lczyk/version/go"

	"github.com/lczyk/chisel-coverage/pkg/cut"
	"github.com/lczyk/chisel-coverage/pkg/packageslicesfile"
	"github.com/lczyk/chisel-coverage/pkg/pool"

	chiseldiff "github.com/lczyk/chisel-diff"
	"github.com/lczyk/chisel-diff/internal/changes"
	"github.com/lczyk/chisel-diff/internal/sides"
)

type flags struct {
	Release     string   `long:"release" value-name:"<dir>" description:"chisel-releases checkout (default: .)"`
	MergeBase   bool     `long:"merge-base" description:"with a single <a>: compare from its merge base with HEAD"`
	Cached      bool     `long:"cached" description:"with a single <a>: compare to the index, not the working tree"`
	Staged      bool     `long:"staged" description:"the same as --cached"`
	NoIndex     bool     `long:"no-index" description:"compare two release dirs, without git"`
	Arches      []string `long:"arch" value-name:"<a>[,<a>...]" description:"architectures to compare; \"all\" for every one (default: host)"`
	Slices      []string `long:"slice" value-name:"<s>[,<s>...]" description:"these slices, bare or <package>_<slice>, changed or not; \"all\" for every slice (default: removed, added and changed)"`
	FailMissing bool     `long:"fail-missing" description:"exit 1 when a slice lost an entry or went away"`
	NameStatus  bool     `long:"name-status" description:"name the slices and their states, cut nothing"`
	NoGroup     bool     `long:"no-group" description:"one block per architecture with its real paths"`
	JSON        bool     `long:"json" description:"machine-readable output, folded"`
	Keep        bool     `long:"keep" description:"keep the snapshots and rootfs dirs"`
	Jobs        int      `short:"j" long:"jobs" value-name:"<n>" default:"-1" description:"cuts to run at once; -1 for one per cpu"`
	Verbose     []bool   `short:"v" long:"verbose" description:"one line per cut on stderr; twice also streams chisel's output"`
	Version     bool     `long:"version" description:"print the version and exit"`

	help bool // -h or --help was given
}

// Main runs the CLI and returns a process exit code: 0 the comparison was
// made, 1 with --fail-missing and something missing, 2 the comparison could
// not be made, 130 interrupted.
func Main(args []string) int {
	if len(args) == 0 {
		_, _, _ = parseArgs([]string{"--help"}, os.Stderr)
		return 2
	}
	switch args[0] {
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
	if _, err := pool.ResolveArches(f.Arches); err != nil {
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
	code, err := run(ctx, f, spec, pkgs, os.Stdout)
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

// run makes the comparison and renders it on out.
func run(ctx context.Context, f flags, spec sides.Spec, pkgs []string, out io.Writer) (int, error) {
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
	for _, n := range s.Notices {
		notef("%s", n)
	}
	// Chisel reads and checks a whole release for a find that matches
	// nothing, without the network.
	for name, dir := range map[string]string{"base": s.Base.Dir, "head": s.Head.Dir} {
		if err := runner.Fetch(ctx, dir); err != nil {
			return 0, fmt.Errorf("the %s is not a release chisel accepts: %w", name, err)
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
	res, err := changes.Compare(base, head, pkgs, picks)
	if err != nil {
		return 0, err
	}
	if res.ChiselYAMLDiffers {
		if len(res.Packages) == 0 {
			notef("chisel.yaml differs and no package was named, so nothing is compared")
		} else {
			notef("chisel.yaml differs, so every slice of the packages considered counts as changed")
		}
	}
	if f.NameStatus {
		for _, sl := range res.Reported() {
			fmt.Fprintf(out, "%s %s: %s\n", sl.Package, sl.Name, sl.State)
		}
		return 0, nil
	}
	return 0, errors.New("cutting is not built yet, only --name-status is")
}

// spec reads the positionals: the revisions, or two directories with
// --no-index, and then the packages.
func (f flags) spec(rest []string) (sides.Spec, []string, error) {
	spec := sides.Spec{Release: f.Release, MergeBase: f.MergeBase, Cached: f.Cached || f.Staged, NoIndex: f.NoIndex, Git: gitBinary()}
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
		if len(rest) == 0 {
			return spec, nil, errors.New("no revisions given: <a>, <a>..<b> or <a>...<b>, as for git diff")
		}
		if spec.Release == "" {
			spec.Release = "."
		}
		spec.Revisions = rest[0]
		rest = rest[1:]
	}
	pkgs := make([]string, len(rest))
	for i, arg := range rest {
		pkgs[i] = packageslicesfile.Package(arg)
	}
	return spec, pkgs, nil
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
	p.Usage = "[flags] <revisions> [<package>...]\n  chisel-diff [flags] --no-index <dir> <dir> [<package>...]"
	p.LongDescription = strings.TrimSpace(`
git diff for what slices deliver. the slices whose definition changed between
two revisions of a chisel-releases checkout are cut from both with the real
chisel, and the paths each lost, gained or changed are reported per
architecture.

<revisions> is as for git diff: <a> compares <a> with the working tree,
<a>..<b> the two revisions, <a>...<b> the merge base of the two with <b>. a
package may be given as the path of its file, slices/<package>.yaml, and
without any every package whose file differs is looked at. the "chisel" binary
is found on PATH, or set CHISEL=/path/to/chisel, and "git" likewise with GIT.
"version" and "help" as the first argument are --version and --help.`)
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
