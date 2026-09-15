# Unpublished local distributions

From the repository root:

```sh
make dist-local
# Or specify a version and output directory:
go run ./tools/dist-local -version 1.16.10+local.20260915.deadbeef -output dist-local
# Equivalent make overrides:
make dist-local DIST_VERSION=1.16.10+local.20260915.deadbeef DIST_OUTPUT=dist-local
```

The default version uses the nearest `v` version tag, the UTC build date and the
abbreviated commit hash, for example `1.16.10+local.20260915.deadbeef`.
The working tree is built as it stands, including uncommitted changes; the commit
suffix identifies the base commit. Supply a unique version for each build when
comparing multiple dirty working trees.

The tool builds darwin/arm64, linux/amd64 and linux/arm64 with `CGO_ENABLED=0` and
the release linker flags `-s -w -X main.Version=<version>`. `-trimpath` removes
controller source paths from the binary. It uses the committed
CSS, as the release does. Each `agent-deck_<version>_<os>_<arch>.tar.gz` contains
`agent-deck`, `README.md`, `LICENSE` and `CHANGELOG.md` at its root.
`checksums.txt` contains SHA-256 hashes of the complete archives in the release
format. Compilation finishes before any archive is published; the manifest is
published last. Use a separate output directory for concurrent builds.

Install through `agent-deck remote update <name> --from-build dist-local`, or
preview using `--dry-run`. Building a distribution does not contact or update
any remote. No release token or GoReleaser installation is needed.

## Updating remotes

```sh
agent-deck remote update dev --from-build dist-local --dry-run
agent-deck remote update --all --from-build dist-local --json
agent-deck remote update dev --from-build dist-local --force
agent-deck remote list --json
```

The controller detects each remote's OS and architecture independently, verifies
the matching archive with the release checksum verifier, and checks its executable
format. SSH streams only that archive with its checksum and remote installation
arguments. The remote verifies the checksum again, checks the staged executable's
version, then atomically replaces the resolved installation file. Existing
symlink, ownership, PATH and inode checks apply. A failed checksum, architecture
or staged-version check leaves the installed binary untouched.

A newer remote version is refused unless `--force` is supplied. Local builds with
the same release core can replace each other. Successful installs record
`installed_from: "local-build"` in the controller's remote version cache, visible
in `remote list --json`. A subsequent published release with the same version
core or a newer version replaces the local build during the normal release sweep.
Build metadata after `+` does not increase semantic version precedence.

`--dry-run` verifies the local artifact and probes the remote destination paths,
but uploads nothing and changes neither installed binaries nor the version cache.
`--json` writes an array with every remote's outcome, target version, installation
report and any error. The same flags work for a named remote and `--all`; a failed
remote does not prevent processing the rest. Human output ends with a summary
table. Exit status is 1 if any remote fails.

Running processes use their existing idle-only restart mechanism. Processes
running a version containing this change recognize distinct equal-core local
builds. Older processes whose watcher only recognizes semantic-version upgrades
need a one-time restart when first moving to an equal-core local build. Forced
downgrades also require a manual restart of existing processes; the watcher does
not automatically restart into an older semantic version. The updater does not
interrupt active sessions to force a restart.
