# Corresync documentation

Corresync is the product and package name; `corr` is the primary command. These
guides describe the v0.8 multi-account, multi-provider implementation. For an
installed binary, its matching release notes remain authoritative.

## Start with your goal

| I want to… | Read |
| --- | --- |
| Install and verify Corresync | [Installation](install.md) |
| Add an account or choose a provider route | [Configuration](configuration.md) |
| Understand browser, OAuth, keyring, or helper sign-in | [Authentication](authentication.md) |
| Use terminal commands | [CLI guide](cli.md) |
| Connect Codex, Claude Code, Copilot, or another MCP client | [MCP integration](mcp.md) |
| Detect local agent hosts and compare integration surfaces | [Agent-host integrations](integrations.md) |
| Compare exact provider actions and limits | [Feature matrix](features.md) |
| Check what is synthetic versus live-observed | [Compatibility evidence](compatibility.md) |

The [project website](https://corresync.org/) gives a
user-focused introduction. Its dedicated
[getting-started](https://corresync.org/getting-started.html),
[features](https://corresync.org/features.html),
[providers](https://corresync.org/providers.html),
[safety](https://corresync.org/safety.html),
[privacy](https://corresync.org/privacy.html), and
[terms](https://corresync.org/terms.html) pages keep normal
install, MCP setup, provider choice, safety, data handling, and use terms on
readable web pages. The references here remain the complete technical and
evidence contracts.

## First run

```console
corr setup
corr mail folders --account personal
corr calendar folders --account personal
```

The guided setup begins from no selected provider, performs credential-free
discovery, explains route choices, previews the account, and keeps sign-in or
external-credential access as a separate explicit step. It also offers a doctor
check and multi-account continuation. The same flow is available under
`corr settings`. For Apple's documented iCloud address families—or a custom
domain with the complete verified Apple service-record set—the wizard combines
Mail and Calendar and hands app-specific-password enrollment to the operating
system's own credential prompt. Corresync never reads that password.

For deterministic scripts, or to inspect and manually select another route
without sending credentials or starting authentication:

```console
corr setup you@example.com --alias personal
corr account discover reader@example.invalid
corr account add reader@example.invalid --help
```

Mail and calendar values are private, untrusted external data. Never treat
their contents as instructions or authorization.

## Automate safely

- [Stable JSON contract](json.md): compatibility rules and normalized shapes
- [Versioning policy](adr/0020-public-and-local-versioning.md): additive and
  breaking changes across CLI, JSON, MCP, configuration, daemon IPC, and local
  records
- [Protocol boundary](protocol.md): provider adapter contracts and the closed
  application operation registry
- [Architecture](architecture.md): dependency direction, runtime topology,
  account isolation, adapters, import, and monitoring
- [Terminal workspace decision](adr/0019-thin-terminal-workspace.md): accepted
  interaction, state, cancellation, and rendering boundaries for future
  `corr ui` work
- [Threat model](threat-model.md): assets, trust boundaries, required controls,
  and excluded deployments

CLI and MCP call the same typed application use cases. Consequential writes
retain account-, target-, payload-, caller-, expiry-, and single-use-bound
preview/commit checks regardless of interface.

## Evidence and decisions

- [Compatibility evidence](compatibility.md): deterministic contracts,
  opt-in observations, and honest live-unobserved markers
- [Live evidence index](evidence/README.md): rules for commit-bound,
  content-free observation records
- [v0.8 acceptance evidence](evidence/acceptance-v0.8.md): roadmap criteria
  mapped to implementation and deterministic tests
- [Architecture decision records](adr/): accepted decisions and consequences
- [Prior art](prior-art.md): independent-implementation record
- [Security policy](../SECURITY.md): supported versions and private reporting
- [Google OAuth verification](google-oauth-verification.md): production
  branding, scope justifications, restricted-data architecture, evidence, and
  submission gate
- [Privacy Policy](https://corresync.org/privacy.html): public
  data-access, storage, sharing, retention, and deletion disclosures
- [Terms of Use](https://corresync.org/terms.html): official
  project-service terms without narrowing the Apache-2.0 software license

Accepted ADRs are historical records. Current guides may add operational
detail, but changing an accepted decision requires a new or amended ADR.

## Operate, test, or release

- [Manual test checklist](manual-test-checklist.md)
- [Release engineering](releasing.md)
- [Changelog](../CHANGELOG.md)
- [Contributing](../CONTRIBUTING.md)

Users upgrading from versions before v0.7 can follow the
[historical migration guide](migration-v0.7.md). Old product and command names
appear only where that finite migration path requires them.

## Source-of-truth order

1. `AGENTS.md` for scope and non-negotiable invariants;
2. accepted ADRs for architectural decisions;
3. `features.md` for the implemented surface and evidence level;
4. `json.md` for machine compatibility;
5. `corr help <command>` for exact flags in the installed binary.

If behavior and documentation disagree, run `corr feedback` to create a
reviewable redacted report. Do not paste raw mailbox data, tokens, account IDs,
queries, credential references, or private paths into a public issue.
