# Security policy

## Supported versions

| Version | Supported |
| --- | --- |
| 0.7.x | Yes |
| 0.6.2 | Migration bridge only |
| Earlier 0.6.x | No |
| 0.5.x | No |
| 0.4.x | No |
| 0.3.x | No |
| 0.2.x | No |
| 0.1.x | No |
| Earlier prereleases | No |

## Report a vulnerability

Use GitHub's
[private vulnerability reporting](https://github.com/nkiyohara/corresync/security/advisories/new).
Do not include vulnerability details in a public issue, discussion, pull
request, or email thread.

Never include live tokens, cookies, message contents, personal data, corporate
information, browser profiles, screenshots, or raw Outlook payloads in an
initial report.

Maintainers will acknowledge a report, reproduce it with synthetic data, and
coordinate disclosure before publishing details.

## Security boundary

The project is intended to operate only with the interactive Outlook Web session
of the local signed-in user. It does not bypass authentication, tenant policy,
MFA, Conditional Access, or mailbox permissions.

See [the threat model](docs/threat-model.md) for the controls expected before the
first release.
