# Contributing to Corresync

Corresync accepts focused fixes, documentation improvements, provider contract
work, and narrowly designed mail or calendar capabilities. Start with an issue
when a change affects public behavior, protocol support, policy, configuration,
or architecture.

Read [AGENTS.md](AGENTS.md) before changing code. It defines the project scope
and non-negotiable security boundaries. Accepted design decisions live in
[the ADR directory](docs/adr/).

## Before writing code

1. Check the [feature matrix](docs/features.md) and
   [roadmap](https://github.com/nkiyohara/corresync/issues/20).
2. Open or find an issue that states the user outcome and safety boundary.
3. Add or update an ADR when changing an accepted architectural decision.
4. Decide how the behavior will be proven with synthetic data before using a
   live mailbox.

Do not describe a provider or capability as implemented until it has typed
application contracts and synthetic fixtures. Do not describe it as
live-compatible until it also has the evidence required by
[compatibility.md](docs/compatibility.md).

## Architecture rules

- Dependencies point inward. Domain and application packages do not import CLI,
  MCP, browser, IPC, or provider adapters.
- CLI and MCP call the same typed application use cases.
- Provider adapters translate protocols; they do not own effect policy.
- Authentication stays interactive and user-owned: a visible browser for web
  and OAuth routes, or an explicitly approved OS-keyring/helper reference for
  standards routes. Never accept or persist passwords, cookies, bearer tokens,
  canaries, OAuth grants, or helper results in configuration.
- Consequential writes keep the server-enforced, target-bound
  `preview -> commit` protocol.
- Unknown write outcomes fail closed and are never retried automatically.
- Fixtures and examples use synthetic identities and contain no personal data.

See [architecture.md](docs/architecture.md) for the component map and
[threat-model.md](docs/threat-model.md) for required controls.

## Developer setup

The repository uses [mise](https://mise.jdx.dev/) to pin Go and every
verification tool on macOS, Linux, and Windows:

```console
mise trust
mise exec -- task setup
mise exec -- task build
mise exec -- task verify
```

Go 1.26 is the minimum supported compiler. The checked-in toolchain follows the
latest pinned Go 1.26 patch instead of downloading an undeclared compiler.
Setup also installs repository-managed prek hooks. From then on, changed
Go, HTML, CSS, YAML, JSON, and TOML files are formatted automatically before a
commit completes; safe whitespace and merge-conflict checks cover every text
file. Run `mise exec -- task format` to apply the same formatters explicitly or
`mise exec -- task hooks:run` to check the complete tree.

`task verify` checks:

- Go and structured-file formatting plus Markdown lint;
- vet, golangci-lint, unit tests, and the race detector;
- repository-history and working-tree secret scans;
- reachable vulnerabilities and linked dependency licenses;
- the binary and GoReleaser configuration;
- GitHub Actions syntax and security policy.

Run the complete command before committing. Live mailbox tests are deliberately
absent from it.

## Make a focused change

- Keep commits narrow and use a
  [Conventional Commit](https://www.conventionalcommits.org/) subject.
- Add domain/application tests before exposing new transport behavior.
- Put protocol examples in synthetic fixtures; never paste a captured live
  response.
- Update CLI help, MCP descriptions, stable JSON docs, and the feature matrix
  together when a public operation changes.
- Preserve unrelated changes in a dirty worktree.

For a new provider, implement an application port and capability contract
before its adapter. Discovery must remain credential-free, explicit selection
must precede authentication, and automatic discovery must not trigger
administrator review or consent.

For monitoring or import work, treat durable queue/staging state as private
account-local data. Collection, runner execution, content inclusion, and remote
egress remain separate consent boundaries. An automatic event may never grant
itself write authority.

## Optional live observations

Use live testing only when the issue requires compatibility evidence and you
are authorized to use the account and device. Follow the
[manual test checklist](docs/manual-test-checklist.md).

Never commit or attach:

- tenant names, mailbox addresses, or personal identifiers;
- message, event, folder, attachment, or request IDs;
- bodies, subjects, recipients, screenshots, or raw payloads;
- browser profiles, cookies, tokens, authorization headers, or canaries.

A useful report contains only the Corresync version, operating system,
architecture, browser family/version, deployment class, observation date, and
content-free success or failure stage.

## Documentation changes

Start from the [documentation map](docs/README.md). Keep examples synthetic,
copy-pasteable, and truthful about what ships today. Historical changelog
entries and accepted ADR decisions should not be rewritten to look current;
add a new decision or current guide instead.

Run the local Markdown check while editing:

```console
mise exec -- task docs
```

## Pull request checklist

- The issue and user-visible outcome are linked.
- Architecture and security invariants remain intact.
- Tests cover success, bounds, malformed input, and ambiguous outcomes.
- Documentation and stable contracts match the implementation.
- Fixtures are synthetic and contain no secrets or personal data.
- `mise exec -- task verify` passes.

Suspected vulnerabilities must use
[private vulnerability reporting](https://github.com/nkiyohara/corresync/security/advisories/new),
not a public issue or pull request.

## GitHub metadata

Human-authored issues and pull requests use exactly one category label:
`bug`, `enhancement`, `documentation`, `security`, `testing`, `release`,
`roadmap`, or `compatibility`. Add only the `area:` labels needed to identify
the affected product surface. Automation labels such as `dependencies`, `go`,
and `github_actions` are reserved for dependency-update pull requests.

Release work belongs to the milestone whose acceptance criteria it delivers.
Standalone maintenance and dependency updates may omit a milestone. Milestones
do not carry due dates: a release is ready when its tracked scope, verification,
documentation, and security review are complete.

Pull requests close or reference their tracking issue. A standalone maintenance
pull request explains why no issue or release milestone is needed.
