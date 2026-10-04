#!/bin/sh
set -e

# Without PUID, or when already started as a non-root user, run as is.
if [ "$(id -u)" != "0" ] || [ -z "$PUID" ]; then
    exec /app/dynacat "$@"
fi

PGID="${PGID:-$PUID}"

case "$PUID$PGID" in
    *[!0-9]*)
        echo "PUID and PGID must be numeric" >&2
        exit 1
        ;;
esac

name_by_id() { awk -F: -v id="$2" '$3 == id { print $1; exit }' "$1"; }

group="$(name_by_id /etc/group "$PGID")"
if [ -z "$group" ]; then
    group=dynacat
    addgroup -g "$PGID" "$group"
fi

user="$(name_by_id /etc/passwd "$PUID")"
if [ -z "$user" ]; then
    user=dynacat
    adduser -D -H -h /app -s /sbin/nologin -u "$PUID" -G "$group" "$user"
fi

# A named user lets su-exec pick up supplementary groups, which the docker socket needs.
if [ -S /var/run/docker.sock ]; then
    sock_gid="$(stat -c %g /var/run/docker.sock)"
    if [ "$sock_gid" != "0" ]; then
        sock_group="$(name_by_id /etc/group "$sock_gid")"
        if [ -z "$sock_group" ]; then
            sock_group=docker-host
            addgroup -g "$sock_gid" "$sock_group"
        fi
        addgroup "$user" "$sock_group" 2>/dev/null || true
    fi
fi

# Only ownership is changed, mode bits stay as they are and symlinks are never followed.
for dir in /app/config /app/assets /app/.cache; do
    mkdir -p "$dir"
    find "$dir" \( ! -user "$PUID" -o ! -group "$PGID" \) -exec chown -h "$PUID:$PGID" {} + ||
        echo "Could not change ownership of $dir to $PUID:$PGID" >&2
done

if [ "$(id -g "$user")" = "$PGID" ]; then
    exec su-exec "$user" /app/dynacat "$@"
fi
exec su-exec "$PUID:$PGID" /app/dynacat "$@"
