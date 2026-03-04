#!/bin/sh
set -e

# Allow running bundled utilities directly, e.g.:
#   docker run --rm <image> hashpw 'mypassword'
case "${1:-}" in
    hashpw)
        exec /usr/local/bin/hashpw "$2"
        ;;
esac

CONFIG_PATH="/app/config.yaml"

# If ADMIN_PASSWORD is set and a config file exists, generate a bcrypt hash
# and replace the password_hash value in the config.
if [ -n "$ADMIN_PASSWORD" ] && [ -f "$CONFIG_PATH" ]; then
    HASH=$(hashpw "$ADMIN_PASSWORD")
    export HASH

    awk '
        /password_hash:/ {
            match($0, /^[[:space:]]*/);
            indent = substr($0, RSTART, RLENGTH);
            print indent "password_hash: " ENVIRON["HASH"];
            next
        }
        { print }
    ' "$CONFIG_PATH" > /tmp/config.yaml

    # Try to update in place; if the mount is read-only, use the copy instead
    if cp /tmp/config.yaml "$CONFIG_PATH" 2>/dev/null; then
        rm /tmp/config.yaml
    else
        CONFIG_PATH="/tmp/config.yaml"
        echo "Config mount is read-only; using modified copy."
    fi

    unset ADMIN_PASSWORD HASH
    echo "Admin password updated from ADMIN_PASSWORD environment variable."
fi

exec /app/ai-gateway -config "$CONFIG_PATH" "$@"
