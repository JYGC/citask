# citask

`citask` builds and tests every change that reaches a project's trunk branch and reports the result on
GitHub. It runs on one OpenBSD machine, needs no inbound network access, and has no language knowledge:
each project declares its own steps.

Cron runs `citask poll` every few minutes. For each enrolled project, the poll:

1. compares the trunk commit on GitHub with the last commit it tested;
2. when they differ, checks out that exact commit in a clean clone and runs the steps in that commit's
   `ci-steps.json`;
3. posts a commit status and, while the trunk is failing, keeps one GitHub issue open.

Runs happen one at a time. It only builds and tests: it never deploys anything.

## Limits

- **Public GitHub repos only.** Clones are anonymous.
- **Toolchains must run on the machine.** `citask` runs any command, but it cannot make one exist.
- **No secrets for steps.** `ci-steps.json` is public, and nothing else supplies a step with one.
- **Projects queue.** A long run delays the others, up to its time limit.
- **Trunk only.** Pull requests are not checked.

## Enrolling a project

1. Install the project's toolchain on the machine, if it is missing.
2. Check GitHub Issues are enabled on the repo, because failures are reported there.
3. Add `ci-steps.json` to the project's trunk.
4. Add the repo to the GitHub token's repository list.
5. Add the project to `citask.json`.

The next poll tests the current trunk commit. Nothing about polling, reporting or the schedule changes.

## `ci-steps.json`

The file sits at the root of the repo and is read from the commit under test, so a project adding a
check ships it in the same commit as the fixes that check depends on.

```json
{
  "Steps": [
    {
      "Name": "build",
      "WorkingDirectory": ".",
      "Command": ["make", "build"]
    },
    {
      "Name": "tests",
      "WorkingDirectory": ".",
      "Command": ["make", "test"],
      "Environment": { "EXAMPLE_SETTING": "value" },
      "DependsOn": ["build"],
      "RepeatOnceOnFailure": true
    }
  ]
}
```

| Field | Required | Meaning |
|---|---|---|
| `Name` | Yes | Unique within the file; shown in reports. `read-step-definitions` is reserved |
| `WorkingDirectory` | Yes | Relative to the repo root, and must stay inside it |
| `Command` | Yes | Program and arguments, run directly with no shell |
| `Environment` | No | Extra variables. This file is public, so never secrets |
| `DependsOn` | No | Earlier steps that must pass first |
| `RepeatOnceOnFailure` | No | For a known-flaky step only. A pass on the repeat stays visible in the log |

Steps run in file order. A step runs only if everything in its `DependsOn` passed; otherwise it is
recorded as skipped, while independent steps still run. Unknown fields make the file invalid, so a
misspelt `DependsOn` fails loudly rather than silently running a step it should skip. A missing or
invalid file fails the run at a step called `read-step-definitions`.

Steps do not inherit `citask`'s environment. They get `HOME`, `PATH`, `USER`, `LOGNAME`, `LANG` and
`LC_ALL` when those are set, then the following, and then the step's own `Environment`:

| Variable | Value |
|---|---|
| `CI` | `true` |
| `TMPDIR` | A private folder in `/tmp` for this run, deleted when it ends |
| `XDG_RUNTIME_DIR` | A folder inside `TMPDIR` |
| `CI_RUN_ARTIFACT_DIRECTORY` | A folder kept with the run log, for screenshots and the like |

## Results

Each run posts a pending status, then a success or failure status. The first failure opens an issue
titled `CI failing on <TrunkBranch> (citask)`, later failures comment on it, and the next pass closes it.

Commit statuses and issues are public on a public repo, so they contain only the project name, commit,
step names, outcomes and run identifier. Log output stays on the machine, because it often carries paths,
hostnames and environment values.

The run identifier, such as `20261031T052700Z-094b1e7`, is the folder name of that run's log under
`logs/<Name>/` in the CI home. A failure with no run, such as an unreachable remote, is recorded in
`logs/<Name>/poll-problems.log`. If GitHub cannot be reached, the result is kept and posted at a later
poll, oldest first.

`citask poll` exits non-zero only when a person is needed, such as for an invalid config or state that
cannot be written. Failing tests are not such a case: they are reported on GitHub and the poll exits 0.

## Installing

`citask` must run as a dedicated user named exactly `_ci`, which runs nothing else. After every run it
stops every other process owned by that user, and it refuses to do so under any other name.

`install-on-openbsd.sh` sets up the machine. Run it as root, as often as you like. It creates what is
missing and replaces the program when it differs, and a run with nothing to do reports `0 changes`.

1. Build the program, and write a `citask.json` based on `citask.example.json`:

   ```sh
   make build
   ```

2. Run the script:

   ```sh
   doas sh install-on-openbsd.sh --config /path/to/citask.json
   ```

   This creates the `ci` login class, the `_ci` user and its home at `/var/ci`, installs the program and
   `citask.json` in `/usr/local/libexec/citask/`, and adds `_ci` to `/var/cron/cron.deny` and
   `/var/cron/at.deny`, so a step cannot schedule its own work.

   The program sits in a root-owned folder rather than the CI home because `_ci` owns that home, and the
   owner of a folder can rename anything inside it, so a step could replace the program.

3. Create a fine-grained GitHub token limited to the enrolled repos, with commit status and issue write
   access. Store it at `/var/ci/etc/github-token`, owned by `_ci` with mode `0600`.
4. Start the schedule, which adds this to `/etc/crontab`. The script never starts it unless asked, and it
   refuses to without a token:

   ```sh
   doas sh install-on-openbsd.sh --start-schedule --mail-to example-user
   ```

   ```
   MAILTO=example-user
   */5 * * * * _ci -n -s /usr/local/libexec/citask/citask poll
   ```

   `-s` stops cron starting a second copy, and `-n` sends mail only when the poll exits non-zero.

To upgrade, run `make build` and the script again. The running poll keeps the old program, because the
new one replaces it by rename.

The script never overwrites the login class, `citask.json` or the schedule, because you may have edited
them, and running it again must not undo that. `--config` only applies when no `citask.json` is installed.
To change the config, edit the installed file, then check it with `citask check-config <file>`, which
names every failing field. Unknown fields are rejected. If `_ci` already exists with a different login
class, home or shell, or is in `wheel`, the script stops rather than change the account.

`citask` reads `citask.json` from the folder holding the program. To test a commit again, delete
`state/<Name>/last-tested-commit` in the CI home as `_ci`.

### What steps can reach

A step runs as `_ci`, so it can read the token file. The token never enters a step's environment or a
log, but a same-user file cannot be hidden from that user, so keep the token's permissions narrow. Caches
in the CI home are shared by every project, so enrol only projects you trust.

## Working on citask

This repo is public. Before your first push, turn on the tracked pre-push hook:

```sh
git config core.hooksPath .githooks
git config user.email <your GitHub noreply address>
```

The hook needs [`gitleaks`](https://github.com/gitleaks/gitleaks) and a list of your private hostnames,
addresses and account names at `~/.config/citask/gitleaks-private-values.toml`. Keep that list out of this
repo. It is a `gitleaks` config that adds your own rules to the defaults:

```toml
[extend]
useDefault = true

[[rules]]
id = "private-hostname"
description = "Hostname on a private network"
regex = '''(?i)\bexample\.internal\b'''
```

The hook scans outgoing file changes, commit messages and file names, and refuses any author or committer
address that is not a GitHub noreply address. It refuses the push if `gitleaks` or the list is missing.

Run the tests with `go test ./...`. OpenBSD is the target, so a change is finished only when they pass
there. See `CLAUDE.md` for the engineering standards.
