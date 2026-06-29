# Dynamic Updates

Dynacat uses dynamic updates to keep your dashboard relevant without requiring manual page refreshes. This is powered by a combination of Server-Sent Events (SSE) and client-side polling.

Updates occur at three levels: **Global**, **Page**, and **Widget**.

### Global Control
You can disable all dynamic updates across the entire application using the `ENABLE_DYNAMIC_UPDATE` environment variable.

- **`ENABLE_DYNAMIC_UPDATE`**: Set to `false`, `0`, or `f` in your `docker-compose.yml` to stop all automatic refreshes.

### Page Control
Dynamic updates can be toggled for specific pages in your `dynacat.yml` using the `dynamic-updates` property.

- **`dynamic-updates`**: (boolean, default: `true`) When set to `false`, the page will not perform SSE updates or poll for widget changes.

```yaml
pages:
  - name: "Static Stats"
    dynamic-updates: false
    columns:
      - ...
```

### Widget Control
All widgets support an `update-interval` property to control how often they refresh.

- **`update-interval`**: Accepts a duration (e.g., `30s`, `5m`, `1h`). Setting this to `0s` typically disables updates for that specific widget.

> [!NOTE]
> Relative timestamps (e.g., "5 minutes ago") are updated every 60 seconds on the client side even if the widget data itself hasn't been refreshed.

---

## Default Widget Update Intervals

The following table lists the default update intervals for widgets when `update-interval` is not explicitly configured:

| Widget | Default Update Interval |
|---|---:|
| Calendar (With releases configured) | 15m |
| ChangeDetection.io | 30m |
| Custom API | 1m |
| Docker Containers | 2m |
| Docker Controller | 30s |
| DNS Stats | 10m |
| Extension | 5m |
| Hacker News | 5m |
| Lobsters | 30m |
| Markets | 10m |
| Monitor | 2m |
| Reddit | 25m |
| Releases | 3h |
| Repository | 3h |
| RSS | 15m |
| Server Stats | 15s |
| Twitch Channels | 15m |
| Twitch Top Games | 35m |
| Videos | 1h |
| Weather | 1h |

| External Integration | Default Update Interval |
| --- | ---:|
| Currently Playing | 30s |
| Latest Media | 30m |
| Torrenting | 30s |

## Efficiency and Optimization

To save resources, Dynacat intelligently manages updates using two primary mechanisms:

### Server-Side Updates (SSE)
Most widgets use **Server-Sent Events (SSE)**. The server keeps a persistent connection with your browser and "pushes" updates only when the server-side update loop detects a change. This is highly efficient as it avoids constant HTTP requests from your browser.

### Client-Side Polling
Certain widgets, specifically the **Custom API** widget (when an `update-interval` is set), use **Client-Side Polling**.

Instead of waiting for the server to push data, your browser independently sends a request to your url at the specified interval. This is used for:
- **High-frequency updates**: Ensuring that very short intervals (e.g., `5s`) don't overhead the global SSE event loop.
- **Direct feedback**: Ensuring that specific API-driven widgets can refresh their state even if the global page updates are throttled.

### Visibility Tracking
If you switch to another tab or minimize your browser, Dynacat detects the `visibilitychange`. To save your device's battery and reduce server load:
- Independent polling is **paused**.
- SSE processing is throttled.
- Relative time updates (e.g., "just now") are paused.

When you return to the tab, Dynacat immediately triggers a refresh for any widgets that have expired during the background period.