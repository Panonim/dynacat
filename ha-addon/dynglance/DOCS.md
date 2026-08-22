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

The add-on itself only exposes two options:

| Option      | Description                                                      |
|-------------|--------------------------------------------------------------------|
| `log_level` | `debug`, `info`, `warning`, or `error`.                            |
| `timezone`  | Optional IANA timezone (e.g. `Europe/Warsaw`) for the clock widget/logs. |

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

### Docker widgets

The add-on requests Docker socket access (`docker_api: true`), so the
`docker-containers` and `docker-controller` widgets work with their default
socket path (`/var/run/docker.sock`) without extra configuration. This grants
the add-on root-equivalent access to the host - if you don't use those
widgets, you can remove `docker_api: true` from `config.yaml` and rebuild a
local copy of the add-on for a smaller privilege footprint.

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
