# Home Assistant integration

## Viewing the dashboard via the sidebar (Ingress)

If DynGlance opens with all the page structure and content but none of the
styling (no colors, unstyled default browser buttons/checkboxes, everything
stacked full-width) when opened from the Home Assistant sidebar - but looks
fine when opened directly in its own browser tab - this was a real bug in
versions before the fix below and has been resolved: DynGlance now reads the
`X-Ingress-Path` header Supervisor sends on every Ingress request and uses it
to prefix every generated link, script, and stylesheet URL, instead of only
supporting a fixed `server.base-url` set at config time (which can't work for
Ingress anyway, since each install's `/api/hassio_ingress/<token>/` prefix is
random and only known at runtime, not when you write `dynglance.yml`). Update
to a build that includes this fix if you still see the issue - no
config change is needed for it to take effect.

DynGlance has no Home Assistant-specific widget, but you can pull data out of
Home Assistant's REST API into the `custom-api` widget with no code changes.
Because `custom-api` fetches and renders server-side, your Home Assistant
token is never sent to the browser - anyone viewing the dashboard sees only
the rendered HTML, so this works even with DynGlance's own authentication
turned off (e.g. behind the Home Assistant add-on's Ingress, or on a kiosk
display that has no login of its own).

## 1. Create a long-lived access token in Home Assistant

In Home Assistant, go to your profile (bottom left) → **Security** →
**Long-lived access tokens** → **Create Token**. Copy it somewhere safe; you
won't be able to see it again.

**Running as the Home Assistant add-on:** paste the token into the add-on's
**Configuration** tab, under **Home Assistant long-lived token** - it's never
typed into `dynglance.yml` at all. The add-on passes it through as the
`HA_TOKEN` environment variable, so your widgets reference it the same way
either way (see step 2).

**Running standalone (Docker/binary):** pass it in as an environment
variable yourself and reference it with `${env:HA_TOKEN}` (see
[Templating / variables](configuration.md#configuring-dynglance) for the
`${env:...}`/`${secret:...}` syntax) rather than pasting it directly into
`dynglance.yml`.

Either way, treat this token like a password: anyone who has it can read
(and, depending on scope, control) your Home Assistant instance. Don't
commit it to version control.

## 2. Display a single entity's state

```yaml
- type: custom-api
  title: Living Room Temperature
  cache: 30s
  url: http://homeassistant.local:8123/api/states/sensor.living_room_temperature
  headers:
    Authorization: "Bearer ${env:HA_TOKEN}"
  template: |
    <div class="size-h2">{{ .JSON.String "state" }}°{{ .JSON.String "attributes.unit_of_measurement" }}</div>
    <div class="color-highlight">{{ .JSON.String "attributes.friendly_name" }}</div>
```

Replace `homeassistant.local:8123` with your instance's address (if
DynGlance is running as the Home Assistant add-on, the Supervisor's internal
hostname works instead - see step 4).

## 3. Display several entities in one widget

Use `subrequests` to fetch multiple entities concurrently and combine them in
a single widget, instead of adding one `custom-api` widget per entity:

```yaml
- type: custom-api
  title: Home
  cache: 30s
  headers: &ha-auth
    Authorization: "Bearer ${env:HA_TOKEN}"
  url: http://homeassistant.local:8123/api/states/sensor.living_room_temperature
  subrequests:
    humidity:
      url: http://homeassistant.local:8123/api/states/sensor.living_room_humidity
      headers: *ha-auth
    front_door:
      url: http://homeassistant.local:8123/api/states/binary_sensor.front_door
      headers: *ha-auth
  template: |
    {{ $humidity := .Subrequest "humidity" }}
    {{ $door := .Subrequest "front_door" }}
    <div class="flex justify-between">
      <span>Temperature</span>
      <span>{{ .JSON.String "state" }}°{{ .JSON.String "attributes.unit_of_measurement" }}</span>
    </div>
    <div class="flex justify-between">
      <span>Humidity</span>
      <span>{{ $humidity.JSON.String "state" }}%</span>
    </div>
    <div class="flex justify-between">
      <span>Front door</span>
      <span>{{ if eq ($door.JSON.String "state") "on" }}Open{{ else }}Closed{{ end }}</span>
    </div>
```

