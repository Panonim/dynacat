# URL Parameters

Dynacat can read query parameters from the page's URL and act on them, entirely in the browser. This lets a single dashboard behave differently depending on how it was opened. For example, `https://dash.example.com?code=XYZ789` can turn an otherwise-dead invite link into a working one, or reveal a widget that is hidden by default.

Two mechanisms are available:

- **[`[[...]]` substitution](#link-substitution)** fills query-parameter values into `href`/`src` URLs anywhere on the page.
- **[`show-if`](#widget-visibility)** shows or hides an entire widget based on a query parameter.

> [!IMPORTANT]
>
> All of this happens **client-side**. Query values are never sent to the server, so there is no risk of leaking them through request logs or caches. But it also means this is **not** a security boundary. A hidden widget or an unresolved link is still present in the page source; it is simply non-functional without the right parameter. That is fine when revealing it would only expose a dead link, but for genuinely secret content use [Authentication](authentication.md) instead. URL parameters are weak by nature: they show up in browser history, bookmarks, and `Referer` headers.

The feature is enabled by default. To turn it off globally:

```yaml
server:
  disable-url-parameters: true
```

## Link substitution

Any `href` or `src` rendered by any widget may contain `[[...]]` tokens. When the page loads, each token is replaced using the current URL's query parameters.

### Simple substitution

`[[name]]` is replaced with the value of the `name` query parameter:

```yaml
- type: bookmarks
  groups:
    - links:
        - title: Join
          url: https://invite.example.com/j/[[code]]
```

Opening the dashboard at `?code=XYZ789` makes the link point to `https://invite.example.com/j/XYZ789`.

If the parameter is missing or empty the token has nothing to resolve to, so **the link is hidden** rather than left present and broken. This is the "fail unless the invite code is supplied" behavior. The substituted value is URL-encoded, so it always stays a single, safe URL segment.

### Default values

Append `|default` to supply a fallback that is used when the parameter is absent or empty (so the link stays visible):

```yaml
url: https://invite.example.com/j/[[code|guest]]
```

### Conditional values

`[[name=match:ifTrue:ifFalse]]` inserts `ifTrue` when the parameter equals `match`, otherwise `ifFalse`. This is handy for pointing a link at one of a few known hosts, such as a production or a staging environment:

```yaml
url: https://[[env=prod:app:staging]].example.com
```

Add `|default` to choose a value for when the parameter is absent entirely. This gives three outcomes (matches, present-but-different, and not-provided):

```yaml
url: https://[[env=prod:app:staging|status]].example.com
```

| URL | Result |
| --- | --- |
| `?env=prod` | `https://app.example.com` |
| `?env=dev` | `https://staging.example.com` |
| *(no `env`)* | `https://status.example.com` |

The `match` value is part of the page sent to the browser, so do not use a conditional to check a value that must stay secret. See [What visitors can see](#what-visitors-can-see).

### Quoting

Wrap any literal (`match`, `ifTrue`, `ifFalse`, or `default`) in single quotes so it can contain `:` or `|`. Use `\'` for a literal quote:

```yaml
url: https://example.com/schedule?range=[[shift=day:'09:00-17:00':'17:00-09:00']]
```

### Notes

- Tokens work in the **path or the query** of a URL: both `…/j/[[code]]` and `…/redeem?ref=[[code]]` resolve correctly.
- `[[` is reserved inside URL fields that use this feature; a URL that needs a literal `[[` cannot also use substitution.
- The resulting URL must use the `http`, `https`, `mailto`, or `tel` scheme, otherwise the link is hidden.
- When a link is hidden, the container that would be left empty is collapsed too: a bookmark's list row disappears, and an `iframe` widget whose `source` is gated collapses entirely (the iframe is its whole content).
- To gate something that is a whole widget, including a widget's `title-url`, prefer [`show-if`](#widget-visibility) on the widget rather than putting a token in a single URL.

## Widget visibility

Use the [`show-if`](shared-widget-options.md#show-if) shared widget option to show or hide an entire widget based on a query parameter:

```yaml
- type: bookmarks
  title: Crew Only
  show-if: code=XYZ789
  groups:
    - links:
        - title: Join
          url: https://invite.example.com/j/[[code]]
```

| Parameter | Widget is shown when… |
| --------- | --------------------- |
| `code` | `?code` is present (even with no value, e.g. `?code`) |
| `code!` | `?code` is absent |
| `code=XYZ789` | `?code` equals `XYZ789` |
| `code!=XYZ789` | `?code` is anything other than `XYZ789` |

Presence alone satisfies the bare `code` form, so a valueless `?code` works as an on/off flag.

### Gating raw HTML

The `html` widget renders its `source` verbatim, without the shared widget wrapper, so the YAML `show-if` option (like `title` and `css-class`) does not apply to it. To gate raw HTML, put `data-show-if` directly on an element in your `source`. The same predicates apply, and because it works on any element you can also gate part of a larger block rather than the whole thing:

```yaml
- type: html
  source: |
    <div data-show-if="code">Visible only when ?code is present.</div>
```

## What visitors can see

Everything here is evaluated in the browser, so anything the page *compares against* is part of the content delivered to the browser. It can be read with the network inspector, or by requesting `/api/pages/<slug>/content/` directly. In particular, the match value in a conditional or a `show-if` equality check is visible in the page:

| Form | Is the checked value in the page? |
| --- | --- |
| `[[code]]` | No. The value comes from the visitor's URL; the page contains only the token. |
| `[[code\|default]]` | Only the non-secret default. |
| `[[code=SECRET:a:b]]` | Yes. `SECRET` appears in the page source. |
| `show-if: code=SECRET` | Yes. `SECRET` appears in the page source. |

A conditional or an equality check is fine for branching on non-secret context (environment, tier, network, locale), but it is not a way to hide a secret value. To gate on something that must stay secret, use plain substitution into a URL whose destination validates it:

```yaml
url: https://join.example.com/j/[[code]]
```

The page then contains only `[[code]]`. The real code lives in the visitor's URL and is checked by the destination, never appearing in the page. Anything compared in the browser is, by definition, readable in the browser.

## Examples

### Per-network service links

A self-hosted service often needs a different address depending on where you are: a LAN hostname at home, a VPN address when away. Rather than keeping separate dashboards or configuring split-horizon DNS, put the host in a parameter and use your most common case as the default:

```yaml
- type: bookmarks
  title: Services
  groups:
    - links:
        - title: Jellyfin
          url: http://[[host|nas.local]]:8096
        - title: qBittorrent
          url: http://[[host|nas.local]]:8080
```

Opened normally, the links use `nas.local`. Bookmark `?host=100.64.0.5` to reach the same services over a VPN, or `?host=nas.example.com` from outside your network. One configuration covers every route.

### Search across tools

A single parameter can prefill the same query into several destinations. Open the page with `?q=...` and every link runs that search:

```yaml
- type: bookmarks
  title: Search everywhere
  groups:
    - links:
        - title: Code
          url: https://github.com/search?q=[[q]]
        - title: Docs
          url: https://docs.example.com/search?q=[[q]]
        - title: Tickets
          url: https://tracker.example.com/search?q=[[q]]
```

Each link is hidden until `q` is supplied, so the widget stays empty until there is something to search for.

### Multi-step flows

`show-if` and parameter forwarding combine into a flow on a single page: widgets reveal themselves based on the current parameter, and links move the visitor to the next state. For example, a picker that becomes a detail view once a record is chosen:

```yaml
# Both widgets live on the same page, e.g. /records
- type: bookmarks
  title: Choose a record
  show-if: id!
  groups:
    - links:
        - title: Record A
          url: /records?id=A-100
        - title: Record B
          url: /records?id=B-200

- type: bookmarks
  title: Record actions
  show-if: id
  groups:
    - links:
        - title: Open in tracker
          url: https://tracker.example.com/[[id]]
```

With no `id`, only the picker is shown. Choosing a record reloads the page with `?id=...`, which hides the picker and reveals the actions, their links already pointing at the selection.
