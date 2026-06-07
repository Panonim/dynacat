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
