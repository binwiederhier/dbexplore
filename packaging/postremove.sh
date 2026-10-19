#!/bin/sh
set -e
if [ "$1" = "purge" ]; then
	rm -rf /etc/dbexplore
	userdel dbexplore >/dev/null 2>&1 || true
fi
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
fi
