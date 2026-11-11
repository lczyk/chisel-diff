# chisel-diff

`git diff` for what [chisel](https://github.com/canonical/chisel) slices deliver.

a development tool for [chisel-releases](https://github.com/canonical/chisel-releases) work, and a sister of [chisel-coverage](https://github.com/lczyk/chisel-coverage), [chisel-file](https://github.com/lczyk/chisel-file) and [chisel-proxy](https://github.com/lczyk/chisel-proxy). for two revisions of a release it cuts the slices whose definition changed, from both, with the real chisel, and reports the paths each lost, gained or changed, per architecture.

## why

published slices are append-only: a path a slice delivered must stay delivered. a yaml diff cannot tell. a path that moves into a new slice the old one depends on is still there, and a dropped essential takes paths the yaml never spelt out. so chisel is asked: cut the slice at both revisions and compare the two trees.

## use

```shell
chisel-diff --arch all --fail-missing HEAD
libssl3t64_libs 3.0.13-0ubuntu3.16 [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/usr/lib/<arch>-linux-<gnu>/engines-3/padlock.so
-/usr/lib/<arch>-linux-<gnu>/libcrypto.so.3
openssl_bins 3.0.13-0ubuntu3.16 [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/usr/bin/c_rehash
-/usr/lib/<arch>-linux-<gnu>/engines-3/padlock.so
-/usr/lib/<arch>-linux-<gnu>/libcrypto.so.3
openssl_config 3.0.13-0ubuntu3.16 [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/etc/ssl/private/
openssl_scripts: added
```

a line is `-<path>` for an entry lost, `+<path>` for one gained and `~<path>: <base> -> <head>` for one on both sides that changed kind, file mode or symlink target, so `grep '^-'` is the list of what went missing. architectures that agree are folded into one block. a slice that was added or removed is named and not cut.

a base the archive has moved under, a slice naming a path its package no longer ships, does not cut, and the PR that drops the path is routine. so the base is cut again without the entries chisel named, and each is reported as `!<path>: no content`, neither lost nor gained.

the two sides are named as for `git diff`:

```shell
chisel-diff                     # what is not staged
chisel-diff --merge-base        # what this branch would change as a PR
chisel-diff HEAD                # the uncommitted edits
chisel-diff main...             # this branch since it left main
chisel-diff --merge-base main   # the same, with the uncommitted edits
chisel-diff --cached HEAD       # what is staged
chisel-diff ubuntu-24.04..ubuntu-26.04 openssl
chisel-diff --no-index ./a ./b  # two release dirs, no git
chisel-diff --release ubuntu-26.04 HEAD~1..HEAD  # upstream's last merge, no checkout
```

`--merge-base` with no revision compares the files on disk from where `HEAD` parted from the release branch `chisel.yaml` is of, `ubuntu-26.10` say: the local branch, else the one of the remote for canonical/chisel-releases, else fetched. a stale local branch gets a warning. `--release` takes a checkout or a release name, whose branch of canonical/chisel-releases is kept in a bare clone under `~/.cache/chisel-diff` and whose tip is then `HEAD`. a release name in a checkout that lacks the branch is read from that clone too, and the checkout is left as it was.

a comparison across ubuntu releases, by the version in each side's `chisel.yaml`, is refused without `-x` / `--x-release`, and with it cuts only the packages or `--slice` named (`--slice=all` is the whole release). some release branches share no history, so with `-x` a missing merge base compares the tips.

without packages, every package whose file differs between the two sides is looked at. a package can be given as the path of its file (`slices/openssl.yaml`), so tab completion works. `--slice` picks slices by name, changed or not, and `--slice=awt:awt-v2` compares slice `awt` at the base with slice `awt-v2` at the head in every package that has both, to see what a replacement slice drops or adds. `chisel-diff --slice=awt:awt-v2 HEAD..HEAD` asks it on one tree, so only the two definitions differ. `--name-status` names the slices and their states and cuts nothing, which takes half a second and no network.

`-T` / `--tree` draws each block's entries as a tree of their paths, each entry's sign glued to its name as the flat report glues it to the path.

`-P` / `--packages` ends each entry with the packages it comes from, `-/usr/lib/x86_64-linux-gnu/libuuid.so.1 libuuid1`, and `--packages=slices` with the slices. chisel says which: every cut also writes chisel's manifest, through a slice added to a copy of the package's file, and the manifest names the slices of each path. a directory chisel made only to hold others comes from none and has no suffix. chisel cannot write a manifest for a release whose archive gives no sha256 digests, 26.10 for now, and the run then stops with chisel's error.

`--json` is the same report as one document, with every cut's outcome on both sides and every entry, directories too.

`--fail-missing` makes it a check. exit codes:

- 0 -- the comparison was made, whatever it found.
- 1 -- only with `--fail-missing`: an entry was lost, a slice was removed, or a slice no longer cuts on an architecture.
- 2 -- the comparison could not be made: a cut failed, the archive moved under the run, a side is not a release chisel accepts.

## known gaps

- a directory's mode is not compared, since chisel does not make it the same from one cut to the next ([canonical/chisel#353](https://github.com/canonical/chisel/issues/353)).
- a changed entry never fails the check. the reviewer reads the `~` lines.
- with `prefer`, a slice nobody edited can lose a path ([canonical/chisel#354](https://github.com/canonical/chisel/issues/354)). slices that list such a path are cut as well, which finds it there. a slice that only reaches one of those through an essential is not looked at.

`design.md` is the design note the tool was built from.

## build

```shell
make build     # ./bin/chisel-diff
make verify    # lint + tests
make e2e       # against the real archive and a chisel-releases clone
```

needs `git` and `chisel` on `PATH`, or `GIT=` and `CHISEL=`.