See [custom-api.md](custom-api.md) for the full templating reference
(loops, conditionals, formatting helpers) and the `subrequests` property
documentation in [configuration.md](configuration.md#subrequests).

## 4. Why there's no "no-token" shortcut anymore

Earlier builds of the add-on set `homeassistant_api: true`, which let the
container skip the token entirely and call `http://supervisor/core/api/...`
using an auto-injected `SUPERVISOR_TOKEN`. That's been removed: it's a
standing network-access grant to Home Assistant's Core API that isn't scored
by Home Assistant's add-on security rating but is still a real privilege,
and the token-based method above already covers the same need with one extra
step (pasting a token into the Configuration tab) instead of a permanent
grant. Use `${env:HA_TOKEN}` against your instance's normal address
(`http://homeassistant.local:8123/api/...` or similar) as shown above.

## 5. Uploading/replacing dynglance.yml from the Configuration tab

The `/config-upload` page (see [Config Upload](authentication.md#config-upload))
is also a Configuration-tab toggle when running as the add-on: turn on
**Config Upload** and set a **Config Upload passphrase** (12+ characters),
and the passphrase-gated page becomes available with no YAML editing at all
- the add-on manages the `config-upload:` section of `dynglance.yml` for
you. Leaving the passphrase blank (or under 12 characters) keeps it disabled
even if the toggle is on, so a half-filled-in option can't accidentally
start the app with a broken config.

## 6. Referencing config files that live in your Home Assistant config folder

The add-on maps Home Assistant's own config directory into the container,
read-only, at `/homeassistant`. Combined with `dynglance.yml`'s `$include`
directive, this lets you keep widget definitions alongside the rest of your
Home Assistant config (e.g. under a `dynglance/` folder you already sync or
back up) instead of duplicating them into the add-on's own `/config`:

```yaml
# in /addon_configs/<slug>/dynglance.yml
pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - $include: /homeassistant/dynglance/home-widgets.yml
```

`/homeassistant/dynglance/home-widgets.yml` is then just a plain YAML list of
widgets (or any other includable fragment), edited directly from the Home
Assistant `/config` folder via the File editor/Studio Code Server add-ons,
Samba, or an automation that regenerates it. Included files are watched the
same as `dynglance.yml` itself, so edits apply automatically without
restarting the add-on.

If you're running the standalone Docker container instead of the add-on,
the equivalent is mounting the folder yourself, e.g.
`-v /path/to/ha/config:/homeassistant:ro`.

## 7. Pushing data from Home Assistant instead of polling

The recipes above are pull-based: DynGlance polls Home Assistant on
`cache`'s schedule (as low as a few seconds, though very short intervals add
load to both sides). There is currently no push/webhook-receiver widget that
would let a Home Assistant automation notify DynGlance the instant something
changes - polling with a short `cache` is the supported approach today. A
generic webhook-ingest widget is a reasonable feature request; see the
project's issue tracker if you'd like to propose one.

## Security rating

The add-on requests `docker_api: true` so the `docker-containers` and
`docker-controller` widgets work. Per Home Assistant Supervisor's own rating
logic (`supervisor/apps/utils.py`, current as of Supervisor's `main` branch):

```python
# Docker Access & full Access
if app.access_docker_api or app.with_full_access:
    rating = 1
```

This is an unconditional override - it forces the rating to 1 regardless of
anything else the add-on does right (Ingress, a shipped AppArmor profile,
no other elevated privileges, etc. all still apply, they just can't move the
number while this override is in effect). There is no config.yaml option,
schema field, or Supervisor REST API that makes `docker_api` conditional -
it's read once from the add-on's manifest at install/update time.

Two things worth knowing if you don't need the Docker widgets:
- **Removing `docker_api: true` and rebuilding a local copy** of the add-on
  is the only way to change the *number*. With it removed (and the AppArmor
  profile and Ingress this add-on already has), the rating reaches the
  maximum of 8 (baseline 5 + Ingress's +2 + AppArmor's +1).
- **Protection mode** (Settings → Add-ons → DynGlance → Info tab, or
  `POST /apps/{slug}/security` / `POST /addons/{slug}/security` with
  `{"protected": false}`) is a real, existing, per-installation toggle -
  but it does **not** affect the rating number, which reflects what the
  add-on's manifest *requests*, not what's currently granted. What it does
  affect is whether the Docker socket is actually mounted at runtime
  (`supervisor/docker/app.py`: `if not app.protected and app.access_docker_api:
  mounts.append(MOUNT_DOCKER)`) - with protection mode left on (the
  default), the socket isn't mounted even though `docker_api: true` is
  declared, so the Docker widgets won't work until you turn it off. Note
  this also means simply toggling protection mode back on later doesn't
  retroactively re-secure anything already running - it takes effect on the
  next container recreate, not the next start/stop.
