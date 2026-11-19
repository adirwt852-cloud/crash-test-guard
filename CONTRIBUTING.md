# Contributing to Crash-Test Guard

Thanks for your interest in improving Crash-Test Guard. This document covers
how to get set up, the conventions we follow, and what we expect from a pull
request.

By participating in this project you agree to license your contributions under
the [Apache License 2.0](LICENSE).

---

## How to Contribute

There are many ways to help, and not all of them involve writing code:

- **Report bugs.** Open an issue with a minimal reproduction, the version you
  are running (`crash-test-guard version`), your OS/architecture, and the
  configuration file used to reproduce the problem.
- **Request features.** Describe the problem you are trying to solve and the
  workflow or agent framework you use. Concrete use cases get prioritized.
- **Improve documentation.** Typo fixes, clearer quickstarts, and new adapter
  guides in [`docs/`](docs/) are always welcome.
- **Write adapters.** Crash-Test Guard is protocol-based and language-agnostic.
  See [`docs/adapter-development.md`](docs/adapter-development.md) to build
  support for a new agent framework.
- **Send code.** Bug fixes, test coverage, and new crash-injection backends are
  the most valuable code contributions.

Before starting on a large change, **open an issue or discussion first** so we
can agree on the approach. This avoids duplicated effort and wasted review
cycles.

### Good First Issues

Issues labelled [`good first issue`](https://github.com/adirwt852-cloud/crash-test-guard/labels/good%20first%20issue)
are scoped to be approachable without deep knowledge of the codebase.

---

## Development Setup

### Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| [Go](https://go.dev/dl/) | 1.22+ | Build and test the CLI |
| [golangci-lint](https://golangci-lint.run/usage/install/) | latest | Static analysis |
| [Docker](https://docs.docker.com/get-docker/) | 20.10+ | Container/pod crash scenarios (optional) |
| GNU Make | 3.81+ | Task runner |

The project has no C dependencies and builds as a single static binary with
`CGO_ENABLED=0`.

### Get the Code

```bash
git clone https://github.com/adirwt852-cloud/crash-test-guard.git
cd crash-test-guard
```

### Install Dependencies

```bash
make deps
```

This downloads the Go module dependencies declared in `go.mod`. If `make deps`
is not available in your checkout, run `go mod download` directly.

### Build

```bash
make build      # produces bin/crash-test-guard
make run        # builds and prints --help
```

### Test

```bash
make test
```

### Lint

```bash
make lint
```

The same commands run in CI (`.github/workflows/ci.yml`), so a green local run
is the fastest way to a green pull request.

---

## Code Style

### Formatting: gofmt

All Go code must be formatted with `gofmt`. Before committing:

```bash
gofmt -l -w .
```

`gofmt -l .` must produce no output. Most editors can format on save, and the
standard `go fmt ./...` is equivalent for package trees.

### Linting: golangci-lint

We use [`golangci-lint`](https://golangci-lint.run/) with the repository
configuration. Run it before pushing:

```bash
golangci-lint run
```

Install it if needed:

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

CI fails on lint findings. Do not add `//nolint` directives without a
justification comment explaining why the warning is a false positive or an
accepted trade-off.

### General Guidelines

- Keep packages focused: `internal/` holds implementation details and is not
  part of the public API; `pkg/types` is the stable shared surface.
- Prefer explicit error handling with wrapped context (`fmt.Errorf("...: %w", err)`).
- Avoid new dependencies unless they are clearly justified; the project aims to
  stay small and dependency-light.
- Exported identifiers need doc comments starting with the identifier name.
- Tests belong next to the code they test (`*_test.go`), and test fixtures live
  under `test/`.
- Do not add comments that merely restate the code; explain intent and
  invariants instead.

---

## Commit Message Format

We follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):

```
<type>(<optional scope>): <description>

<optional body>

<optional footer(s)>
```

### Types

| Type | Use for |
|------|---------|
| `feat` | A new user-facing feature |
| `fix` | A bug fix |
| `docs` | Documentation only |
| `test` | Adding or correcting tests |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `perf` | Performance improvement |
| `build` | Build system, Makefile, or dependency changes |
| `ci` | CI/CD configuration |
| `chore` | Maintenance that does not touch source behavior |

### Rules

- Use the imperative mood in the description: `add checkpoint validation`,
  not `added` or `adds`.
- Keep the subject line at 72 characters or fewer and do not end it with a
  period.
- Use the body to explain **why**, not **what** — the diff already shows what.
- Reference issues in the footer: `Closes #42` or `Refs #17`.
- Mark breaking changes explicitly, either with a `!` after the type/scope
  (`feat(protocol)!: require protocol v2`) or with a `BREAKING CHANGE:` footer.

### Examples

```
feat(executor): add pod-delete crash injection

fix(orchestrator): retry checkpoint reads after container restart

docs(quickstart): clarify Docker socket requirements

test(reporter): cover exit code 1 on failed verification
```

---

## Pull Request Process

1. **Fork and branch.** Create a topic branch from `main`:
   `git checkout -b feat/pod-delete-injection`.
2. **Make focused changes.** One logical change per pull request. Keep unrelated
   refactors in separate PRs.
3. **Add tests.** See [Testing Requirements](#testing-requirements) below.
4. **Verify locally.** Run `gofmt -l .`, `make test`, `make lint`, and
   `make build`.
5. **Write a clear description.** In the PR body, include:
   - the problem and the approach,
   - how you tested it,
   - any user-visible behavior changes, and
   - linked issues (`Closes #123`).
6. **Keep the history clean.** Rebase on `main` and squash fixup commits. We use
   squash-merge, so the PR title must itself be a valid Conventional Commit.
7. **Respond to review.** Address feedback with new commits; do not force-push
   over an active review unless asked.
8. **CI must pass.** All required checks (test, build, lint) must be green before
   merge. A maintainer merges once the PR is approved.

### Review Expectations

- Expect an initial response within about a week. Feel free to ping the thread
  after that.
- Reviews focus on correctness, test coverage, backwards compatibility of the
  protocol, and whether the change fits the project's scope.
- Maintainers may decline changes that add configuration surface, dependencies,
  or complexity without a clear use case.

---

## Testing Requirements

Tests are required for any behavior change. A pull request that changes logic
without tests will not be merged.

- **Unit tests.** New functions and packages need table-driven tests covering
  the happy path and error paths. Place them in `*_test.go` next to the code.
- **Race detector.** CI runs `go test ./... -race`. Your tests must pass with
  `-race` enabled; anything touching the orchestrator or executor is likely
  concurrent.
- **Coverage.** Do not lower overall coverage. Run:
  ```bash
  go test ./... -race -coverprofile=coverage.out
  go tool cover -func=coverage.out
  ```
- **Integration tests.** Changes to crash injection, checkpoint handling, or
  reporting should be exercised end-to-end against the mock agent in
  `mock-agent/`. Document any Docker or cluster requirement in the PR.
- **Regression tests.** Every bug fix should include a test that fails before
  the fix and passes after it.
- **No flaky tests.** Avoid fixed `time.Sleep` waits; poll with a timeout
  instead. A flaky test is treated as a bug.

### Running a Subset

```bash
go test ./internal/executor/... -run TestInject -v
go test ./... -race -count=1
```

---

## Reporting Security Issues

Please do **not** open public issues for vulnerabilities. Follow the process in
[SECURITY.md](SECURITY.md) instead.

---

## Questions

Open an issue with the `question` label. We are happy to help you find a
starting point.
