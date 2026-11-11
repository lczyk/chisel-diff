# chisel-diff

`git diff` for what [chisel](https://github.com/canonical/chisel) slices deliver.

a development tool for [chisel-releases](https://github.com/canonical/chisel-releases) work, and a sister of [chisel-coverage](https://github.com/lczyk/chisel-coverage), [chisel-file](https://github.com/lczyk/chisel-file) and [chisel-proxy](https://github.com/lczyk/chisel-proxy). for two revisions of a release it cuts the slices whose definition changed, from both, with the real chisel, and reports the paths each lost or gained, per architecture.

## why

published slices are append-only: a path a slice delivered must stay delivered. a yaml diff cannot tell. a path that moves into a new slice the old one depends on is still there, and a dropped essential takes paths the yaml never spelt out. so chisel is asked: cut the slice at both revisions and compare the two trees.

## use

```shell
chisel-diff --arch all --fail-missing HEAD
libssl3t64 3.0.13-0ubuntu3.16 libs [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/usr/lib/<arch>-linux-<gnu>/engines-3/padlock.so
-/usr/lib/<arch>-linux-<gnu>/libcrypto.so.3
openssl 3.0.13-0ubuntu3.16 bins [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/usr/bin/c_rehash
-/usr/lib/<arch>-linux-<gnu>/engines-3/padlock.so
-/usr/lib/<arch>-linux-<gnu>/libcrypto.so.3
openssl 3.0.13-0ubuntu3.16 config [amd64, arm64, armhf, i386, ppc64el, riscv64, s390x]
-/etc/ssl/private/
openssl scripts: added
```

a line is `-<path>` for an entry lost and `+<path>` for one gained, so `grep '^-'` is the list of what went missing. architectures that agree are folded into one block. a slice that was added or removed is named and not cut.

the two sides are named as for `git diff`:

```shell
chisel-diff HEAD                # the uncommitted edits
chisel-diff main...             # this branch since it left main
chisel-diff --merge-base main   # the same, with the uncommitted edits
chisel-diff --cached HEAD       # what is staged
chisel-diff ubuntu-24.04..ubuntu-26.04 openssl
chisel-diff --no-index ./a ./b  # two release dirs, no git
```

without packages, every package whose file differs between the two sides is looked at. a package can be given as the path of its file (`slices/openssl.yaml`), so tab completion works. `--slice` picks slices by name, changed or not. `--name-status` names the slices and their states and cuts nothing, which takes half a second and no network.

`--json` is the same report as one document, with every cut's outcome on both sides and every entry, directories too.

`--fail-missing` makes it a check. exit codes:

- 0 -- the comparison was made, whatever it found.
- 1 -- only with `--fail-missing`: an entry was lost, a slice was removed, or a slice no longer cuts on an architecture.
- 2 -- the comparison could not be made: a cut failed, the archive moved under the run, a side is not a release chisel accepts.

## not there yet

entries that changed kind or mode, and a base that no longer cuts because the archive dropped a path it names (an error for now). `design.md` is the plan.

one known gap: with `prefer`, a slice nobody edited can lose a path ([canonical/chisel#354](https://github.com/canonical/chisel/issues/354)). slices that list such a path are cut as well, which finds it there. a slice that only reaches one of those through an essential is not looked at.

## build

```shell
make build     # ./bin/chisel-diff
make verify    # lint + tests
make e2e       # against the real archive and a chisel-releases clone
```

needs `git` and `chisel` on `PATH`, or `GIT=` and `CHISEL=`.
