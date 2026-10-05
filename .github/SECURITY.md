# Security Policy

Dynacat is a self-hosted dashboard that talks to a lot of external services and, in most setups, holds API keys and login credentials for them. That makes security reports genuinely useful, so thanks for taking the time to send one.

## Supported Versions

Only the current major release line gets security fixes. Dynacat moves fast and older lines are not backported.

| Version | Supported          |
| ------- | ------------------ |
| 3.0.x   | :white_check_mark: |
| 2.x     | :x:                |
| < 2.0   | :x:                |

If you are running something older, upgrading is the fix. Docker users on the `latest` tag are already on a supported version.

## Reporting a Vulnerability

**Please do not open a public issue for security problems.**

Private vulnerability reporting is enabled on this repository, so that is the preferred route: open [a private advisory](https://github.com/Panonim/dynacat/security/advisories/new) and it goes straight to the maintainer without ever being public.

If GitHub is not an option for you, reach out on [Discord](https://discord.com/invite/mUqTzrfjFP) and ask for a DM with the maintainer. Do not post details in a public channel.

A good report usually has:

- the Dynacat version and how you run it (Docker, binary, reverse proxy in front, etc.)
- which widget, endpoint, or config option is involved
- steps to reproduce, ideally with a minimal `dynacat.yml`
- what an attacker actually gets out of it

Proof of concept code is welcome. A rough description is fine too - do not sit on a finding because the writeup is not polished.

## What Happens Next

This is a small project maintained by one person, so the timelines are honest rather than enterprise-grade:

- **Within 72 hours** - a reply confirming the report landed.
- **Within 7 days** - an assessment of whether it is accepted, plus a rough severity.
- **After that** - a fix lands in a release as soon as it is ready. Critical issues get a patch release of their own; lower severity ones may ride along with the next regular release.

You will be kept in the loop while it is being worked on. Once a fix is out, a GitHub Security Advisory is published and you get credit by name or handle unless you would rather stay anonymous.

If a report is declined, you get an explanation of why. Sometimes that is "this is expected behaviour" and sometimes it is "this is real but out of scope" - either way you will know which.

## Scope

In scope: Dynacat itself - the server, password and OIDC authentication, session handling, the UI editor, the built in widgets, the custom API widget, and the official Docker image.

Out of scope:

- vulnerabilities in the third party services Dynacat fetches from
- issues that need an attacker to already have your config file or server access
- exposing an unauthenticated Dynacat instance to the public internet - that is a deployment choice, not a bug
- missing hardening headers with no demonstrated impact
- automated scanner output with no working proof of concept

## Hardening Your Own Instance

Most real world incidents come down to deployment, not code. A short checklist:

- put Dynacat behind a reverse proxy with HTTPS, and turn on [authentication](https://dynacat.artur.zone/authentication) if the instance is reachable from outside your network
- store password hashes rather than plain passwords - `./dynacat password:hash <password>` generates one, and `./dynacat secret:make` generates the required `secret-key`
- if you run behind a proxy, set the proxy options under `server` so brute-force protection sees real client IPs instead of the proxy's
- keep tokens out of `dynacat.yml` using `${ENV_VAR}` or `${secret:name}` for Docker secrets
- for the Docker widgets, prefer a read only socket proxy such as [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy) over mounting `/var/run/docker.sock` directly, via the `sock-path` option
- only widen `allowed-embed-hosts` when you actually need cross-origin embedding
- keep the image or binary updated - security fixes only exist in the current release line

Details for all of the above live in the [configuration docs](https://dynacat.artur.zone/configuration).
