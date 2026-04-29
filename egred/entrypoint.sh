#!/bin/sh
set -e

# Redirect agent traffic on the internal NIC to the egred proxy.
# Internal NIC is named per Docker conventions: usually eth0 because the
# 'internal' network is attached first; we don't pin to eth0 to remain
# robust if compose orders networks differently — instead we match by
# the EGRED_INTERNAL_SUBNET env var (e.g. 10.99.0.0/16).
SUBNET="${EGRED_INTERNAL_SUBNET:-10.99.0.0/16}"
PROXY_PORT="${EGRED_PROXY_PORT:-3128}"

# Drop existing rules in our chain so restart is idempotent.
iptables -t nat -N EGRED_REDIRECT 2>/dev/null || iptables -t nat -F EGRED_REDIRECT
iptables -t nat -A EGRED_REDIRECT -p tcp --dport 80  -j REDIRECT --to-port "$PROXY_PORT"
iptables -t nat -A EGRED_REDIRECT -p tcp --dport 443 -j REDIRECT --to-port "$PROXY_PORT"

# Hook into PREROUTING for traffic from the internal subnet.
# Idempotent: remove any prior matching jump first.
while iptables -t nat -C PREROUTING -s "$SUBNET" -p tcp -j EGRED_REDIRECT 2>/dev/null; do
    iptables -t nat -D PREROUTING -s "$SUBNET" -p tcp -j EGRED_REDIRECT
done
iptables -t nat -A PREROUTING -s "$SUBNET" -p tcp -j EGRED_REDIRECT

echo "egred: iptables REDIRECT installed for subnet=$SUBNET proxy_port=$PROXY_PORT"
exec /usr/local/bin/egred
