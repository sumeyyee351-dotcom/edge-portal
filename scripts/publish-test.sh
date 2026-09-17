#!/usr/bin/env bash
#
# publish-test.sh — sends one sample JSON telemetry message to the broker
# running in the edge-mosquitto container, so you can watch it show up on
# the portal at http://localhost:8080 (it auto-refreshes every 5s).
#
# Usage:
#   ./scripts/publish-test.sh
#   ./scripts/publish-test.sh '{"temperature": 30, "humidity": 55}'

set -euo pipefail

TOPIC="${MQTT_TOPIC:-devices/edge01/telemetry}"
PAYLOAD="${1:-{\"temperature\": $(( (RANDOM % 15) + 18 )), \"humidity\": $(( (RANDOM % 40) + 30 )), \"status\": \"ok\"}}"

echo "Publishing to '$TOPIC':"
echo "  $PAYLOAD"

docker exec -i edge-mosquitto mosquitto_pub -t "$TOPIC" -m "$PAYLOAD"

echo "Done. Check http://localhost:8080"
