# Home Assistant integration

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

Treat this token like a password: anyone who has it can read (and, depending
on scope, control) your Home Assistant instance. Pass it in as an environment
variable and reference it with `${env:HA_TOKEN}` (see
[Templating / variables](configuration.md#configuring-dynglance) for the
`${env:...}`/`${secret:...}` syntax) rather than pasting it directly into
`dynglance.yml`, and don't commit it to version control.

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

## 4. Using the Supervisor API instead of a long-lived token

If DynGlance is running as the Home Assistant add-on, Supervisor injects a
`SUPERVISOR_TOKEN` environment variable into the add-on's container and
exposes the Core API at `http://supervisor/core/api/...` - no manually
created long-lived token needed:

```yaml
url: http://supervisor/core/api/states/sensor.living_room_temperature
headers:
  Authorization: "Bearer ${env:SUPERVISOR_TOKEN}"
```

## 5. Referencing config files that live in your Home Assistant config folder

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

## 6. Pushing data from Home Assistant instead of polling

The recipes above are pull-based: DynGlance polls Home Assistant on
`cache`'s schedule (as low as a few seconds, though very short intervals add
load to both sides). There is currently no push/webhook-receiver widget that
would let a Home Assistant automation notify DynGlance the instant something
changes - polling with a short `cache` is the supported approach today. A
generic webhook-ingest widget is a reasonable feature request; see the
project's issue tracker if you'd like to propose one.
