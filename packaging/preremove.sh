#!/bin/sh
set -e
# Debian calls this with "remove" or "upgrade"; only stop the service when it goes away.
if [ "$1" = "remove" ] && [ -d /run/systemd/system ]; then
	systemctl disable --now dbexplore >/dev/null 2>&1 || true
fi
