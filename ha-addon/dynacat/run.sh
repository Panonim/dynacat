#!/usr/bin/with-contenv bashio
# shellcheck shell=bash
set -e

CONFIG_DIR=/config
CONFIG_FILE="${CONFIG_DIR}/dynacat.yml"
DATA_DIR=/data

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

if [ ! -f "${CONFIG_FILE}" ]; then
    bashio::log.info "No dynacat.yml found in /config, creating a starter configuration..."
    cat > "${CONFIG_FILE}" <<EOF
server:
  host: 0.0.0.0
  port: 8080
  cache-dir: ${DATA_DIR}/cache
  db-path: ${DATA_DIR}/dynacat.db

# See https://dynacat.artur.zone/configuration for everything that can go here
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

bashio::log.info "Starting Dynacat..."
exec /usr/bin/dynacat --config "${CONFIG_FILE}"
