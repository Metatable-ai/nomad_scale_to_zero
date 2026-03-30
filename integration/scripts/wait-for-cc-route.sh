#!/bin/bash
# Wait for Traefik ConsulCatalog route for slow-service to appear
for i in $(seq 1 20); do
  sleep 2
  # keepalive
  curl -s -H "Host: slow-service.localhost" http://localhost:80/ > /dev/null
  ROUTE=$(docker exec integration-traefik-1 wget -qO- "http://127.0.0.1:8080/api/http/routers" 2>&1 | python3 -c "
import json,sys
routers=json.load(sys.stdin)
found=[r for r in routers if 'slow-service' in r.get('name','') and 'consulcatalog' in r.get('provider','')]
print('yes' if found else 'no')
")
  echo "tick $i: ConsulCatalog route=$ROUTE"
  if [ "$ROUTE" = "yes" ]; then
    echo "ConsulCatalog route found!"
    exit 0
  fi
done
echo "ConsulCatalog route NOT found after 40s"
exit 1
