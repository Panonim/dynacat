# Changelog

Versions are dated `YYYY.MM.DD.V`: the calendar date a release was cut, and
a 1-based counter for however many releases happened that day.

## 2026.08.23.1

- Initial Home Assistant add-on release, built from source against the
  `main` branch of https://github.com/trooperthorn/ha_app_dynglance.
- Ingress support (including a fix for asset/API links breaking when
  opened from the Home Assistant sidebar), persistent `/config`
  (dynglance.yml) and `/data` (cache/database) mappings, Docker socket
  access for the Docker widgets, and an optional passphrase-gated
  `/config-upload` page for replacing the config from the browser.
