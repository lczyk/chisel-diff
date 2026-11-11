# chisel-diff: design

a development tool for [chisel-releases](https://github.com/canonical/chisel-releases) work. for two revisions of a release, it reports which paths each changed slice lost, gained or changed, per arch. it is `git diff` for what slices deliver instead of for what the yaml says. `--fail-missing` turns it into a check: exit 1 when a slice no longer delivers a path it delivered at the base.

this file is a throwaway working note for the first implementation, written before any code. `design-pkg.md` beside it covers the change it needs in chisel-coverage's library. `spike/` holds the experiments the numbers here come from, and `reviews/` what a second reader made of earlier drafts.

## why

published slices are append-only. removing a slice already fails ci (`removed-slices`). dropping a path from a kept slice has no check, and a yaml diff cannot decide it:

- a path that moves into a new slice the old one depends on is still delivered. `52181c6e` on ubuntu-24.04 took 24 paths out of `coreutils_bins` and lost nothing.
- a glob that replaces explicit paths, or an `arch:` list that changes, moves paths the yaml does not spell out.
- an essential that goes takes every path it brought.

so the question is asked of chisel: cut the slice from both revisions and compare the two rootfs trees. the `_try-cut.py --base` helper of the slicing skills does this for one arch and prints yaml. chisel-diff is that as a tool, on every arch, folded, with an exit code.

## principle

git says what the two revisions are, chisel says what a slice delivers. the tool never reads an archive, extracts a `.deb`, matches a glob or resolves an essential. every listing is a real `chisel cut` of a slice against a revision of the release as it is, essentials, mutate scripts and generated content included, so a listing is what a user of that slice gets. the tool's own work is choosing what to cut, set difference and folding.

this is where it parts from chisel-coverage and chisel-file. those cut stripped slices of a throwaway one-package release, since they ask what a slice claims from its own deb. chisel-diff asks what a cut of the slice produces, which is the thing append-only protects.

what is compared is the rootfs as `lstat` sees it. an entry is its absolute path, with no trailing slash, and has a kind (file, directory, symlink, other), for a regular file its mode (the `07777` bits, setuid, setgid and sticky included) and for a symlink its target as written. content, ownership, timestamps, directory modes and symlink modes are outside. a path is not resolved through symlinks: the report is of physical entries, not of what a path leads to.

## how it works

1. resolve the two sides in the `--release` checkout (see "sides").
2. snapshot each side into a release dir of its own and have chisel validate it.
3. find what to compare (see "what is compared").
4. cut. one unit per (side, package, slice, arch): `chisel cut --release <side> --root <tmp> --arch <a> --ignore unmaintained --ignore unstable <package>_<slice>`. list the rootfs while it exists, then delete it.
5. per (package, slice, arch): an entry at the base only is `lost`, at the head only `gained`, on both with another kind, mode or target `changed`.
6. fold arches that agree and render.

### sides

the two sides are named the way `git diff` names them, by the first argument, and mean what they mean there. nothing is bent, so nobody has to learn a second set of rules:

given | base | head
---|---|---
nothing | the index | the working tree
`--cached` | `HEAD` | the index
`--merge-base` | the merge base of `HEAD` and the branch of the release `chisel.yaml` is of | the working tree
`A` | `A` | the working tree
`A..B` | `A` | `B`
`A...B` | the merge base of `A` and `B` | `B`
`--merge-base A` | the merge base of `A` and `HEAD` | the working tree
`--cached A` | `A` | the index
`--cached --merge-base A` | the merge base of `A` and `HEAD` | the index
`--no-index <dir> <dir>` | the first dir | the second dir
`--release=<name>`, nothing | an error | an error
`--release=<name> A` | `A` | the branch tip

`--staged` is `--cached`, as in git. `--no-index` takes two release dirs in place of the revisions and runs no git at all, so it works outside a checkout. where it is narrower than git, it is so that the first argument is always the revisions and the exit codes stay the tool's own: `--cached` does not default to `HEAD`, `--no-index` is never implied and takes two directories only, and it does not turn differences into exit 1 as git's does. `--cached` with a range, and `--no-index` with `--cached`, `--merge-base` or `--release`, are usage errors.

an empty side of `..` or `...` is `HEAD`, so `A...` is the merge base of `A` and `HEAD` against `HEAD`, and uncommitted work is not in it. the argument is split on `...` first and then on `..`, which is safe since git allows no `..` in a ref name, and each side is resolved with `git rev-parse --verify <side>^{commit}`, so `HEAD~3`, `@{u}`, a tag or a sha all do. anything else is a usage error: two revisions as two arguments (the second is a package), `^A`, `A^!`, a tree such as `HEAD:slices`, `--merge-base` with a range.

a check of a branch wants the merge base, `A...B` in ci and `--merge-base A` at a desk, so that work landed on `A` since branching is not read as this change undoing it. a bare `A` or `A..B` takes `A` at its tip, which is what two release branches of different ubuntu versions need. when the base is a tip and not an ancestor of the head (of `HEAD`, when the head is the working tree or the index), a notice says so and names the two merge-base forms.

no merge base is an error that names the way out: in a shallow clone, fetch more history (`fetch-depth: 0` in a workflow), else compare the tips with `..`. with `-x` (see "across releases") unrelated histories compare their tips, with a notice, since some release branches have roots of their own: 20.04, 23.10 and 24.04 do, 22.04 to 23.04 share one, and 24.10 on share another. a shallow clone keeps the error with `-x` too, since there the history is missing, not unrelated. tips compared with `..` that share no history get a notice that says so, not one that suggests a merge base. several merge bases are handled as git handles them: `A...B` uses the one git picks, with a notice, and `--merge-base` is an error (`multiple merge bases found`).

a side becomes a dir holding what chisel reads of a release: `chisel.yaml`, `slices/` and, where it exists, `bin-slices/`, which chisel reads for format v3.

- a ref is written out from git objects (`git ls-tree -r` and `git cat-file --batch`), the tree as committed, so `export-ignore` and the like have no say. a symlink or submodule entry in it is an error, and so is an object git cannot give (a partial clone that cannot fetch, a hash staged with `update-index --info-only`): it is never made up from the working tree.
- the working tree is copied once, up front, as chisel would read it: every `.yaml` under the slice dirs, tracked or not, ignored or not, a symlinked file by its bytes, a symlinked directory not entered. a file that cannot be read is an error. the run then has one head however long it takes.
- the index is made into a tree by git and then written out like a ref: `git write-tree`, run on a copy of the index file (`GIT_INDEX_FILE`) and with its new objects going to a temp dir (`GIT_OBJECT_DIRECTORY`, the repo's own objects as `GIT_ALTERNATE_OBJECT_DIRECTORIES`), so the checkout's index and object store are left as they are. both paths are asked of git (`git rev-parse --git-path index`, `--git-path objects`), which is what makes a linked worktree work. one known limit: with a split index git still reads the shared index file in place, touches its timestamp and may run a `post-index-change` hook. it is what is staged, so an untracked file is not in it. git itself leaves out an intent-to-add entry (`git add -N`), whether its file is still in the working tree or not, and refuses an index with unmerged entries, which is an error here too. reading the index with `git ls-files -s` would not do: it shows an intent-to-add entry as an empty blob like any other.
- a `--no-index` dir is copied like the working tree.

a side with no `slices/` gets an empty one, since chisel wants the directory.

#### no revisions

with no revisions it is `git diff`, so nothing surprises: the working tree against the index, what is not staged, or with `--cached` the index against `HEAD`. the working tree is read as chisel reads it, so an untracked package file is in it, where git would leave it out. when the index and the working tree agree a notice names `--cached` and `--merge-base`.

`--merge-base` with no revision answers what a pull request of this checkout would change: the files on disk, staged or not, committed or not, against where `HEAD` parted from the release branch. git wants a commit after `--merge-base`, so this adds a meaning and changes none. that branch is `ubuntu-<version>`, the version of the `ubuntu` archive in the working tree's `chisel.yaml` (or of every archive, when they agree), looked up as a branch, never a tag: the local branch, else the one of the remote whose url is canonical/chisel-releases, else the clone below. a notice says which, and the remote's says when its tip was committed, since nothing fetches it. `--merge-base <ref>` picks another branch. the first positional is the revisions unless `--` comes before it or it ends in `.yaml`, so `chisel-diff slices/openssl.yaml` and `chisel-diff -- openssl` name a package with no revisions.

a local release branch used as a base is checked against the remote's: behind it, ahead of it or parted from it is a warning, since a stale base reads what landed since as part of the change.

#### release names

`--release` is a checkout, or a release name such as `ubuntu-26.04` as chisel and chisel-coverage take it. a name is a branch of canonical/chisel-releases, and a tarball of it, which chisel itself fetches, has no history for revisions. so canonical/chisel-releases is kept as a bare clone of its branches in `$XDG_CACHE_HOME/chisel-diff/chisel-releases.git` (else `~/.cache/...`), cloned once (beside it, then moved in whole) and fetched each run. a fetch that fails leaves it as it was, with a warning. the branch tip is then `HEAD`, and `@` too, and is the head side where a checkout's would be the working tree. `--cached` is an error, and so is a reflog of `HEAD`, which the clone does not keep. with no revisions it is an error, as `git diff` is in a repo with no working tree, and the error names `HEAD~1..HEAD`, the last change merged.

in a checkout, a release name that is no revision of it is looked for in the same order as above, the remote's branch and then the clone. the clone is not fetched into the checkout: git is run with the clone's objects as `GIT_ALTERNATE_OBJECT_DIRECTORIES`, added to any inherited ones, so the checkout gains no ref and no object.

#### across releases

a side's release is the version in its `chisel.yaml`, as above. when both sides have one and they differ, the comparison is across releases, and refused (exit 2) without `-x` / `--x-release`: it is rarely meant, and nearly every package file and `chisel.yaml` differ, so the default would cut most of the release. with `-x`, only what is named is cut: packages, or `--slice` picks looked for in every package on either side (`--slice=all` is the whole release, ~1900 slices and some 15 minutes an arch between 24.04 and 26.04), and neither is an error. the slices cut for `prefer` are left out, and a notice gives the number to cut. `-x` with one release is no change, and a side whose version cannot be read is not checked.

`--release` may be a subdirectory of its git repo, and paths are taken relative to it. each side is validated once with `chisel find --release <dir>` and a query matching nothing, 0.1s and no network. a side that does not validate (an essential naming a deleted slice, a path conflict, a format this chisel does not know) is an error before any cut.

### what is compared

a package is its name, the basename chisel takes it from, wherever under the slice dirs its file sits, so moving a file between directories changes nothing.

packages considered: the ones named on the command line, else every package whose file differs between the two snapshots, byte for byte. git is not asked.

a package may be named by the path of its file as well: an argument ending in `.yaml` is its basename without the suffix, so `slices/openssl.yaml` and `../chisel-releases/slices/openssl.yaml` are both `openssl`, and the shell completes it. the path itself is not opened, since the file may be gone from the working tree and still be at the base.

every slice of those packages has a state:

- `removed` -- at the base only, which includes every slice of a deleted file.
- `added` -- at the head only.
- `changed` -- on both sides with a different definition.
- `same` -- on both sides with the same definition.

by default `removed`, `added` and `changed` slices are reported and `same` ones are not. only `changed` slices are cut, on both sides. a `removed` or `added` slice is named and not cut. a `removed` slice is itself what went missing, so it counts for `--fail-missing`.

an `added` or `removed` slice has nothing to be compared with, and its listing would be its whole closure, libc and all. the pipeline spike printed 1100 lines for the 24 new sub-slices of coreutils and spent 189 of its 224 cuts on them. what a new slice delivers is chisel-file's question.

a definition is the decoded yaml value of the slice's node, so comments, key order and flow style do not make a slice `changed`. a slice on both sides is also `changed` when any key of its file other than `slices` differs (a package-level `essential`, `archive`).

one kind of `same` slice is cut all the same: a slice on both sides that lists a path its package gives away with `prefer`. chisel keeps `prefer` per package and path, not per slice, so that is every slice of the package that lists the path, whichever of them spells the `prefer` and on whichever side, in any package of the release, considered or not. these slices are cut on both sides whenever the run cuts anything else, in addition to what `--slice` picks, and are treated like `changed` ones from there, so they print nothing unless they differ. a run that would cut nothing else does not cut them either. the next section says why. that is one or two slices of a release today.

`--slice` picks slices by name instead of by state, and what it picks is cut:

given | reported
---|---
nothing | the `removed`, `added` and `changed` slices of the packages considered
`--slice <name>` | slices of that name in the packages considered, whatever their state
`--slice <package>_<name>` | that slice, and the package joins those considered
`--slice all` | every slice of the packages considered, whatever its state
`--slice <a>:<b>` | slice `a` at the base against slice `b` at the head, in every package considered that has both
`--slice <package>_<a>:<b>` | the pair in that package, which joins those considered
`--slice <package>_<a>:<other>_<b>` | a pair across two packages

values add up, and `all` beside others is `all`. every name given must match a slice on some side, else it is an error, as is a package named on the command line with no file on either side.

a pair asks what one slice delivers that another does not, a slimmer `awt-v2` against the `awt` it is to replace say, where the sides alone would only call the new slice `added`. `:` is git's refspec shape, `<src>:<dst>`, and can be in no package or slice name. a bare base slice pairs with a bare head slice and a full one keeps its package for a bare head slice. a pair is reported as `<package>_<a>:<b>` (`<package>_<a>:<other>_<b>` across packages), counts for `--fail-missing` like any slice, so `--slice=awt:awt-v2 --fail-missing` is the check that `awt-v2` drops nothing `awt` delivers, and its json slice carries `head-package` and `head-slice`. a full pair whose slice is not on its side is an error, and so is a bare pair that no package considered has. the revisions still say what the sides are: with none, `awt` in the index against `awt-v2` on disk. a picked `same` or `changed` slice is cut on both sides. a picked `added` or `removed` one is cut on the side that has it and listed, every entry gained or lost.

when `chisel.yaml` differs between the sides every slice that is on both sides is `changed`, with a notice. the packages considered stay the same, so a change to `chisel.yaml` alone compares nothing unless packages are named, and the notice says that too. that is exit 0, with `--fail-missing` as well: the tool compares slices, and what a new `chisel.yaml` does to the release is not its question. cutting the whole release is hours.

`--name-status` stops here. it prints one line per slice reported, `<package>_<slice>: <state>`, and cuts nothing, so it needs no network and takes as long as the two snapshots and their validation. `--json` then holds the same slices with no cuts and no findings. with `--fail-missing` it is a usage error, since a check that cut nothing has not looked.

### why changed slices are enough, and where that stops

take a slice S whose definition is the same on both sides and which loses path p. at the base p came from some slice u that S reaches through essentials. walk that chain from S. either u's definition changed and u is still reached, and then u lost p as well, or some slice w on the chain dropped the essential that led to u, and then w's definition changed and w lost p as well. so a loss in a `same` slice is also a loss in a `changed` one, and that one is reported.

the argument holds as long as a cut is the union of what its slices declare: more slices selected never take a path away, and what one slice puts at a path does not depend on the others. two things in chisel bend that.

- `prefer`, which lets what else is selected decide which package provides a path. with `until: mutate` on the preferred side a path goes from a `same` slice while no `changed` one loses anything. `spike/prefer` builds this and chisel does it (canonical/chisel#354). as chisel is today a path is only given away through `prefer`, which chisel keeps per package and path. so the slice the path came from at the base is one that lists a path its package gives away, whichever slice of that package spells the `prefer`. those slices are cut too, without reading essentials, and the code says why at the place it picks them. this finds the loss when the slice that lists the path is the one that reaches the preferred package. it does not find it in a `same` slice that only reaches the listing slice through an essential and gets the preferred package through another: that slice loses the path and nothing that is cut does. `spike/prefer-more` has both shapes. the second one stays open until chisel fixes #354, or until essentials are read here (`--dependents`).
- a symlink that replaces a directory. entries under it are lost from every slice that put something there, `changed` or not. this one stays open.

so the default answers "did the slices this change touched lose anything", and `--slice` widens it by hand. naming the dependents of a changed slice automatically would mean reading essentials out of the yaml in all their forms (lists, maps with `arch`, package-level, `v3-essential`), and a change to `libc6_libs` would then cut the release. that is under "later, maybe".

### cut outcomes

per (slice, arch) each side's cut ends in one of three ways: it cut, the slice is `not available` (chisel says `cannot find package "<p>" in archive`, or `in archive(s)`, for the slice's own package or one it reaches), or it failed.

base | head | outcome
---|---|---
cut | cut | compared
not available | not available | `not available`, reported once per package when it is the package's own absence
not available | cut | `base not available`. nothing to compare, nothing lost
cut | not available | `head not available`. the slice no longer cuts on this arch, which counts for `--fail-missing`
failed with `no content` | any | the base is pruned and cut again, see below
failed | any | an error, exit 2
any | failed | an error, exit 2

chisel skips an archive it has no credentials for (`Archive "<name>" ignored: credentials not found`) and then does not find what only that archive has. that is still `not available`, on both sides alike, and the line names the archives chisel skipped so the reader sees why nothing was compared.

an error prints the command line and chisel's output on stderr, as the sister tools do. there are no retries: a fetch that times out is an error like any other, and the run is to be repeated.

the first unit of a (side, package, arch) runs before that side's others for the pair, which are skipped when it finds the package itself missing. one side never decides for the other.

#### a base the archive has moved under

a slice that names a path its package no longer ships does not cut (`no content at <path>`, or the list form). the PR that fixes it by deleting the path is routine: five of the 45 single-file commits looked at for the spike were that. calling the base uncuttable would leave exactly those PRs unchecked, and a second deletion could ride along unseen.

so the base is pruned and cut again. chisel names a package and the sources it found nothing for. in a copy of the base release made for that unit, every `contents` entry of that package whose source is one of them goes: the entry's key, or its `copy:` value when it has one, compared as written, a glob as the pattern it is. the unit is then cut again into an empty root. each round must delete at least one entry, so it ends: when the cut passes, or with an error when chisel names something no entry has as its source.

only chisel's two plain forms are read this way, `no content at <path>` and `no content at:` with its list. `cannot create hard link <link>: no content at <target>` is a failure.

what is compared is the base as today's archive can deliver it. each entry deleted is reported as `no content`, neither lost nor gained, and does not count for `--fail-missing`. a head that still names the path fails by itself.

### one archive

a difference is down to the revisions only when both sides saw the same archive. chisel has no way to pin one, so the run watches for movement instead. every cut logs the debs it fetched, cached or not (`Fetching pool/.../<name>_<version>_<arch>.deb`), and for one deb name on one arch the run expects one filename:

- across all units of a side, else the archive moved during the run.
- across the sides too, unless `chisel.yaml` differs or the `archive:` key of that package's file does, where another version is the point.

a filename that moved is an error, exit 2, naming the deb and both versions. where the versions differ on purpose the header shows both. this is a tripwire, not a proof: a pool filename has no epoch, no archive and no digest in it.

### directories

every entry of the rootfs counts, as in chisel-coverage. a directory is shown with a trailing slash, which is rendering only: entries are matched by path, so a directory that becomes a symlink is one `changed` entry, and what was under it is lost.

the verdict and the json hold every entry. the text report leaves out a directory when something under it is reported in the same state, so losing `/usr/lib/foo/bar.so` and with it the now empty `/usr/lib/foo/` is one line, and losing an empty `/var/lib/foo/` that the slice used to make is one line too.

### modes

a regular file's mode is compared, a directory's is not, since a generated directory's mode differs from cut to cut (canonical/chisel#353). chisel makes generated content in map order, and a `make: true` directory with a mode of its own ends up with that mode or with 0755 depending on whether one of its children was made first. `spike/modes` cut one such slice twelve times and got 0700 six times and 0755 six times. a real slice does it too: `python3.14-minimal_bins` on 26.04 declares `/usr/local/lib/python3.14/` and `dist-packages/` under it, both `{make: true, mode: 02775}`, and in ten cuts the permission bits of the parent were 755 twice and 775 eight times. until that is fixed in chisel a directory's mode says nothing about the revision.

`changed` entries are reported and never count for `--fail-missing`.

### folding

`chisel-coverage/pkg/fold` with the state as what must agree: every finding is an item with `Key` the normalised path, `Raw` the real one, and `Tag` the state, for a `changed` entry with the two descriptions in it (symlink targets normalised like paths, raw ones in `RawTag`). each set of arches that shares findings is a block, most arches first. a block of one arch shows its real paths. `--no-group` makes every arch its own block with real paths. unlike the sister tools there is no special case for an `arch: all` package, since a real cut holds paths of other debs too.

## scheduling

a real cut costs more than a stripped one. on a warm cache it averaged 2.1s over 224 cuts, nearly all of it chisel fetching `InRelease` for every suite, and it extracts every deb the slice reaches (18 for `dpkg_bins` on 26.04).

so the tool cuts little and in parallel:

- unless `--slice` picks more, only `changed` slices are cut, and with them the one or two slices that list a path given away with `prefer`. on real history that is a handful per PR.
- the worker pool, the private cache views that keep concurrent chisels apart (canonical/chisel#340) and the verbose lines are chisel-coverage's, moved into `pkg/pool` so that a unit names its own release dir. see `design-pkg.md`.
- both sides go through one pool, so a view is seeded once for the run.
- the units of the lead arch run first and alone, then the rest, as in chisel-coverage. the lead pass leaves every `arch: all` deb in the cache.

sigint or sigterm stops the run as in the sister tools: no new cut starts, running chisels get sigint, temp dirs and views go, exit 130.

## cli

```
chisel-diff [flags] [<revisions>] [--] [<package>...]
chisel-diff [flags] --no-index <dir> <dir> [<package>...]
  <revisions>                  <a> for <a> against the working tree, <a>..<b>,
                               or <a>...<b> for the merge base of the two
                               against <b>, as in git diff (default: from
                               the merge base with the release branch)
  <package>                    a package, by name or by the path of its file
                               (default: every package whose file differs)
      --release=<dir|name>     chisel-releases checkout, or a release name like
                               ubuntu-26.04 (default: .)
      --merge-base             with a single <a>: compare from its merge base
                               with HEAD
      --cached, --staged       with a single <a>: compare to the index, not
                               the working tree
      --no-index               compare two release dirs, without git
  -x, --x-release              allow a comparison across ubuntu releases,
                               which cuts only the packages or --slice named
      --arch=<a>[,<a>...]      architectures to compare; "all" for every one
                               (default: host)
      --slice=<s>[,<s>...]     these slices, bare or <package>_<slice>,
                               changed or not; "all" for every slice;
                               <a>:<b> slice a at the base against slice b
                               at the head (default: removed, added and
                               changed)
      --fail-missing           exit 1 when a slice lost an entry or went away
      --name-status            name the slices and their states, cut nothing
      --no-group               one block per architecture with its real paths
      --json                   machine-readable output, folded
      --keep                   keep the snapshots and rootfs dirs
  -j, --jobs=<n>               cuts to run at once; -1 for one per cpu
                               (default: -1)
  -v, --verbose                one line per cut on stderr; twice also streams
                               chisel's output
      --version                print the version and exit
  -h, --help                   this text
```

flags are parsed by go-flags as in the sister tools, list flags split on commas and repeat, `version` and `help` as the first argument are aliases. `chisel` is `$CHISEL` or on `PATH`, `git` is `$GIT` or on `PATH`.

the first argument is the revisions, unless `--` comes first or it ends in `.yaml`, so a package is never taken for a revision or the other way round. with none, the base is where `HEAD` parted from the release branch (see "no revisions"), never `HEAD` itself: a check that lost its base and compared the head with itself would pass without having looked. `chisel-diff HEAD` is what the uncommitted edits do.

exit codes:

- 0 -- the comparison was made, whatever it found.
- 1 -- only with `--fail-missing`: an entry is `lost`, a slice is `removed`, or a slice is `head not available` on an arch.
- 2 -- the comparison could not be made: usage, not a git checkout (without `--no-index`), an unknown ref, no merge base, a side that does not validate, an unknown package or slice, io, a cut that failed on either side, an archive that moved. the report is still rendered with what was found, in text and in json, and 2 wins over 1.
- 130 -- interrupted.

notices go to stderr: `chisel.yaml` differs, the revision the base was resolved to when it is a merge base, a second merge base that was not used, tips compared with a base that is not an ancestor of the head or shares no history with it, where a release branch was found, and the number of slices to cut across releases. warnings, for what looks like a mistake and still runs, go there too, as `warning: ...`: a stale local release branch, a release name with no revisions, a clone that could not be fetched.

## report

text, to stdout, nothing indented and no blank lines. one block per (package, slice, group of arches). a `changed` slice with no finding prints nothing.

```
coreutils_bins 9.4-3.1ubuntu1 [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/usr/bin/basename
+/usr/libexec/coreutils/
~/usr/bin/md5sum: file 0755 -> symlink to ../libexec/coreutils/md5sum
~/usr/bin/su: file 4755 -> file 0755
~/var/run/: dir -> symlink to /run
systemd_bins 255.4-1ubuntu8.11 [armhf]
+/usr/lib/<arch>-linux-<gnu>/cryptsetup/libcryptsetup-token-systemd-tpm2.so
+/usr/lib/systemd/systemd-measure
apparmor_profiles 4.0.1really4.0.1-0ubuntu0.24.04.4 [amd64, arm64]
!/etc/apparmor.d/busybox: no content
dpkg_divert: added
openssl_scripts: removed
ruby3.3_libs 3.3.8-2ubuntu3.1 [i386]: head not available: libfoo
bash [riscv64]: not available
linux-firmware [amd64, arm64]: not available (skipped archives: ubuntu-esm-infra)
```

a line is `-<path>` lost, `+<path>` gained, `~<path>: <base> -> <head>` changed or `!<path>: no content` deleted from the base, in that order, each sorted by path, so `grep '^-'` is the list of what went missing. an entry is described as `file <mode>`, `dir`, `symlink to <target>`, or `other`. a slice is named as chisel names it, `<package>_<slice>`, so a name can be pasted into `--slice` or `chisel cut`. the header is `<package>_<slice> <version> [arches]`, with both versions as `<base> -> <head>` where they differ on purpose, and a state after a colon for a block with no lines of its own. an `added` or `removed` slice is one line with no version and no arches, since it was not cut, unless `--slice` forced it, and then it is a block like any other. colour as in chisel-coverage, only on a terminal with `NO_COLOR` unset: red `-`, green `+`, yellow `~` and `!`.

`-v` ends with the summary line of the pool and one more: `12 slices compared, 3 differ, 5 lost, 40 gained, 2 changed`. slices are counted once each whatever the number of arches, one that differs on any arch differs, and entries are counted per arch as the text report shows them, so a directory left out there is not counted.

`--json` is the same folding:

```json
{"run": {"chisel": "v1.2.0", "release": ".", "chisel-yaml-differs": false,
   "revisions": "canonical/ubuntu-24.04", "merge-base": true,
   "base": {"ref": "canonical/ubuntu-24.04", "tip": "<sha>", "rev": "<sha>"},
   "head": {"kind": "worktree", "ref": null, "tip": "<sha of HEAD>", "rev": null}},
 "packages": [{"name": "coreutils", "file": "modified",
   "slices": [{"name": "bins", "state": "changed", "forced": false,
     "cuts": [{"arch": "amd64",
       "base": {"status": "cut", "entries": 181, "version": "9.4-3.1ubuntu1", "pruned": [], "skipped-archives": [], "error": null},
       "head": {"status": "cut", "entries": 181, "version": "9.4-3.1ubuntu1", "skipped-archives": [], "error": null}}],
     "findings": [
       {"path": "/usr/bin/basename", "change": "lost", "arches": ["amd64", "arm64"]},
       {"path": "/usr/bin/su", "change": "changed", "arches": ["amd64"],
        "base": {"kind": "file", "mode": "4755"}, "head": {"kind": "file", "mode": "0755"}}]}]}]}
```

`revisions` is the argument as given and `merge-base` whether the base is one (`...` or `--merge-base`). the example is `chisel-diff --merge-base canonical/ubuntu-24.04`. `rev` is the revision compared, which for the base is then the merge base and else its tip, and null for a working tree. a side that is no revision says what it is in `kind` (`worktree`, `index`, `dir`, and `rev` when left out), and a `dir` side has its path in `dir`. a slice's `forced` is true when it was cut without being `changed`: picked by `--slice`, or for a path its package gives away with `prefer`. a side's `status` is `cut`, `not-available`, `failed`, `skipped` (its lead found the package missing) or `none` (the slice is not on that side, or was not cut), so a reader can tell a slice with no finding from one that was not compared. `change` is `lost`, `gained`, `changed` or `no-content`. a finding's `path` is the normalised one when its arches spell it differently, and then `raw` maps each arch to its own. `--no-group` makes every finding one arch's own.

## layout

mirrors the sister tools: `cmd/`, `internal/`, `version.go` + `VERSION`, `e2e/`, lowercase `makefile`, agpl-3, the same ci workflow.

- `internal/sides` -- git and snapshots: resolve refs and the merge base, write a revision or the working tree out as a release dir, validate it. the only package that runs git.
- `internal/changes` -- read the package files of both snapshots and say each slice's state, and delete entries from a base copy by their source. yaml only.
- `internal/listing` -- a rootfs as entries: path, kind, mode, symlink target.
- `internal/diff` -- the run: units through `pkg/pool`, the outcome table, the archive check, the findings, the folding into report groups.
- `internal/cli` -- flags, text and json rendering, exit codes.

deps: `github.com/lczyk/chisel-coverage/pkg` (`pool`, `cut`, `cache`, `fold`), `github.com/jessevdk/go-flags`, `gopkg.in/yaml.v3`, `github.com/lczyk/assert`, `github.com/lczyk/version/go`.

## testing

- unit tests with a fake `chisel`: the test binary re-executed, which fills `--root` from the release dir it is given by reading the slice's `contents` keys and following `essential`, no globs, and fails on demand the ways the outcome table names. two sides are two commits of a temp git repo made in the test. this checks the sides (every form of the revisions argument and the ones refused, a stale base, the working tree with untracked files, the index with unstaged edits beside it, two dirs with no repo, a subdirectory release), the states, the `prefer` slices joining the cuts, `--name-status`, every row of the outcome table, pruning, the archive check, the directory rule, folding, exit codes and rendering without chisel or network.
- integration tests with the real `chisel` against an in-memory signed archive of handmade debs (`chisel-proxy/pkg/apt`), as chisel-coverage has. they pin what the design assumes of chisel, one fixture each: a path moved behind an essential is not lost, a dropped path is, a glob for explicit paths, a dropped essential, package-level essentials, list and map essentials with `arch`, `no content` in both its forms, a package missing on an arch (its own and an essential's), a directory made by `make: true`, a `copy:` whose source is gone, a hard link whose target is gone, a directory turned symlink, `until: mutate`, `generate: manifest`, a setuid file, and the `prefer` cases: the loss in a slice that was not touched must be reported when that slice spells the `prefer` and when a sibling slice of its package does, and the shape that is known to be missed is pinned as missed. skipped when `chisel` is not found. not covered: an archive that needs credentials.
- `e2e/` against the real archive and a chisel-releases clone, behind `-tags e2e` and `CHISEL_DIFF_E2E=1`. it takes a current release branch tip as the base and edits the working tree for the head, since old commits stop cutting as the archive moves.

## what the spikes found

observations from one machine in november 2026, arm64 host. they show the design's moves work on real history. they prove nothing about the cases the integration fixtures are for.

`spike/real-cut-diff.sh <releases-repo> <base> <head> <arch> <pkg>_<slice>...` does a snapshot, the cuts and the difference by hand on one arch, with `git archive`:

run | result
---|---
`52181c6e^ 52181c6e coreutils_bins` (24.04, 24 paths moved to sub-slices behind essentials) | 181 paths on both sides, no difference, 8s and 10s
`4f7cf065^ 4f7cf065 dpkg_bins` (26.04, same pattern) | 136 paths on both sides, no difference, 17s cold and 5s warm
`5525cbe9^ 5525cbe9 systemd_bins` on armhf (wider `arch:` lists) | 672 -> 718 paths, 46 gained, none lost
`f2600d3a^ f2600d3a apparmor_profiles` | the base fails with `no content at /etc/apparmor.d/busybox`, the head cuts
`fc660a42^ fc660a42 base-files_var` | both fail, `chisel.yaml: unknown format "chisel-v1"`
`libc6_config` at an unchanged revision | one of two cuts failed with `cannot fetch from archive: context deadline exceeded`

`spike/prefer/run.sh <v3-release>` and `spike/modes/run.sh <v3-release>` each serve handmade debs with chisel-proxy next to a real release, to run two things a reviewer read out of chisel's source:

run | result
---|---
`prefer`: `cdx-s_bins` declares `/cdx/p` preferring `cdx-v`, `cdx-v_temp` declares it `until: mutate`, the head makes `cdx-u_config`, an essential of `cdx-s_bins`, depend on `cdx-v_temp` | `cdx-s_bins`, the same on both sides, loses `/cdx/p`. `cdx-u_config`, the one that changed, loses nothing
`modes`: `/cdx/new/: {make: true, mode: 0700}` with three `text:` files under it, cut 12 times | 0700 six times, 0755 six times
`prefer-more/a`: as `prefer`, but the `prefer` is spelt by a sibling slice `cdx-s_policy` and `cdx-s_bins` lists `/cdx/p` plainly | `cdx-s_bins` loses `/cdx/p`. `cdx-s_policy` and `cdx-u_config` lose nothing
`prefer-more/b`: `cdx-s_bins` lists nothing itself and reaches `cdx-s_data`, which lists `/cdx/p` with the `prefer`, and `cdx-u_config` | `cdx-s_bins` loses `/cdx/p`. `cdx-s_data` and `cdx-u_config` lose nothing

both look like chisel's to fix. the first is why slices that list a path given away with `prefer` are cut along with the `changed` ones, the second why directory modes are not compared.

`spike/pipeline` is most of the design in one go file on the library as it is today (`pkg/cut`, `pkg/cache`, `pkg/fold`, a pool of its own), every arch, 8 cpus. it has no pruning, no `changed` entries and no archive check.

run | cuts | wall | result
---|---|---|---
`-base 52181c6e^ -head 52181c6e -tip` (coreutils, 3 changed and 24 added slices, added ones cut too) | 224 | 73s | no path lost, 1118 lines of `+` for the added slices
`-base HEAD` on a clone of ubuntu-24.04 with openssl and libssl3t64 edited in the working tree | 49 | 25s | below

```
libssl3t64_libs 3.0.13-0ubuntu3.16 [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/usr/lib/<arch>-linux-<gnu>/engines-3/padlock.so
-/usr/lib/<arch>-linux-<gnu>/libcrypto.so.3
openssl_bins 3.0.13-0ubuntu3.16 [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/usr/bin/c_rehash
-/usr/lib/<arch>-linux-<gnu>/engines-3/padlock.so
-/usr/lib/<arch>-linux-<gnu>/libcrypto.so.3
openssl_config 3.0.13-0ubuntu3.16 [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/etc/ssl/private/
```

the edits dropped `c_rehash` from `openssl_bins` into a new slice nothing depends on, dropped an empty directory from `openssl_config`, and took the `libssl3t64_libcrypto` essential and one engine out of `libssl3t64_libs`. `openssl_bins` shows the libssl losses too, since it reaches that slice and its own definition changed. seeding a view of a 6.5k-blob cache took 10s when seven were seeded at once, which `pkg/cuts` already avoids by seeding one at a time.

also checked: `chisel find --release <dir> <nothing>` validates a release in 0.13s offline and fails on an essential whose slice file was deleted. chisel reads `slices/` recursively and `bin-slices/` beside it (`internal/setup/setup.go`, `readSlices`).

## build order

each step leaves a tool that works.

1. the check. sides in all their forms (`--cached` is a ref once git has written the index as a tree, `--no-index` a copy like the working tree), validation, states and `--name-status`, the `prefer` slices with a comment in the code on why they are cut, real cuts on every arch, the outcome table with every base failure an error, lost and gained entries, the archive tripwire, folding, text and json, `--fail-missing`. entries are matched by path with no trailing slash from the start, so a path on both sides is neither lost nor gained whatever became of its kind, and `pkg/cut.Walk`'s slash-suffixed listing is not what is compared.
2. pruning, so that a PR which drops a path the archive dropped is checked instead of failing.
3. `changed` entries: kind, file mode, symlink target.

step 1 waits for `pkg/pool`: the PR of `design-pkg.md` and its `pkg/` tag come first.

## decided

- the sides are named as `git diff` names them, with nothing bent: a bare `A` is the tip of `A` against the working tree and `A...` ends at `HEAD`. this replaced `--base`, `--head` and `--tip`.
- with no revisions it is `git diff`: the working tree against the index. what a pull request would change is `--merge-base` with no revision, which git does not have. a release name with no revisions is an error. a first take, the pull request with no arguments, was dropped since it surprised.
- the general rule: work, and warn where the circumstances are unusual and likely a mistake.
- `--release` takes a release name as in chisel-coverage, from a bare clone of canonical/chisel-releases kept in the cache, so two shas of upstream can be compared without a checkout. a release branch a checkout lacks comes from its remote or that clone, never fetched into the checkout. no `--onto`: `--merge-base <ref>` already picks the branch.
- a comparison across releases needs `-x` and cuts only what is named. more cases are likely to come up there.
- a `chisel.yaml` change with no package named compares nothing and exits 0 with a notice. the tool compares slices.
- the flag is `--fail-missing`, next to chisel-coverage's `--fail-uncovered`.
- `--name-status` and packages named by path are in the first version. the path spelling goes to chisel-file and chisel-coverage too, as one helper in `chisel-coverage/pkg`, in a small PR of its own and not in the `pkg/pool` one.
- a base that fails for another reason than `no content` is an error, exit 2. a PR that repairs such a slice needs a human to wave the check through.
- `pkg/pool` lands in chisel-coverage first, through a PR and a `pkg/` tag, as chisel-file's needs did. the reviewer would have started with a small pool here.
- `not available` on both sides after chisel skipped an archive for want of credentials is reported and exits 0, with `--fail-missing` too. a laptop has no pro credentials, and every run there would fail for a pro package otherwise. the reviewer picked the error.
- a file's mode is compared, a directory's is not, until canonical/chisel#353 is fixed.
- the `prefer` hole is narrowed by cutting, whenever anything else is cut, the slices that list a path their package gives away with `prefer`, with the reason written in the code. `--dependents` stays for later.
- `--cached` / `--staged` and `--no-index` are in the first version, for parity with `git diff`.
- `changed` entries are reported and never fail the check. the reviewer reads the `~` lines. a `--fail-changed` flag is under "later, maybe".

- for `prefer`, the rule above is as far as it goes while canonical/chisel#354 is open. a slice that only reaches the listing slice is missed and the readme says so. reading essentials to find it was turned down, as was having no rule. no published slice is in either shape today.

no question is open.

both chisel findings are upstream: the mode of a generated directory as canonical/chisel#353, the `prefer` and `until: mutate` loss as canonical/chisel#354. once #354 is fixed the `prefer` slices need no cutting, and once #353 is, directory modes can be compared.

## later, maybe

- `--dependents`: also compare the slices that reach a `changed` one. closes the symlink gap at the price of reading essentials and of many more cuts.
- `--own`: compare what each slice claims from its own deb, through the stripped cuts of chisel-coverage, to see which slice a path moved to.
- `--ignore <glob>`: leave paths out of the report. the sister tools do it with extra slices of the one deb, which a real cut of many debs has no place for.
- `--content`: hash regular files too. `/var/lib/chisel/manifest.wall` differs whenever anything does, so it would need a rule of its own.
- `--stat`: counts per block instead of paths.
- `--fail-changed`: exit 1 on a `changed` entry too, for a ci that wants a lost setuid bit or a file turned symlink to stop a PR.
