#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

YES=0
if [ "${1:-}" = "--yes" ] || [ "${1:-}" = "-y" ]; then
	YES=1
fi

echo "This will delete geth chain data for:"
echo "  data/node1/geth"
echo "  data/node2/geth"
echo "  data/node3/geth"
echo "  data/node4/geth"
echo
echo "Keystores, password.txt, data/genesis.json, and data/address.txt will be kept."
echo

if [ "$YES" -ne 1 ]; then
	printf "Type 'delete geth data' to continue: "
	read -r answer
	if [ "$answer" != "delete geth data" ]; then
		echo "Aborted."
		exit 1
	fi
fi

if [ -x scripts/stop-partitioned-shadow-4nodes.sh ]; then
	scripts/stop-partitioned-shadow-4nodes.sh || true
fi

for idx in 1 2 3 4; do
	rm -rf "data/node${idx}/geth"
done

rm -rf data/run

echo "Deleted geth chain data. Restart from genesis with:"
echo "  ./scripts/start-partitioned-shadow-4nodes.sh"
