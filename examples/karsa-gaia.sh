#!/usr/bin/env bash
set -euo pipefail

KARSA_ROOT="${KARSA_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../karsa" && pwd)}"
FERRY="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/ibc_ferry.py"
KARSAD="/tmp/karsad-ibc"
GAIAD="/home/derika/.local/bin/gaiad"
A=/tmp/karsa-ibc-a
B=/tmp/gaia-ibc
KEYRING=test
PID_A=""
PID_B=""

cleanup() {
  [[ -n "$PID_A" ]] && kill "$PID_A" 2>/dev/null || true
  [[ -n "$PID_B" ]] && kill "$PID_B" 2>/dev/null || true
  [[ -n "$PID_A" ]] && wait "$PID_A" 2>/dev/null || true
  [[ -n "$PID_B" ]] && wait "$PID_B" 2>/dev/null || true
}
trap cleanup EXIT

wait_blocks() {
  local rpc="$1"
  for _ in $(seq 1 60); do
    out="$(curl -s -m 2 "http://127.0.0.1:$rpc/status" || true)"
    if [[ "$out" == *'"latest_block_height"'* && "$out" != *'"latest_block_height":"0"'* ]]; then
      return 0
    fi
    sleep 1
  done
  return 1
}

( cd "$KARSA_ROOT" && go build -o "$KARSAD" ./cmd/karsad )

rm -rf "$A" "$B"

# Karsa chain
"$KARSAD" init ibc --chain-id karsa_262144-1 --home "$A" >/dev/null 2>&1
"$KARSAD" keys add val --keyring-backend "$KEYRING" --algo eth_secp256k1 --home "$A" >/dev/null 2>&1
"$KARSAD" keys add relayer --keyring-backend "$KEYRING" --algo eth_secp256k1 --home "$A" >/dev/null 2>&1
VAL_A="$("$KARSAD" keys show val -a --keyring-backend "$KEYRING" --home "$A")"
REL_A="$("$KARSAD" keys show relayer -a --keyring-backend "$KEYRING" --home "$A")"
"$KARSAD" genesis add-genesis-account "$VAL_A" 100000000000000000000000000akra \
  --keyring-backend "$KEYRING" --home "$A" >/dev/null
"$KARSAD" genesis add-genesis-account "$REL_A" 1000000000000000000000000akra \
  --keyring-backend "$KEYRING" --home "$A" >/dev/null
"$KARSAD" genesis gentx val 1000000000000000000000akra \
  --chain-id karsa_262144-1 --keyring-backend "$KEYRING" --home "$A" >/dev/null
"$KARSAD" genesis collect-gentxs --home "$A" >/dev/null
sed -i.bak 's/^type = "flood"/type = "app"/' "$A/config/config.toml"
python3 - "$A/config/genesis.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d["consensus"]["params"]["block"]["max_bytes"] = "3000000"
d["consensus"]["params"]["block"]["max_gas"] = "100000000"
json.dump(d, open(p, "w"), separators=(",", ":"))
PY

# Gaia chain
"$GAIAD" init ibc --chain-id gaia-test-1 --default-denom stake --home "$B" >/dev/null 2>&1
"$GAIAD" keys add val --keyring-backend "$KEYRING" --home "$B" >/dev/null 2>&1
"$GAIAD" keys add relayer --keyring-backend "$KEYRING" --home "$B" >/dev/null 2>&1
VAL_B="$("$GAIAD" keys show val -a --keyring-backend "$KEYRING" --home "$B")"
REL_B="$("$GAIAD" keys show relayer -a --keyring-backend "$KEYRING" --home "$B")"
"$GAIAD" genesis add-genesis-account "$VAL_B" 100000000000000000stake \
  --keyring-backend "$KEYRING" --home "$B" >/dev/null
"$GAIAD" genesis add-genesis-account "$REL_B" 1000000000000000stake \
  --keyring-backend "$KEYRING" --home "$B" >/dev/null
