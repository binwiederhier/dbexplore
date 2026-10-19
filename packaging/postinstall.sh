#!/bin/sh
set -e
if ! getent passwd dbexplore >/dev/null; then
	useradd --system --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin dbexplore
fi
mkdir -p /etc/dbexplore
chown dbexplore:dbexplore /etc/dbexplore
chmod 700 /etc/dbexplore
[ -f /etc/dbexplore/config.json ] && chown dbexplore:dbexplore /etc/dbexplore/config.json
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	systemctl enable dbexplore >/dev/null
	systemctl restart dbexplore
fi
echo "dbexplore is installed. Add your databases to /etc/dbexplore/config.json and run: sudo systemctl restart dbexplore"
echo "Login password: sudo -u dbexplore dbexplore -config /etc/dbexplore/config.json -print-password"
