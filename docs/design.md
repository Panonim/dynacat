# Design notes

This file documents architecture and implementation rationale for the fork's
Go and JavaScript source, the "why" behind non-obvious code shapes, that
doesn't belong in the user-facing DynGlance documentation under `docs/docs/`
or in `docs/operations.md` (release and CI mechanics). It is a maintainer
reference, not part of the published docs site.

## Shared HTTP request layer

`internal/dynglance/request-share.go` collapses concurrent, identical GET
requests from different widgets into a single fetch. Multiple widgets often
request the same external endpoint, for example two repository widgets
watching the same repo. Routing their GET requests through
`globalSharedFetcher` collapses identical concurrent requests into a single
fetch (singleflight) and keeps a short response cache so near-sequential
requests reuse the result instead of hitting the endpoint again.

The cache is requester-tolerant: each caller passes its own `maxAge` (its
widget's configured cache duration) and only reuses an entry younger than
that. A short-cache widget refetches on its own schedule while a long-cache
widget reuses freely, so every widget's configured cache setting is honored
even though the underlying fetch is shared.

Only GET requests are shared. POST and other methods (auth, GraphQL) always
go out directly, since those are not safe to deduplicate or cache.

`sharedFetcher.do` returns a shared response for a GET request. The returned
body is shared read-only across callers and must not be mutated; callers
should only unmarshal it.

## Widget update batching

`internal/dynglance/dynglance.go`, `page.updateOutdatedWidgets`: a single
batch of widget updates is capped by `widgetUpdateBatchTimeout` (25 seconds).
`page.mu` is held for the duration of the batch (see `updateOutdatedWidgets`
and `sseCheckAndPushUpdates`), so without this ceiling a single widget whose
`update()` call hangs (a stalled outbound connection that never trips its own
timeout) would block that mutex forever, wedging every subsequent request and
SSE tick for the page indefinitely.

## Ingress base URL resolution

`internal/dynglance/dynglance.go`, `effectiveBaseURL`: the effective base
path prefixing every generated link, redirect, and cookie is the Home
Assistant Ingress path when present, otherwise the statically configured
`server.base-url` (for manually reverse-proxied setups). The mechanism and
the bug this fixes are documented in full in `docs/docs/home-assistant.md`
("Viewing the dashboard via the sidebar (Ingress)"); this note exists only so
a reader of `dynglance.go` knows to look there.

`StaticAssetPath` (also in `dynglance.go`) must be called with the
requesting page's effective base URL (`.Request.BaseURL` in templates), not
the static `server.base-url` config value, so embedded asset links still
resolve correctly when accessed through Ingress.

The `assetResolver` passed into `widgetProviders` is resolved once per widget
refresh, not per request (there is no HTTP request in scope at that point),
so unlike page and asset links it cannot be made Ingress-path aware; it falls
back to the static `server.base-url` config value.

## Config file watching

`internal/dynglance/config.go`, the fsnotify watch loop: on a `Rename` event,
Linux stops watching the renamed file, but Windows continues watching it
under the new name with no way to read that new name from the event. To keep
behavior consistent with Linux, the old path is removed from the manually
tracked includes on every rename; `debouncedParseAndCompareBeforeCallback`
re-adds it if it is still required after the rename triggers. This may
produce different edge-case behavior on Windows than on Linux for renames
that keep the file within a watched include tree. See
[fsnotify/fsnotify#255](https://github.com/fsnotify/fsnotify/issues/255).

### Config variable substitution

`internal/dynglance/config.go`, `parseConfigVariables` supports four forms
inside `dynglance.yml` values:

| Syntax | Behavior |
| --- | --- |
| `${API_KEY}` | Replaced with the value of the `API_KEY` environment variable. |
| `\${API_KEY}` | Escaped; used as-is in the config, with the backslash stripped. |
| `${secret:api_key}` | Value loaded from `/run/secrets/api_key`. |
| `${readFileFromEnv:PATH_TO_SECRET}` | Value loaded from the file path named by the `PATH_TO_SECRET` environment variable. |

## Custom API template functions

`internal/dynglance/widget-custom-api.go`:

- `customAPITemplateData.Subrequest` panics when the requested key was not
  defined, rather than returning a zero value. There is nothing sensible to
  return for an undefined subrequest, and returning zero values silently
  would be confusing from the user's perspective. Go's `text/template`
  package recovers from panics during execution and surfaces the panic
  message as an error, so this still fails gracefully for the person editing
  the config.
- The `trimPrefix` template function takes its arguments in `(prefix, s)`
  order, the reverse of the more natural `(s, prefix)`, specifically so
  calls can be piped: `{{ .JSON.String "foo" | trimPrefix "bar" | doSomethingElse }}`
  works because the piped value becomes the last argument to the function;
  `{{ trimPrefix (.JSON.String "foo") "bar" | doSomethingElse }}` would not
  chain as cleanly. `trimSuffix` follows the same convention.

## DNS stats widget (Pi-hole)

`internal/dynglance/widget-dns-stats.go`:

- `pihole5QueriesSeries.UnmarshalJSON` falls back to an empty map because
  when the user has query logging disabled, Pi-hole can return
  `domains_over_time` as an empty array instead of a map, which would
  otherwise break unmarshalling the rest of the response. See
  [Panonim/dynacat#289](https://github.com/Panonim/dynacat/issues/289).
- The history endpoint drops the first data point returned
  (`seriesResponse.History[1:]`). The dashboard shows the last 24 hours; at a
  10-minute interval that is `24 * (60/10) = 144` points, but Pi-hole v6
  returns 145. **Unverified**: the reason for the extra leading point is not
  known; the code assumes it is the oldest point and discards it. If Pi-hole
  changes its response shape again, re-check this assumption before trusting
  the discard.

## sysinfo widget (`pkg/sysinfo`)

- **Unverified**, Windows CPU load: `load.Avg()` values on Windows have been
  observed to be unreliable. Even with the CPU pegged near 50% for several
  minutes, `load1` was sometimes far under or far over with no clear
  pattern. Dividing by core count (as done on Unix) produced numbers that
  looked too low, so that adjustment is skipped on Windows. This has not
  been root-caused; treat Windows load percentages as approximate.
- **Unverified**, Windows CPU temperature: sensor reading is disabled on
  Windows because it requires elevated privileges, and when it is
  available anyway it has been observed to return a single sensor keyed
  `ACPI\ThermalZone\TZ00_0` that does not appear to be the CPU sensor and
  does not match the temperatures Libre Hardware Monitor reports for the
  same machine. Also disabled on OpenBSD, NetBSD, and FreeBSD because
  `github.com/shirou/gopsutil` does not implement sensor reading for those.
- Verified, container disk reporting: inside containers (Docker, for
  example), `disk.Partitions(false)` only returns devices whose source
  starts with `/dev/`, which skips the overlay root filesystem. When that
  leaves no mountpoints, `/` is added as a fallback so disk usage is still
  reported without requiring a host bind-mount.
- Verified, ZFS usage: `getZFSUsage` shells out to `zfs list` because
  `statfs(2)` on a ZFS pool root returns `Used=0` when all data lives in
  child datasets (this is the case on TrueNAS SCALE, for example). It
  returns an error if the `zfs` binary is unavailable or the mountpoint is
  not found, so callers can fall back to the `statfs` values in that case.
</content>
