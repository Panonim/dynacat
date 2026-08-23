# Changelog

Versions are dated `YYYY.MM.DD.V`: the calendar date a release was cut, and
a 1-based counter for however many releases happened that day.

## 2026.08.23.3

- `docker_api` is now off by default - the `server-stats` widget never
  needed it (reads `/proc`/`/sys`/`gopsutil` directly, not Docker), and
  only `docker-containers`/`docker-controller` do. With it off, the add-on
  reaches the maximum Home Assistant security rating of 8. Set
  `docker_api: true` back and rebuild locally if you use those two
  widgets.

## 2026.08.23.2

- Dropped `homeassistant_api: true`; added a `home_assistant_token`
  Configuration option (passed through as `HA_TOKEN`) so a Home Assistant
  long-lived token never has to be typed into `dynglance.yml`.
- `/config-upload` is now a Configuration toggle (`config_upload_enabled`
  + `config_upload_password`) instead of something you hand-edit into
  `dynglance.yml`.
- Added a custom AppArmor profile (`apparmor.txt`).

## 2026.08.23.1

- Initial Home Assistant add-on release, built from source against the
  `main` branch of https://github.com/trooperthorn/ha_app_dynglance.
- Ingress support (including a fix for asset/API links breaking when
  opened from the Home Assistant sidebar), persistent `/config`
  (dynglance.yml) and `/data` (cache/database) mappings, Docker socket
  access for the Docker widgets, and an optional passphrase-gated
  `/config-upload` page for replacing the config from the browser.
