#!/usr/bin/with-contenv bashio
# shellcheck shell=bash
set -e

CONFIG_DIR=/config
CONFIG_FILE="${CONFIG_DIR}/dynglance.yml"
DATA_DIR=/data

CONFIG_UPLOAD_BEGIN_MARKER="# BEGIN-DYNGLANCE-CONFIG-UPLOAD"
CONFIG_UPLOAD_END_MARKER="# END-DYNGLANCE-CONFIG-UPLOAD"

mkdir -p "${CONFIG_DIR}" "${DATA_DIR}/cache"

if bashio::config.has_value 'timezone'; then
    export TZ
    TZ="$(bashio::config 'timezone')"
fi

case "$(bashio::config 'log_level')" in
    debug)   export LOG_LEVEL=DEBUG ;;
    warning) export LOG_LEVEL=WARN ;;
    error)   export LOG_LEVEL=ERROR ;;
    *)       export LOG_LEVEL=INFO ;;
esac

# See docs/docs/home-assistant.md.
HA_TOKEN=""
if bashio::config.has_value 'home_assistant_token'; then
    HA_TOKEN="$(bashio::config 'home_assistant_token')"
fi
export HA_TOKEN

# See "Config Upload" in docs/docs/authentication.md.
CONFIG_UPLOAD_PASSWORD=""
if bashio::config.has_value 'config_upload_password'; then
    CONFIG_UPLOAD_PASSWORD="$(bashio::config 'config_upload_password')"
fi

CONFIG_UPLOAD_ENABLED="false"
if [ "$(bashio::config 'config_upload_enabled')" = "true" ]; then
    if [ "${#CONFIG_UPLOAD_PASSWORD}" -ge 12 ]; then
        CONFIG_UPLOAD_ENABLED="true"
    else
        bashio::log.warning "Config Upload is enabled but its passphrase is missing or shorter than 12 characters; leaving it disabled until a valid passphrase is set."
    fi
fi
export CONFIG_UPLOAD_ENABLED CONFIG_UPLOAD_PASSWORD

if [ ! -f "${CONFIG_FILE}" ]; then
    bashio::log.info "No dynglance.yml found in /config, creating a starter configuration..."
    cat > "${CONFIG_FILE}" <<EOF
server:
  host: 0.0.0.0
  port: 8080
  cache-dir: ${DATA_DIR}/cache
  db-path: ${DATA_DIR}/dynglance.db

# See https://github.com/trooperthorn/ha_app_dynglance/blob/main/docs/docs/configuration.md for everything that can go here
# (widgets, pages, auth, themes, custom CSS...). This file lives on the
# "addon_config" share, so it survives add-on updates and reinstalls, and can
# be edited from the Studio Code Server / File editor add-ons.
pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: clock
          - type: server-stats
EOF
fi

# See "Config Upload" in docs/docs/authentication.md for why a hand-written
# config-upload: section takes precedence over this managed block.
if grep -qF "${CONFIG_UPLOAD_BEGIN_MARKER}" "${CONFIG_FILE}"; then
    sed -i "/^${CONFIG_UPLOAD_BEGIN_MARKER}\$/,/^${CONFIG_UPLOAD_END_MARKER}\$/d" "${CONFIG_FILE}"
    # Trim the trailing blank line(s) left behind so repeated restarts don't
    # accumulate blank lines at the end of the file forever.
    printf '%s\n' "$(cat "${CONFIG_FILE}")" > "${CONFIG_FILE}"
fi

if grep -qE '^config-upload:' "${CONFIG_FILE}"; then
    bashio::log.warning "dynglance.yml already has its own 'config-upload:' section; the add-on's Config Upload option is ignored until you remove it."
else
    cat >> "${CONFIG_FILE}" <<EOF

${CONFIG_UPLOAD_BEGIN_MARKER}
# Managed by the add-on's "Config Upload" options - edits here are
# overwritten on every restart, use the Configuration page instead.
config-upload:
  enabled: \${env:CONFIG_UPLOAD_ENABLED}
  password: \${env:CONFIG_UPLOAD_PASSWORD}
${CONFIG_UPLOAD_END_MARKER}
EOF
fi

bashio::log.info "Starting DynGlance..."
exec /usr/bin/dynglance --config "${CONFIG_FILE}"
