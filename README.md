# chisel-diff

`git diff` for what [chisel](https://github.com/canonical/chisel) slices deliver.

a development tool for [chisel-releases](https://github.com/canonical/chisel-releases) work, and a sister of [chisel-coverage](https://github.com/lczyk/chisel-coverage), [chisel-file](https://github.com/lczyk/chisel-file) and [chisel-proxy](https://github.com/lczyk/chisel-proxy). for two revisions of a release it cuts the slices whose definition changed, from both, with the real chisel, and reports the paths each lost, gained or changed, per architecture. `--fail-missing` makes that a check: published slices are append-only, and a path a slice delivered must stay delivered.

**work in progress.** what works today is `--name-status`, which names the slices and their states and cuts nothing. `design.md` is the plan for the rest.

## use

the two sides are named as for `git diff`:

```shell
chisel-diff --name-status HEAD                # the uncommitted edits
chisel-diff --name-status main...             # this branch since it left main
chisel-diff --name-status --merge-base main   # the same, with the uncommitted edits
chisel-diff --name-status --cached HEAD       # what is staged
chisel-diff --name-status ubuntu-24.04..ubuntu-26.04 openssl
chisel-diff --name-status --no-index ./a ./b  # two release dirs, no git
```

```
libssl3t64 libs: changed
openssl bins: changed
openssl config: changed
openssl scripts: added
```

without packages, every package whose file differs between the two sides is looked at. a package can be given as the path of its file (`slices/openssl.yaml`), so tab completion works.

## build

```shell
make build     # ./bin/chisel-diff
make verify    # lint + tests
```

needs `git` and `chisel` on `PATH`, or `GIT=` and `CHISEL=`.
