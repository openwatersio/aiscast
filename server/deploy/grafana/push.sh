#!/bin/sh
# Apply the alert rules, their email contact point, and the capacity dashboard to Grafana Cloud. Rerun after
# any change in this directory; each step converges, and rules removed from rules.yaml are deleted.
# Usage: GRAFANA_URL=https://<stack>.grafana.net GRAFANA_TOKEN=<service account token> ALERT_EMAIL=<address> server/deploy/grafana/push.sh
# Needs curl, jq, and mimirtool. GRAFANA_PROM_UID overrides the Prometheus data source the rules query.
set -eu
cd "$(dirname "$0")"
: "${GRAFANA_URL:?}" "${GRAFANA_TOKEN:?}" "${ALERT_EMAIL:?}"
url=${GRAFANA_URL%/}
prom=${GRAFANA_PROM_UID:-grafanacloud-prom}
api() { curl -fsS -H "Authorization: Bearer $GRAFANA_TOKEN" -H 'Content-Type: application/json' "$@"; }

contact=$(jq -n --arg to "$ALERT_EMAIL" '{uid: "aiscast-email", name: "aiscast email", type: "email", settings: {addresses: $to}}')
if api "$url/api/v1/provisioning/contact-points?name=aiscast%20email" | jq -e 'length > 0' >/dev/null; then
	api -X PUT "$url/api/v1/provisioning/contact-points/aiscast-email" -d "$contact" >/dev/null
else
	api -X POST "$url/api/v1/provisioning/contact-points" -d "$contact" >/dev/null
fi

MIMIR_ADDRESS=$url/api/convert/ MIMIR_AUTH_TOKEN=$GRAFANA_TOKEN MIMIR_TENANT_ID=1 \
	mimirtool rules sync rules.yaml --concurrency 1 \
	--extra-headers "X-Grafana-Alerting-Datasource-UID=$prom" \
	--extra-headers 'X-Grafana-Alerting-Notification-Settings={"receiver":"aiscast email"}'

jq '{dashboard: ., overwrite: true, message: "server/deploy/grafana/push.sh"}' capacity.json |
	api -X POST "$url/api/dashboards/db" -d @- | jq -r '"dashboard: " + .url'
