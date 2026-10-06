---
name: conventional-commits
description: Write, review, and organize Git commits following the Conventional Commits specification (feat, fix, refactor, etc.). Use whenever creating a commit, writing or suggesting a commit message, deciding a commit type, scope, description, body, or footer, flagging a breaking change, deciding how to split a mixed working tree into logical commits, or checking whether a message complies with Conventional Commits. Trigger on requests like "commit this", "what commit message for these changes?", "make this commit conventional", "split my changes into commits". Do not use for general Git operations (clone, rebase, branching, merge conflicts, stash, remotes) unless a commit message is part of the task.
---

# Conventional Commits

Write commit messages that follow the Conventional Commits 1.0.0 specification, and group changes into commits that someone reading `git log` later can understand at a glance.

## What this skill covers

- **Commit-message policy**: format, types, scopes, breaking changes, bodies, footers, and how to split changes into logical commits.
- **Not Git mechanics.** Staging, committing, amending — that's ordinary Git knowledge you already have. This skill says *what to write* and *which changes belong together*, nothing more.
- **Two modes.** If the user asked you to commit, commit. If they asked for a suggested message, a review, or advice, respond with text only and run no commands that modify the index or history. Check the wording before touching anything.

## Step 1 — repository rules beat this skill

Conventional Commits deliberately leaves room for project conventions. Before writing messages, gather the local rules in this order, and let each level override the ones below it:

1. **Explicit repo rules** — AGENTS.md, CONTRIBUTING.md, README sections about committing, docs/ guides; tooling config: commitlint (`.commitlintrc*`), release configs (release-please.yaml, `.releaserc*`, changesets), PR templates. These reveal which types and scopes the project's automation actually relies on.
2. **Strong, consistent history signal** — what the project actually does, including commits written by automation (Dependabot, Renovate).
3. **The defaults in this skill.**

For level 2, run the bundled helper instead of eyeballing raw logs:

```
python <skill-dir>/scripts/history_profile.py <repo-path>
```

It prints one compact JSON line describing the recent history: sample size, Conventional Commits match rate, observed types/scopes with frequencies, whether a scope vocabulary is established, body/footer usage, the dominant dependency-commit pattern, and a verdict — `strong`, `mixed`, `none` (sloppy history to ignore as a style signal), or `unknown` (too few commits). It needs only Python 3 (stdlib) and git. **The helper is descriptive only**: it profiles history, it never decides the type, scope, or wording of your commit — that comes from understanding the change (Step 2). If it fails or Python is unavailable, fall back to `git log --oneline -30` and judge by hand: mirror an established vocabulary (`feat(parser):` in the log means use `parser`, not `syntax`), and don't mirror sloppiness — a history of "WIP", "fix stuff", "asdf" is a signal to ignore, not to imitate.

## Step 2 — understand the change before choosing a type

The type describes the *effect of the change*, not the files touched. File names are a terrible proxy: an edit to `main.go` could be a feature, a bug fix, a refactor, or a dependency chore; a change to `styles.css` is usually *not* a `style` commit. Before writing anything:

1. Look at `git status` and the actual diffs (`git diff`, plus `git diff --staged` if part of the tree is already staged). Read the hunks, not the file list.
2. Ask what the change does to the people who *use* this code — CLI users, API consumers, other packages importing it:
   - New capability they didn't have → `feat`
   - Behavior now matches what was already intended or documented → `fix`
   - Structure or speed changes with no observable behavior change → `refactor` / `perf`
3. Only then pick the type and write the message.

A wrong type is worse than a missing body: automated tooling maps types to version bumps and changelog sections, so `fix` vs `feat` is a decision with consequences, not a style preference.

## Message format

```text
<type>[scope][!]: <description>

[optional body]

[optional footer(s)]
```

Header rules:

- One line, ideally ≤ 72 characters. Longer subjects get truncated by tools and break log readability.
- Description in the imperative mood, as if completing the sentence "this commit will…" — "add", not "added" or "adds".
- Lowercase first word and no trailing period by default (the near-universal convention; follow the repo if it differs).
- Describe the change itself, not the task or the request behind it: `add --version flag`, not `add version flag functionality as discussed`.

## Types

`feat` and `fix` are the only types the spec requires tooling to understand; the rest are convention. Common meanings (following the Angular lineage most configs inherit):

- `feat` — new capability visible to users of the code
- `fix` — corrects behavior that was supposed to work
- `refactor` — restructuring with no change in observable behavior
- `perf` — improves performance (internal changes, same behavior, faster)
- `docs` — documentation only (README, docs site, docstrings)
- `test` — tests and test fixtures only
- `build` — changes to the build system, toolchain, or build scripts; Angular-lineage repos also file dependency updates here (see the dependency note under ambiguities for how to choose)
- `ci` — CI pipeline configuration and scripts (workflow files, Jenkinsfile, GitLab CI)
- `style` — code formatting: whitespace, semicolons, import ordering. *Code style, not CSS* — visual or UI changes are behavior; they belong to `feat` or `fix`.
- `chore` — repository maintenance that fits nothing else. Prefer a more specific type when one applies.
- `revert` — reverts an earlier commit (see below).

Classic ambiguities:

