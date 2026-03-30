#!/bin/sh
# CGI handler for slow-service — returns after ?delay=N seconds.
DELAY=$(echo "$QUERY_STRING" | sed -n 's/.*delay=\([0-9]*\).*/\1/p')
[ -z "$DELAY" ] && DELAY=0
[ "$DELAY" -gt 0 ] 2>/dev/null && sleep "$DELAY"
printf "Content-Type: text/plain\r\n\r\nDone after %ss delay\n" "$DELAY"
