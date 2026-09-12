# API

Dynacat can expose the data its widgets have already fetched as read-only JSON, so you can reuse it in scripts, home automation or another dashboard. The API is disabled by default and never modifies anything.

## Enabling

Configure via the top-level `api` property:

```yaml
api:
  enabled: true
  token: ${env:DYNACAT_API_TOKEN}
  allowed-origins:
    - https://example.com
```

| Name | Type | Required | Default |
| ---- | ---- | -------- | ------- |
| enabled | boolean | no | false |
| token | string | no |  |
| enforce-page-permissions | boolean | no | true |
| rate-limit | number | no | 5 |
| allowed-pages | array of strings | no |  |
| allowed-origins | array of strings | no |  |

#### `enabled`

Turns the API on. While this is `false` the endpoints are not registered at all and requests to them return `404`.

#### `token`

A shared secret that callers send in the `X-API-Token` header. When it is empty no token is required and anyone who can reach the server can read your widget data, so Dynacat prints a warning on startup that cannot be silenced with `LOG_LEVEL`.

#### `enforce-page-permissions`

Keeps pages that use `allowed-users` or `allowed-groups` behind a real user login. See [Page permissions](#page-permissions).

#### `rate-limit`

How many API requests a single IP may make per minute. Requests beyond it get `429` with a `Retry-After` header. Set it to `0` to remove the limit.

Example:

```yaml
api:
  enabled: true
  rate-limit: 60
```

The limit is applied before credentials are checked, so it also caps how fast someone can guess a token or password.

> [!NOTE]
>
> Behind a reverse proxy, set `server.proxied: true` so the limit counts real client IPs instead of the proxy.

#### `allowed-pages`

A list of page slugs the API is allowed to serve. When empty every page is available. Pages outside the list behave as if they do not exist, including their widgets.

#### `allowed-origins`

Origins allowed to call the API from a browser. See [CORS](#cors).

> [!NOTE]
>
> The API is read-only. There is no endpoint that changes configuration, triggers actions or writes anything.

## Authentication

Dynacat resolves the caller in this order:

| Method | How | Identity |
| ------ | --- | -------- |
| Session cookie | an existing dashboard login | the logged-in user |
| Basic auth | `Authorization: Basic <username:password>` against your configured `auth.users` | that user |
| Token | `X-API-Token: <token>` matching `api.token` | none |

With a token:

```sh
curl -H "X-API-Token: mytoken" http://localhost:8080/api/v1/pages
```

With a username and password:

```sh
curl -u admin:mysecretpassword http://localhost:8080/api/v1/pages
```

Failed username and password attempts count towards the same brute-force protection as the login page, which blocks an IP after 5 failures within 5 minutes. All requests are additionally capped by [`rate-limit`](#rate-limit).

## Page permissions

By default a page that restricts access with `allowed-users` or `allowed-groups` also requires a real user over the API. A valid `api.token` on its own is not enough, because a token identifies nobody and cannot be checked against a user list.

That means for a restricted page you must call the API with either a session cookie or basic auth, and the resulting user must be allowed on that page. Requests without any identity get `401`, and users who are not on the list get `403`. Restricted pages are also left out of `/api/v1/pages` so their existence is not revealed.

> [!NOTE]
>
> Groups only come from OIDC claims, so a user authenticating with a username and password has no groups. Add such users to `allowed-users` rather than relying on `allowed-groups`.

You can turn this off:

```yaml
api:
  enabled: true
  token: mytoken
  enforce-page-permissions: false
```

> [!CAUTION]
>
> With `enforce-page-permissions: false` your restricted pages are served to anyone holding the API token, and to everyone at all if no token is set. Dynacat prints an unsuppressable warning while this is active.

## Restricting what is exposed

Two things narrow the surface:

- `allowed-pages` limits the API to the page slugs you list.
- Individual widgets are only addressable when you give them an `api-id`, which is opt-in.

Example:

```yaml
api:
  enabled: true
  token: mytoken
  allowed-pages:
    - home
```

## Widget IDs

Add `api-id` to any widget to give it a stable address. It must be unique across your whole configuration, and Dynacat refuses to start if two widgets share one.

Example:

```yaml
- type: weather
  api-id: home-weather
  location: London, United Kingdom
```

The widget is then available at `/api/v1/widgets/home-weather`. Widgets nested inside `group` and `split-column` work the same way.

## Endpoints

Every endpoint responds to `GET` and lives under `/api/v1`.

### `/api/v1/pages`

Lists the pages the caller is allowed to read and the widgets on them, without any data. Useful for discovering what `api-id` values exist.

```sh
curl -H "X-API-Token: mytoken" http://localhost:8080/api/v1/pages
```

```json
[
  {
    "slug": "home",
    "name": "Home",
    "widgets": [
      { "api-id": "home-weather", "type": "weather", "title": "Weather" },
      { "type": "group", "widgets": [{ "type": "clock", "title": "Clock" }] }
    ]
  }
]
```

### `/api/v1/pages/{slug}`

Returns every widget on the page together with its data.

```sh
curl -H "X-API-Token: mytoken" http://localhost:8080/api/v1/pages/home
```

### `/api/v1/widgets/{api-id}`

Returns a single widget.

```sh
curl -H "X-API-Token: mytoken" http://localhost:8080/api/v1/widgets/home-weather
```

```json
{
  "api-id": "home-weather",
  "type": "weather",
  "title": "Weather",
  "data": {
    "Weather": {
      "Temperature": 14,
      "ApparentTemperature": 12,
      "WeatherCode": 3
    }
  }
}
```

### Status codes

| Code | Meaning |
| ---- | ------- |
| 200 | success |
| 401 | missing or invalid credentials, or a restricted page was requested without a user |
| 403 | the authenticated user is not allowed on that page |
| 404 | unknown page slug or `api-id`, or the API is disabled |
| 429 | over the rate limit, or too many failed username and password attempts |

## Data shape

Each widget is returned as an envelope. `api-id` and `error` are omitted when they are empty, and `widgets` only appears on `group` and `split-column` widgets.

```json
{
  "api-id": "home-weather",
  "type": "weather",
  "title": "Weather",
  "error": "failed to fetch",
  "data": {}
}
```

Everything inside `data` comes from the widget itself, so the keys are the internal field names and use `PascalCase`. They can change between Dynacat versions when a widget is reworked.

> [!IMPORTANT]
>
> Anything you wrote in your configuration is never returned. Only values a widget produced at runtime are serialized, so tokens, passwords and other credentials cannot appear in a response. Field names that look like a credential are dropped as well, and secrets embedded in query strings are replaced with `redacted`.

A side effect of that rule is that configured values you might expect to see, such as a bookmark title or a server name, are not part of the output.

Two widgets are limited:

- `custom-api` and `dynawidgets` cannot expose their rendered result as structured data, so they return the raw upstream JSON response under `APIResponse` instead.
- `calendar` only keeps rendered markup in memory and returns almost nothing.

### Updating and shared requests

Requesting a widget refreshes it only when its cache has expired, exactly like loading the page would. Calling the API repeatedly never causes extra requests to the services behind your widgets.

Widgets that point at the same source also continue to share a single request, so reading two `latest-media` widgets configured against one server hits that server once.

## CORS

Browser scripts on another origin can only call the API if you list their origin:

```yaml
api:
  enabled: true
  allowed-origins:
    - https://example.com
    - https://dash.example.com
```

Use a single `*` to allow any origin:

```yaml
api:
  enabled: true
  allowed-origins:
    - "*"
```

> [!NOTE]
>
> Browsers refuse to send credentials to an endpoint that answers with `*`, so a page using `X-API-Token` or basic auth needs its exact origin listed.

## Security

> [!CAUTION]
>
> Enabling the API without a `token` makes your widget data readable by anyone who can reach the server, even when the dashboard itself requires a login. Set a token, keep `enforce-page-permissions` on, and use `allowed-pages` to expose only what you need.

Behind a reverse proxy, set `server.proxied: true` so brute-force protection sees the real client IP:

```yaml
server:
  proxied: true
```
