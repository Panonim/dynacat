# Docker Configuration & Options
## Environment Variables

Environment variables can be set in the `docker-compose.yml` file or through a `.env` file. These control runtime behavior of Dynacat without requiring changes to the configuration file.

### LOG_LEVEL

Controls the verbosity of logging output. This is useful for debugging issues or reducing log noise when caching operations fail.

**Valid values:**

| Level | Description |
| ----- | ----------- |
| `ERROR` | Only errors are logged (minimal output) |
| `WARN` | Warnings and errors are logged |
| `INFO` | General information, warnings, and errors (default) |
| `DEBUG` | Detailed debugging information, including all API requests and responses |

**Example:**

```yaml
environment:
  - LOG_LEVEL=INFO
```

By default, when image caching fails or widgets fail to update, warning messages are displayed. If you want to suppress these messages and only see critical errors, you can set `LOG_LEVEL=ERROR`:

```yaml
environment:
  - LOG_LEVEL=ERROR
```

For troubleshooting widget updates or API issues, use `LOG_LEVEL=DEBUG`:

```yaml
environment:
  - LOG_LEVEL=DEBUG
```

> [!NOTE]
>
> Changes to `LOG_LEVEL` require restarting the container. Unlike configuration file changes, environment variable changes do not trigger a hot reload.

### BIND

The address the server listens on. By default, this is set to `0.0.0.0` which allows access from any interface.

**Example:**

```yaml
environment:
  - BIND=0.0.0.0
```

To restrict access to only localhost (for local development), use:

```yaml
environment:
  - BIND=127.0.0.1
```

### ENABLE_EDITOR

Permanently disables the interactive page editor. Set it to `false`, `0`, `f`, `no`, or `off` to hide the edit button on every page, stop the editor assets from loading, and unregister the `/api/editor/*` endpoints:

```yaml
environment:
  - ENABLE_EDITOR=false
```

Editor-specific configuration (`server.allow-editing`, `server.editing-users`, `server.editing-groups`, and any user's `restrict-editing`) stays valid YAML and is **not** an error - it is simply ignored. On startup Dynacat logs a single warning naming the settings it skipped.


> [!NOTE]
>
> With the editor disabled, the config file is the only way to change pages. Requests to the editor API return `404` instead of `403`.

### EDITOR_SEPARATE_PAGE_FILES

Controls where the interactive page editor stores a new page. By default each new page is written to its own file (e.g. `my-page.yml`) in the config directory and linked into the main config with a `$include` line, which keeps multiple pages easy to manage.

Set it to `false`, `0`, or `f` to write new pages inline into the main config file (`dynacat.yml`) instead:

```yaml
environment:
  - EDITOR_SEPARATE_PAGE_FILES=false
```

> [!NOTE]
>
> This only affects pages created after the change. Existing pages, whether inline or included, keep their current location.

### HOST_ETC

Points the `server-stats` widget at a bind-mounted copy of the host's `/etc` directory so it reports the host's OS instead of the container image's (usually `alpine`). Dynacat reads `$HOST_ETC/os-release`.

This is an alternative to mounting the file directly to `/host/etc/os-release` (see [Server Stats](configuration.md#server-stats)) - useful if you'd rather bind mount the whole host `/etc` to a path of your choosing:

```yaml
environment:
  - HOST_ETC=/host-etc
volumes:
  - /etc:/host-etc:ro
```

## Dynamic Refreshing

Dynamic refreshing allows widgets to automatically update their data at specified intervals. This behavior can be controlled through two mechanisms:

### Via Configuration File

To disable dynamic refreshing globally, you can configure the update interval on individual widgets or use the `update-interval` property set to a very large value or remove it entirely:

```yaml
pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: monitor
            sites:
              - title: Example
                url: https://example.com
            update-interval: 0s  # Disables automatic updates
```

### Browser-side Control

The global page update interval can be disabled by:

1. Setting `update-interval` on individual widgets to control their refresh rate
2. Using `0s` to disable updates for specific widgets
3. Removing the property entirely to use server-side defaults

> [!TIP]
>
> If you want to reduce server load and network traffic, increase the `update-interval` values for widgets that don't need frequent updates, or set it to `0s` to disable polling entirely.

> [!NOTE]
>
> Some widgets like monitor and docker-containers have built-in default update intervals (typically 2 minutes) that are used if `update-interval` is not specified.

### Disable automatic updates

| Variable | Default | Description |
| -------- | ------- | ----------- |
| `ENABLE_DYNAMIC_UPDATE` | `true` | Set to `false`, `0`, or `f` to disable automatic widget refresh. Useful for static views or default glance behaviour. |
| `EDITOR_SEPARATE_PAGE_FILES` | `true` | Set to `false`, `0`, or `f` to write new pages inline into the main config instead of a separate `$include` file. |
| `ENABLE_EDITOR` | `true` | Set to `false`, `0`, `f`, `no`, or `off` to disable the page editor everywhere. Editor config keys are ignored with a startup warning. |

## ZFS Mountpoint Support

By default, the `server-stats` widget uses `statfs()` to read disk usage. On ZFS pool roots (e.g. TrueNAS SCALE), `statfs()` returns `Used = 0` because data lives in child datasets, not at the pool root. Dynacat works around this by calling `zfs list` when a ZFS filesystem is detected.

The `zfs` binary is included in the Dynacat image but **requires `/dev/zfs` to be passed to the container** to function. Without it, the binary is inert and poses no security risk.

### Enabling ZFS stats

Add the following to your `docker-compose.yml`:

```yaml
services:
  dynacat:
    image: panonim/dynacat:latest
    devices:
      - /dev/zfs:/dev/zfs
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    volumes:
      - /mnt/POOLNAME:/mnt/POOLNAME:ro  # repeat for each pool
```

Then configure the mountpoints in `dynacat.yml`:

```yaml
- type: server-stats
  servers:
    - type: local
      name: TrueNAS
      hide-mountpoints-by-default: true
      mountpoints:
        "/mnt/POOLNAME":
          name: POOLNAME
          hide: false
```

> [!NOTE]
>
> `cap_drop: ALL` prevents destructive ZFS operations (`zfs destroy`, `zpool export`, etc.) from being executed inside the container even though `/dev/zfs` is accessible. `no-new-privileges` prevents privilege escalation. Both are strongly recommended when passing `/dev/zfs`.

> [!TIP]
>
> If you do not add `/dev/zfs` to your compose file, Dynacat silently falls back to `statfs()` values. ZFS pool roots will show `0 MB used` in that case, but no error is raised.
