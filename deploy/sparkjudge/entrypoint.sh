#!/bin/sh
# Hand the data volume to the app user, then run the API as that user.
set -eu
mkdir -p "$SPARKJUDGE_DATA_DIR"
chown sparkjudge:sparkjudge "$SPARKJUDGE_DATA_DIR"
exec su-exec sparkjudge /usr/local/bin/sparkjudge-api "$@"
