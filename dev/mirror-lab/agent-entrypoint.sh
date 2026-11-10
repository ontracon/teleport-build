#!/bin/sh
set -eu
if [ -f /lab/ca.pem ]; then
    cp /lab/ca.pem /usr/local/share/ca-certificates/teleport-lab.crt
    update-ca-certificates >/dev/null
fi
exec /sbin/init
