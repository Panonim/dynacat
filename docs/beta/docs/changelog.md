# Changes for 3.0.0
- Moved the compose template to [Panonim/dynacat-compose-template](https://github.com/Panonim/dynacat-compose-template), the install command is now a single `curl | tar` with no `sed` renaming
- Added support for `tcp://`/`http://` remote Docker hosts to `sock-path` in the `docker-controller` widget, matching `docker-containers` -> https://github.com/Panonim/dynacat/issues/137
- Documented `id`/`parent` grouping for the `containers` property in `docker-containers` widget -> https://github.com/Panonim/dynacat/issues/134
- Fixed an issue where subrequests were not detected properly in `dynawidgets`
- Added automatic update checks for cached `dynawidgets` templates, with a notice on the widget when one changed
- Fixed `dynawidgets` protocol handling 
- Added an option to visually edit your configuration
- API, so you can now easily get all your data in external apps
- Fixed Navidrome in currently playing
- Added an option to see bookmarks, docker containers and monitors in search widget -> https://github.com/Panonim/dynacat/issues/126
- Fixed issues with releases performence in `calendar` widget 
- Made image fetching in `currently playing` more reliable
- Added a function in `calendar` widget when showing releases to also show release type
- Added a function in `calendar` widget to also show current state of the media e.g. grabbed
- Fixed issue with `search` widget highlighting
- Fixed Jellyfin issue where `playing` widget couldn't be resolved because of the api changes.
- Added a read-only JSON API for accessing widget data
- Added option to mark `monitor` as disabled
- Added `show-history` option to `monitor` widget showing a bar of the last hour of status checks
- Added a comparison to the previous run in `speedtest` widget
- Fixed a few security issues
- Fixed trash icon disappearing while dragging a `todo` item on mobile
- Fixed `todo` checkbox sitting too close to widget edge on mobile
- Fixed a lot of smaller ui issues (I lost count at some point tbh)
- Added [degoog](https://github.com/degoog-org/degoog) theme and engine 
- Fixed Reddit widget and Reddit RSS feeds returning `403` again after Reddit renamed the JS challenge token field -> https://github.com/Panonim/dynacat/issues/141
- Updated Go packages
- Updated docs to include missing blocky information
- Server stats not showing CPU temp and wrong platform name -> https://github.com/Panonim/dynacat/issues/135

# Changes for 2.4.0
- Added Brave Search as an autocompletion engine and normal one
- Added support for icons in the page title 
- Fixed issue where todo widget highlight was too short
- Fixed issue where in todo widget trash animation icon was slower than highlight
- Added speedtest widget
- Fixed issue where incorrect thumbnails were pulled for series
- Improved endpoint fetching by adding shared cache
- Fixed issue where testing repo wasnt properly detected in dynawidgets
- Fixed log level functionality -> https://github.com/Panonim/dynacat/pull/96
- Fixed incorrect daily percentage in market widget -> https://github.com/Panonim/dynacat/pull/117
- Added server-stats `compact: true` option -> https://github.com/Panonim/dynacat/pull/104
- Added support for Sonarr/Radarr releases in the `calendar` widget 
- Preserve auth redirect -> https://github.com/Panonim/dynacat/pull/120
- Fixed an issue where combining subreddits would show incorrect title
- Fixed an issue with theme syncing
- Fixed an issue with `latest-media` widgets interfering with each other -> https://github.com/Panonim/dynacat/issues/124
- Added setting to open stream instead of category if click on category -> https://github.com/Panonim/dynacat/pull/123

# Changes for 2.3.1
- Added support for loading environment variables from a file via `--env-file`
- Made initial loading faster by fetching data on service start
- Fixed an issue where `glance.yml` was not detected correctly which would cause issues when transitioning
- Fixed Reddit widget and Reddit RSS feeds returning `403` by mimicking a browser TLS handshake and solving the JS challenge for the `loid` cookie
- Bumped up Go packages to latest

# Changes for 2.3.0
- Removed photos from latest-media widget
- Fixed issue where latest-media widget wasn't receiving the correct thumbnail type from Plex
- Every widget now supports `frameless: true`
- Fixed issue with icons fallback when no svg is found 
- Added diffrent header support for monitor widget
- Fixed issue where `Currently Playing` widget grabbed incorrect cover for shows
- Fixed issue where qBittorrent would incorrectly detect current state when seeding 
- Fixed issue where page doesnt load correctly on browser reload
- Fixed issue where key-binding only works when there are search widgets
- Fixed issue where server-stats disk usage were shown incorrectly -> https://github.com/Panonim/dynacat/issues/89
- Fixed issue where grouped tabs would reset after refresh -> https://github.com/Panonim/dynacat/issues/93
- Added ability to have navbar hidden on desktop, show it on hover (hover height area 22px) -> https://github.com/Panonim/dynacat/pull/91
- Added ability to center nav-item elements on navbar on desktop -> https://github.com/Panonim/dynacat/pull/91
- Added ability to hide logo from navbar -> https://github.com/Panonim/dynacat/pull/91
- Fixed rendering user svg from branding correctly -> https://github.com/Panonim/dynacat/pull/91
- Fixed issue where failed pulls from Youtube would block other fetches -> https://github.com/Panonim/dynacat/issues/94
- Made Github fetches faster -> https://github.com/Panonim/dynacat/pull/97
- Fixed issue with incorrect PKCE handling in OIDC
- Made RSS feed render faster -> https://github.com/Panonim/dynacat/pull/99
- Made key-bindings work with other keyboard layouts -> https://github.com/Panonim/dynacat/pull/99

# Changes for 2.2.3
- Add utility functions for array manipulation -> https://github.com/Panonim/dynacat/pull/60
- Key Binding for easier navigation between pages
- Fixed search widget query for bangs
- Added start on page open for stopwatch widget
- Fixed issue where groups would open multiple of the same links
- Added caching for every widget 
- Fixed issues with `markets` pulling
- Allowed to invert colors in `markets` widget

# Changes for 2.2.2
- Resolved an issue where Reddit denied requests

# Changes for 2.2.1
- Fixed `videos` widget collapsing state
- Updated OIDC documentation
- Cross iFrame embeding fix

# Changes for 2.2.0
- OICD Support
- Dynamic Updates Documentation
- Add Navidrome to the "playing" widget
- Added `dynawidgets` 
- Stopwatch widget
- Security Updates
- Allow insecure for `changedetection` widget 
- Added icon support for titles
- Added icon support for bangs in search widget
- Added search completion using ddg api
- Allowed to press `enter` to login 
- Add cursor pointer to youtube thumbnails
- Added `{{hide}}` function to cutom-api widget
