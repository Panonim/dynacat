# Shared Widget Options

All widgets share a common set of configuration options that control their appearance and behavior. These options are available across every widget type and can be combined with widget-specific options.

## Overview

| Name | Type | Required | Default |
| ---- | ---- | -------- | ------- |
| title | string | no | |
| hide-header | boolean | no | false |
| title-icon | icon | no | |
| title-url | string | no | |
| css-class | string | no | |
| cache | string | no | widget-specific |
| update-interval | string | no | widget-specific |
| frameless | boolean | no | false |

## Properties

### `title`

The display name of the widget, shown in the widget header. If not specified, some widgets will use a default title.

Example:

```yaml
- type: weather
  title: Weather in London
```

### `hide-header`

When set to `true`, completely hides the widget header including the title and title icon. Useful for widgets that don't require a header or when you want a more compact layout.

Example:

```yaml
- type: clock
  hide-header: true
```

### `title-icon`

Displays an icon next to the widget title in the header. Supports the same icon syntax as the `icon` property used in other widgets, including Simple icons, Material Design icons, and custom URLs.

For icon syntax details, refer to the [Icons](configuration.md#icons) section of the configuration guide.

Example:

```yaml
- type: custom-api
  title: Todoist
  title-icon: di:todoist
```

```yaml
- type: hacker-news
  title: Top Stories
  title-icon: mdi:fire
```

Self-hosted icon example:

```yaml
- type: bookmarks
  title: Links
  title-icon: /assets/icons/links.png
```

### `title-url`

Makes the widget title clickable by converting it to a link. When clicked, it navigates to the specified URL. The link will open in a new tab by default.

Example:

```yaml
- type: rss
  title: GitHub Releases
  title-url: https://github.com/releases
```

### `css-class`

Allows you to specify custom CSS class names for individual widgets. This is useful when combined with a custom CSS file to apply specific styling to particular widgets.

Because Dynacat uses utility classes, it can be difficult to target specific elements. Each widget already has a `widget-type-{name}` class automatically applied. With `css-class`, you can add additional classes for more targeted styling.

Example:

```yaml
- type: rss
  title: Important Feed
  css-class: custom-feed
```

Then in your custom CSS file:

```css
.widget-type-rss.custom-feed {
    border-color: var(--color-highlight);
    background-color: rgba(255, 255, 0, 0.1);
}
```

See the [Theme](configuration.md#theme) section for more information on custom CSS files.

### `cache`

Overrides the widget's default cache duration. Cached content is stored and reused for the specified duration to reduce API requests and improve performance.

**Format:** A number followed by a unit. Supported units are:
- `s` for seconds (e.g., `30s`)
- `m` for minutes (e.g., `5m`)
- `h` for hours (e.g., `2h`)
- `d` for days (e.g., `1d`)

Each widget has its own default cache duration. Using this property allows you to customize how long content is cached for a specific widget instance.

Example:

```yaml
- type: hacker-news
  cache: 1h
```

```yaml
- type: rss
  title: Fast-updating RSS
  cache: 15m
```

```yaml
- type: custom-api
  title: Random Fact
  cache: 1d
```

### `update-interval`

Controls how frequently the widget content is refreshed. This is independent of caching and controls the polling interval for dynamic updates.

**Format:** A number followed by a unit. Supported units are:
- `s` for seconds (e.g., `15s`)
- `m` for minutes (e.g., `30m`)
- `h` for hours (e.g., `1h`)

The page-level `dynamic-updates` setting must be enabled (the default) for widget updates to occur. When set to `false` at the page level, no widget polling will happen regardless of individual widget `update-interval` settings.

Example:

```yaml
- type: server-stats
  update-interval: 10s
```

```yaml
- type: playing
  title: Now Playing
  update-interval: 30s
```

```yaml
- type: monitor
  update-interval: 5m
```

> [!NOTE]
>
> The actual update frequency may vary based on the page configuration and dynamic updates settings.

### `lazy-load`

When set to `true`, removes the border and padding (frame) around the widget, making it blend more seamlessly with the background or other content.

This is particularly useful for widgets that are designed to be minimalist or when creating custom layouts.

Example:

```yaml
- type: clock
  frameless: true
```

```yaml
- type: calendar
  hide-header: true
  frameless: true
```

```yaml
- type: custom-api
  title: Minimal Widget
  frameless: true
```

## Combining Options

Most of these options can be combined to achieve the desired appearance and behavior:

```yaml
- type: rss
  title: Important News
  title-icon: mdi:newspaper
  title-url: https://news.example.com
  cache: 30m
  update-interval: 1h
  css-class: news-feed
  lazy-load: false
```

```yaml
- type: clock
  hide-header: true
  frameless: true
```

```yaml
- type: custom-api
  title: System Status
  title-icon: di:server
  update-interval: 15s
  css-class: status-widget
```
