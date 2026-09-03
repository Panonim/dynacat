# Operations

This file documents operational knowledge for maintaining this fork (release
process, CI) that doesn't belong in the user-facing DynGlance documentation
under `docs/docs/`.

## Release tags

Release tags are the version itself, formatted `YYYY.MM.DD.V`: the calendar
date the release was cut, and a 1-based counter for however many releases
happened that day (`2026.08.23.1`, then `2026.08.23.2` for a second release
the same day).

To cut a new release: check `git tag -l "$(date -u +%Y.%m.%d).*"` for the
highest existing `V` today, then tag and push the next one. The `deploy.yml`
workflow (`.github/workflows/deploy.yml`) builds and publishes container
images on tag push and on pushes to the `beta` branch.

The `version:` field in `ha-addon/dynglance/config.yaml` and the
`APP_VERSION` default in `ha-addon/dynglance/Dockerfile` are bumped by hand
and must stay in sync with each other; nothing automates that today.
