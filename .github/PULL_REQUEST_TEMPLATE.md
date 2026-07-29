# Pull request

## Summary

Describe the user-visible behavior and the typed application use case changed.

## Safety boundary

State the operation effect (`read`, `sensitive_read`, `reversible_write`,
`external_write`, or `destructive_write`) and explain any preview/commit,
retry, audit, or session-boundary impact.

## Verification

List the exact commands and synthetic fixtures used to verify the change.

## Checklist

- [ ] The PR has one category label and only the relevant `area:` labels.
- [ ] The PR closes or references an issue, or explains why it is standalone maintenance.
- [ ] A release milestone is assigned when the PR delivers milestone scope.
- [ ] Tests and examples use only synthetic identities and content.
- [ ] No credential, mailbox data, browser profile, or live capture is present.
- [ ] CLI and MCP call the same application use case.
- [ ] Writes have an explicit unknown-outcome and retry policy.
- [ ] Protocol changes include bounded, synthetic contract fixtures.
- [ ] `mise exec -- task verify` passes.
- [ ] User-facing behavior and compatibility evidence are documented.