- **fix vs feat**: restoring behavior users never actually had (because it was broken) is `fix`; giving them behavior they never had at all is `feat`. Changing behavior people may legitimately depend on is new behavior — `feat`, possibly breaking.
- **Docs and tests that accompany a code change** belong in the same commit as the code they cover, not in separate `docs`/`test` commits. A standalone `test` commit is for expanding coverage of *existing* behavior — a new test that exists to validate the change being committed travels with that change. Standalone docs edits are `docs`.
- **Dependency updates** (`chore(deps)` vs `build(deps)`): the spec is silent and the ecosystem is split — the Angular definition of `build` explicitly includes external dependencies and Dependabot writes `build(deps)` by default, while Renovate writes `chore(deps)` by default. Precedence: if the repo's history or bots establish a pattern (the profile's `deps_commit_pattern` shows it), match it. Otherwise default to `chore(deps)` — routine dependency maintenance, with `build` reserved for build-system and toolchain changes proper (webpack config, npm scripts, Makefile) and `ci` for CI changes. Treat `(deps)` as the de-facto standard scope for dependency commits: use it even in a repo whose other commits carry no scopes. Security-relevant production updates are sometimes typed `fix(deps)` so they reach a patch release — follow repo practice.
- **build vs ci vs chore**: build affects how the artifact compiles/packages, ci affects pipeline automation, chore is the rest.

## Scopes

A scope names the part of the codebase affected: `feat(auth): …`. It is optional and entirely project-defined — the spec mandates nothing. If the repository has an established scope vocabulary (visible in `git log` or a commitlint config), infer it and stay consistent. If scopes are absent from the log, omit them.

Never coin a scope from a file or module name alone — `auth.py` existing is not evidence that `auth` is a recognized scope. Scopes come from repository evidence; when you have none (you can't inspect the repo, or history shows no scopes), no scope beats a wrong one.

## Breaking changes

Two signals exist, either alone is spec-compliant:

- `!` between type/scope and colon: `feat(api)!: remove legacy output flags`
- A `BREAKING CHANGE:` footer describing what changed, why, and — when relevant — how to migrate.

Prefer using **both together**: the `!` is visible in `git log --oneline` at a glance, while the footer carries the explanation that humans and release tooling need. A breaking change can combine with any type (`fix!`, even `chore!` if it breaks someone's workflow) — a bug fix that changes behavior callers may depend on is still breaking.

```text
feat(api)!: return 404 instead of 500 for missing resources

Unknown IDs were triggering an internal error and a 500 response.

BREAKING CHANGE: clients relying on 500-retry logic for missing
resources must handle 404 responses instead.
```

## Body

Most commits need no body. Add one only when the header and the diff genuinely cannot carry the *why*: motivation, constraints, trade-offs, side effects, or measurements (e.g. benchmark numbers for `perf`).

A body that narrates what the diff already shows adds noise — any reader can run `git show`. Redundant bodies train people to skip bodies, which punishes the commits that need one. When in doubt, leave it out. If you write one, wrap around 72 characters.

## Footers

`Token: value`, one footer per line, after a blank line following the body. Multi-word tokens use hyphens (`Reviewed-by:`), except `BREAKING CHANGE`. The value may span multiple lines until the next valid `Token:` pair. Common uses:

- Issue references: `Closes: #123`, `Refs: #456` — prefer whatever reference token (`Fixes`/`Resolves`/`Closes`) the repository already uses.
- Reviews/attribution: `Reviewed-by: Z`, `Co-authored-by: Name <email>`.
- `BREAKING CHANGE: …` as described above.

## Reverts

The common convention: `revert: <subject line of the reverted commit>`, with a body line `This reverts commit <sha>.` Keeping the original subject (including its type and scope) visible lets readers trace the pair in the log.

```text
revert: fix(paths): detect Windows VS Code storage

This reverts commit 667ecc1654a317a13371b17657a6c3d78e6f2e4c.
It broke path detection on macOS; a correct fix follows separately.
```

## One commit, one purpose

A mixed working tree is a prompt to split, not to lump. After reading status and diffs, group changes by purpose:

- Each commit should stand on its own — it builds, and tests pass where that's feasible.
- Code, its tests, and its documentation belong together in one commit.
- Unrelated drive-bys (formatting sweeps, dependency bumps, typo fixes) get their own commits — each with the type that matches *its* content.

When the tree contains several independent changes, briefly propose the split first (a list of `type: subject` lines) instead of silently committing; if the user explicitly asked to commit everything as one, that's their call — do it, optionally noting the alternative in a single sentence.

The mechanics are ordinary Git: stage each group (`git add <paths>`, or `git add -p` for partial hunks) and commit it with its own message.

Two hygiene notes when your work involves running builds or tests (e.g. verifying a commit passes): never commit the artifacts they generate (`__pycache__/`, `dist/`, coverage files), and don't leave them behind as untracked noise either — remove them (or leave the tree exactly as you found it). When you report the final state afterwards, describe it accurately: "clean except untracked `__pycache__/`" is not a clean tree.

## Relation to versioning

Conventionally `feat` means a minor version bump, `fix` a patch, and a breaking change a major. Whether releases actually behave that way depends on this repository's tooling — semantic-release, release-please, changesets, manual tagging, or nothing at all. Don't assert release consequences unless the repo's release configuration confirms them; just write messages that let whatever tooling exists do its job.

## Reviewing an existing message

When asked whether a message follows Conventional Commits, keep three tiers separate and label them as such — presenting a preference as a spec violation erodes trust in real violations:

1. **Spec violations**: header doesn't match `<type>[scope][!]: <description>`; no valid type before the colon; a breaking change signaled with an invalid token like `BREAKING:` instead of `!` or `BREAKING CHANGE:`; footer tokens with spaces instead of hyphens.
2. **Repository rules**: types/scopes outside the project's allowed lists, subject-length limits, casing or language conventions from CONTRIBUTING.md, commitlint config, or the log itself.
3. **Style conventions** (recommendations, not compliance): imperative mood, lowercase subject, no trailing period, issue references in footers rather than body prose, informative rather than vague descriptions.

Report what fails, with the specific fix. If you can't inspect the repository, suggest the corrected message *without* a scope rather than inferring one from a file name — and don't rewrite history unless asked.
