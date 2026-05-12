# Changes for 2.X.X
- Removed photos from latest-media widget
- Fixed issue where latest-media widget wasn't receiving the correct thumbnail type from Plex
- Every widget now supports `frameless: true`
- Fixed issue with icons fallback when no svg is found 
- Added diffrent header support for monitor widget

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