"$GAIAD" genesis gentx val 1000000000000stake \
  --chain-id gaia-test-1 --keyring-backend "$KEYRING" --home "$B" >/dev/null
"$GAIAD" genesis collect-gentxs --home "$B" >/dev/null

"$KARSAD" start --home "$A" --chain-id karsa_262144-1 --keyring-backend "$KEYRING" \
  --minimum-gas-prices=0akra --evm.min-tip=0 --json-rpc.enable=true \
  --json-rpc.address 127.0.0.1:18545 --json-rpc.ws-address 127.0.0.1:18546 \
  --json-rpc.api eth,txpool,personal,net,debug,web3 \
  --rpc.laddr tcp://127.0.0.1:26657 --p2p.laddr tcp://127.0.0.1:26656 \
  --grpc.address 127.0.0.1:9090 >"$A/node.log" 2>&1 &
PID_A=$!

"$GAIAD" start --home "$B" --minimum-gas-prices=0stake \
  --rpc.laddr tcp://127.0.0.1:26658 --p2p.laddr tcp://127.0.0.1:26659 \
  --grpc.address 127.0.0.1:9091 >"$B/node.log" 2>&1 &
PID_B=$!

wait_blocks 26657 || { tail -60 "$A/node.log"; exit 1; }
wait_blocks 26658 || { tail -60 "$B/node.log"; exit 1; }
echo "both chains producing blocks"

cat >/tmp/ibc_v2_relay_karsa_gaia.json <<EOF
{
  "src": {
    "binary": "$KARSAD", "home": "$A", "node": "tcp://127.0.0.1:26657",
    "rpc": "http://127.0.0.1:26657", "chain_id": "karsa_262144-1",
    "keyring_backend": "$KEYRING", "from": "relayer", "gas_prices": "0akra"
  },
  "dst": {
    "binary": "$GAIAD", "home": "$B", "node": "tcp://127.0.0.1:26658",
    "rpc": "http://127.0.0.1:26658", "chain_id": "gaia-test-1",
    "keyring_backend": "$KEYRING", "from": "relayer", "gas_prices": "1stake",
    "fee": [{"denom": "stake", "amount": "5000000"}]
  },
  "src_client": "07-tendermint-0",
  "dst_client": "07-tendermint-0",
  "denom": "akra",
  "amount": "1000",
  "receiver": "$REL_B"
}
EOF

python3 "$FERRY" --config /tmp/ibc_v2_relay_karsa_gaia.json

echo "gaia receiver balance:"
"$GAIAD" q bank balances "$REL_B" --node tcp://127.0.0.1:26658 --home "$B" -o json

cat >/tmp/ibc_v2_relay_gaia_karsa.json <<EOF
{
  "src": {
    "binary": "$GAIAD", "home": "$B", "node": "tcp://127.0.0.1:26658",
    "rpc": "http://127.0.0.1:26658", "chain_id": "gaia-test-1",
    "keyring_backend": "$KEYRING", "from": "relayer", "gas_prices": "1stake",
    "fee": [{"denom": "stake", "amount": "5000000"}]
  },
  "dst": {
    "binary": "$KARSAD", "home": "$A", "node": "tcp://127.0.0.1:26657",
    "rpc": "http://127.0.0.1:26657", "chain_id": "karsa_262144-1",
    "keyring_backend": "$KEYRING", "from": "relayer", "gas_prices": "0akra"
  },
  "src_client": "07-tendermint-1",
  "dst_client": "07-tendermint-1",
  "denom": "stake",
  "amount": "1000",
  "receiver": "$REL_A"
}
EOF

python3 "$FERRY" --config /tmp/ibc_v2_relay_gaia_karsa.json

echo "karsa receiver balance:"
"$KARSAD" q bank balances "$REL_A" --node tcp://127.0.0.1:26657 --home "$A" -o json
