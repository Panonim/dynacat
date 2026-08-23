# DynGlance Home Assistant add-on

DynGlance ([repository](https://github.com/trooperthorn/ha_app_dynglance), a fork of
[Glance](https://github.com/glanceapp/glance)) packaged as a Home Assistant
add-on. It runs the same container as the standalone Docker image, wired up
for Supervisor: Ingress, persistent config/cache, and optional Docker socket
access for the Docker widgets.

## Installation

1. In Home Assistant, go to **Settings → Add-ons → Add-on Store**.
2. Open the **⋮** menu (top right) → **Repositories**, and add:
   `https://github.com/trooperthorn/ha_app_dynglance`
3. Find **DynGlance** in the store and click **Install**. The first build
   compiles the Go binary from source and can take a few minutes.
4. Start the add-on, then open it from the sidebar (Ingress) or via
   `http://<home-assistant-ip>:8080`.

## Configuration

The add-on exposes these options on its **Configuration** tab:

| Option | Description |
|---|---|
| `log_level` | `debug`, `info`, `warning`, or `error`. |
| `timezone` | Optional IANA timezone (e.g. `Europe/Warsaw`) for the clock widget/logs. |
| `home_assistant_token` | Optional Home Assistant long-lived access token, passed through as the `HA_TOKEN` env var so `custom-api` widgets can use `${env:HA_TOKEN}` - see [Displaying Home Assistant data](#displaying-home-assistant-data-on-the-dashboard). |
| `config_upload_enabled` | Turns the passphrase-gated `/config-upload` page on or off - see [Config Upload](#config-upload). |
| `config_upload_password` | Passphrase for that page (12+ characters, required if the toggle above is on). |

Everything else - pages, columns, widgets, theming, authentication - is
configured the same way as upstream DynGlance/Glance: through
`dynglance.yml`. On first start the add-on creates a minimal starter file at:

```
/addon_configs/<addon_slug>/dynglance.yml
```

which is reachable from the **File editor** or **Studio Code Server**
add-ons as `/config` (Note: this is the *add-on's* `/config`, distinct
from Home Assistant's own `/config`). Edit it and restart the add-on to
apply changes, or rely on DynGlance's built-in config file watcher, which
reloads most changes automatically without a restart.

See the full configuration reference at
<https://github.com/trooperthorn/ha_app_dynglance/blob/main/docs/docs/configuration.md>.

### Three ways to get a config onto the add-on

1. **Reference files already in your Home Assistant config** - the add-on
   mounts Home Assistant's `/config` read-only at `/homeassistant` inside the
   container, so `dynglance.yml` can `$include` YAML files you keep there
   (e.g. under a `dynglance/` folder), instead of duplicating them. See
   [docs/docs/home-assistant.md](https://github.com/trooperthorn/ha_app_dynglance/blob/main/docs/docs/home-assistant.md#5-referencing-config-files-that-live-in-your-home-assistant-config-folder).
2. **Edit the file directly** via the File editor/Studio Code Server add-ons,
   as described above.
3. **Upload or drag-and-drop from the browser** - see [Config Upload](#config-upload)
   below; turned on and off entirely from the Configuration tab, no YAML
   editing required.

### Displaying Home Assistant data on the dashboard

Set the **Home Assistant long-lived token** option (Configuration tab) to a
token from your profile (**Security** → **Long-lived access tokens** →
**Create Token**). It's passed through as the `HA_TOKEN` env var - never
typed into `dynglance.yml`, and never sent to the browser either, since
`custom-api` fetches and renders server-side:

```yaml
- type: custom-api
  title: Living Room Temperature
  cache: 30s
  url: http://homeassistant.local:8123/api/states/sensor.living_room_temperature
  headers:
    Authorization: "Bearer ${env:HA_TOKEN}"
  template: |
    <div class="size-h2">{{ .JSON.String "state" }}°{{ .JSON.String "attributes.unit_of_measurement" }}</div>
```

Replace `homeassistant.local:8123` with your instance's address. See
[docs/docs/home-assistant.md](https://github.com/trooperthorn/ha_app_dynglance/blob/main/docs/docs/home-assistant.md)
for more examples (multiple entities via `subrequests`, etc).

### Config Upload

Set **Config Upload** to on and give it a **Config Upload passphrase**
(12+ characters) on the Configuration tab, and a passphrase-gated
`/config-upload` page becomes available with a file picker and a drop zone
for replacing `dynglance.yml` or adding an `$include` fragment - no YAML
editing needed to turn it on. This passphrase is entirely separate from
DynGlance's own dashboard auth (if any). Leaving the passphrase blank or
under 12 characters keeps the page disabled even with the toggle on, so a
half-filled-in option can't start the app with a broken config. See
[docs/docs/authentication.md#config-upload](https://github.com/trooperthorn/ha_app_dynglance/blob/main/docs/docs/authentication.md#config-upload)
for the full details, including how it interacts with a hand-written
`config-upload:` section if you already have one.

### Authentication

If you add an `auth:` section to `dynglance.yml`, you need a secret key. Since
Ingress traffic only reaches the add-on over Supervisor's internal Docker
network (already gated by your Home Assistant login), most users leave
DynGlance's own auth disabled and rely on Ingress/HA authentication instead.
If you do want DynGlance-level auth (e.g. because you also expose port 8080
directly), generate a key with:

```
docker exec -it addon_dynglance dynglance secret:make
```

and put the result in `auth.secret-key`.

### Docker widgets and the security rating

The add-on requests Docker socket access (`docker_api: true`), so the
`docker-containers` and `docker-controller` widgets work with their default
socket path (`/var/run/docker.sock`) without extra configuration. This grants
the add-on root-equivalent access to the host, and per Supervisor's own
rating logic it unconditionally forces the add-on's Home Assistant security
rating to 1 (the minimum) - no combination of other settings can offset it;
see [docs/docs/home-assistant.md#security-rating](https://github.com/trooperthorn/ha_app_dynglance/blob/main/docs/docs/home-assistant.md#security-rating)
for exactly how that's calculated, including a real (if partial) mitigation:
Home Assistant's per-install **Protection mode** toggle (Settings → Add-ons
→ DynGlance → Info tab) determines whether the Docker socket is actually
mounted at runtime, independent of the rating number. If you don't use the
Docker widgets, removing `docker_api: true` from `config.yaml` and rebuilding
a local copy of the add-on is the only way to change the number itself.

This add-on also ships a custom AppArmor profile (`apparmor.txt`), which
Supervisor loads automatically and which is worth +1 on top of whatever the
Docker-access override allows.

### Networking / Ingress

The add-on listens on port 8080 and is exposed both through Home Assistant
Ingress (no extra port needed) and, optionally, directly on port 8080 if you
want to reach it without going through Home Assistant (e.g. from another
service on your network). If you don't need direct access, you can disable
the port mapping in the add-on's **Network** tab.

## Persistent data

- `/config` (mapped to the add-on's `addon_config` share) holds `dynglance.yml`
  and anything else you put next to it (custom CSS, a `favicon`, etc.) - this
  survives add-on updates/reinstalls.
- `/data` (always private to the add-on) holds the image cache and the
  to-do widget's SQLite database, so restarting the add-on doesn't lose them.

## Known limitations / reliability notes

This container has historically been reported to occasionally stop
responding under low activity (e.g. overnight) until something unrelated
"wakes" it back up. If you were relying on a manual restart to fix this,
update to a build of this add-on/image that includes the fix described in
the project's `CHANGELOG.md` / commit history: a page-wide lock could be
held indefinitely if a single widget's outbound HTTP request hung longer
than its own client timeout, freezing every request and Ingress update for
that page until the process was restarted. Widget update batches are now
hard-capped at 25 seconds so a single misbehaving widget can no longer wedge
the whole page.

If you still see the add-on become unresponsive, check the add-on's log for
widgets that time out repeatedly (a slow RSS feed, an unreachable Docker
host, etc.) and either fix/remove that widget or increase its `cache`/
`update-interval` so it's retried less aggressively.
