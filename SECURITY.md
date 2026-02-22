# Security Policy

## Supported Versions

| Version | Supported          |
|---------|--------------------|
| latest  | Yes                |
| < latest | No                |

We only provide security patches for the latest release. We recommend always running the most recent version.

## Reporting a Vulnerability

We take security seriously. If you discover a vulnerability in mautrix-mattermost, please report it responsibly.

### How to Report

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please use **GitHub Security Advisories** (preferred): Go to the [Security Advisories](https://github.com/aiku/mautrix-mattermost/security/advisories) page and click "Report a vulnerability".

### What to Include

- Description of the vulnerability
- Steps to reproduce
- Affected versions
- Potential impact
- Suggested fix (if any)

### Response Timeline

- **Acknowledgment**: Within 48 hours of receipt
- **Initial Assessment**: Within 5 business days
- **Fix Timeline**: Depends on severity
  - Critical: Patch within 7 days
  - High: Patch within 14 days
  - Medium/Low: Included in next scheduled release

### Scope

This security policy covers the mautrix-mattermost bridge itself. Issues in upstream dependencies should be reported to their respective maintainers:

- **Mattermost**: [Mattermost Responsible Disclosure](https://mattermost.com/security-vulnerability-report/)
- **Matrix/Synapse**: [Matrix Security](https://matrix.org/security-disclosure-policy/)
- **mautrix/go**: [mautrix GitHub](https://github.com/mautrix/go)

### Disclosure

We follow coordinated disclosure. We will work with you to understand the issue and agree on a disclosure timeline before any public announcement.
