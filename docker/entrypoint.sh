#!/bin/sh
set -e
DATA_DIR="${BUILTLY_DATA_DIR:-/data}"
if [ "$(id -u)" = "0" ]; then
	mkdir -p "$DATA_DIR"
	chown -R nonroot:nonroot "$DATA_DIR" 2>/dev/null || true
	exec su-exec nonroot /builtly "$@"
fi
exec /builtly "$@"
