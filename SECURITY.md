# Security policy

sandboxd exists to run untrusted code, so isolation bugs are taken seriously.

## Reporting a vulnerability

**Please do not open a public issue.** Report privately through
[GitHub security advisories](https://github.com/bpradana/sandboxd/security/advisories/new).

Include what you can of:

- the affected version or commit
- the host kernel version, distribution, and how sandboxd runs
- a description of the issue and its impact (for example, a sandbox escape, reading host
  files, reaching blocked network ranges, or affecting other sandboxes)
- steps or a proof of concept to reproduce it

You can expect an acknowledgement within a few days. Fixes for confirmed issues are
released as soon as practical, with credit to the reporter unless you prefer otherwise.

## Scope

In scope: anything that lets code inside a sandbox affect the host, the daemon, or other
sandboxes beyond what the [security model](README.md#security-model) allows. That
includes escapes through the file API, the agent protocol, terminals, networking,
resource limits, or image import, and API authentication or authorization bypasses.

Out of scope:

- Vulnerabilities in the Linux kernel itself. sandboxd shares the host kernel by design;
  report those upstream. Mitigations sandboxd could add are welcome as regular issues.
- Denial of service within a sandbox's own configured limits.
- Deployments that ignore the documented production configuration (for example, no API
  keys, or `/ui/` exposed publicly).

## Supported versions

Security fixes go into the latest release. Older releases are not patched.
