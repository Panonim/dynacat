# Backlog

TODO/FIXME comments pulled out of the source during the 2026-09-03
comment-to-docs pass and filed here so they stay visible without cluttering
the code. Each entry is dated to when it was filed here, not necessarily
when the comment was first written. This is a maintainer reference, not part
of the published docs site under `docs/docs/`.

## 2026-09-03: config parsing

- `internal/dynglance/config-fields.go:384`, `UnmarshalJSON` for
  `queryParametersField`: the per-type switch cases duplicate logic; refactor
  the duplication if more value types need to be supported.
- `internal/dynglance/config.go:408` and `internal/dynglance/config.go:444`,
  the `$include` file-watch tracking (`lastIncludes` / `currentIncludes`
  bookkeeping around `debouncedParseAndCompareBeforeCallback`): marked flaky,
  refactor. No further detail was recorded about which scenario reproduces
  the flakiness.
- `internal/dynglance/config.go`, `parseConfigVariables` (variable
  substitution syntax documented in `docs/design.md`): the substitution regex
  matches inside commented-out YAML lines, since it operates on raw file
  bytes rather than parsed YAML. Not clear how to fix this cleanly, since
  variables can appear anywhere and can modify the YAML structure itself.
- `internal/dynglance/config.go:535-538`, `isConfigStateValid`: config
  validation currently happens in two separate places, this function (checks
  logical errors, does not modify data) and again during application
  construction (which does modify data and does further validation). Would
  be better consolidated into a single validation pass.

## 2026-09-03: dynglance.go

- `internal/dynglance/dynglance.go:781`, `handleNotFound`: returns a plain
  text "Page not found" body; add a proper not-found page template instead.
- `internal/dynglance/main.go:154-156`, `serveApp`: the startup/reload
  orchestration is difficult to reason about because of the number of
  callbacks and simultaneous operations involved. Refactor to a single
  goroutine driven by a channel for synchronous state changes if it grows
  any more complex.

## 2026-09-03: widgets

- `internal/dynglance/widget.go:340-341`, the widget error-render fallback
  path: when re-rendering a widget's own error template also fails, there is
  no generic fallback error template to show; add one.
- `internal/dynglance/widget.go:399-410`,
  `canContinueUpdateAfterHandlingErr`: needs to cover more edge cases. If a
  widget has partial content and is updated early, the early update can
  return even less content than the initial update. Would need a mechanism
  that decides whether to update early based on how many things failed
  during the initial and subsequent update and how they failed (server error
  such as a gateway timeout, worth retrying early, versus a client error
  such as a rate limit, not worth retrying early). Would require reworking a
  fair amount of the `feed` package, likely with a custom error type that
  carries more information than a wrapped error does. An alternative
  approach floated: a resource cache that only refetches the specific
  resources that failed, then rebuilds the widget from that.
- `internal/dynglance/widget-markets.go:151`, `marketChartDays`: the chart
  time frame is a fixed constant; allow it to be configured per widget
  instead.
- `internal/dynglance/widget-repository.go:214`, the issues-fetch error
  path: when `issuesErr` is set, assigning to `err` overwrites whatever
  error was already set from a prior partial failure in the same function,
  losing that earlier error. Fix should combine both errors (for example
  with `errors.Join`) instead of overwriting.

## 2026-09-03: static assets

- `internal/dynglance/utils.go:156`, the static file server wrapper: sets
  the `Cache-Control` header unconditionally, even when the requested file
  does not exist (and the underlying `http.FileServer` will 404). Should
  only set the header for files that actually exist.

## 2026-09-03: frontend (static/js)

- `internal/dynglance/static/js/page.js:7-8`, `fetchPageContent`: does not
  handle non-200 status codes or timeouts from the content fetch, and has no
  retry behavior.
- `internal/dynglance/static/js/page.js:1034`, timezone conversion in the
  clock/time widget: when a configured timezone string is invalid, the error
  is only logged to the console; the UI should indicate to the user that the
  configured timezone is invalid.
- `internal/dynglance/static/js/page.js:1136`, `setupCalendars`: the
  calendar module is dynamically imported and then release data is fetched
  afterward, producing a waterfall of requests. Implement prefetching so
  these can overlap instead.
- `internal/dynglance/static/js/calendar.js:86`, `Calendar`: when navigating
  to the previous or next month, spill-over days from the current month are
  not highlighted as "today" even when they fall within the visible range.
  Should display the current date if it is within the spill-over days shown.
- `internal/dynglance/static/js/calendar.js:112`, `autoAdvanceNow`: the
  calendar auto-advances to today at midnight even if the user is currently
  viewing a different month; it should not auto-advance the visible month in
  that case.
</content>
