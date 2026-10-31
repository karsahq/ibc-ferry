#!/usr/bin/env bash
set -euo pipefail

KARSA_ROOT="${KARSA_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../karsa" && pwd)}"
FERRY="${FERRY_BIN:-/tmp/ibc-ferry-go}"
BIN="/tmp/karsad-ibc"
A=/tmp/karsa-ibc-a
B=/tmp/karsa-ibc-b
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

setup_chain() {
  local home="$1" chain="$2" rpc="$3" p2p="$4" grpc="$5" jsonrpc="$6"
  rm -rf "$home"
  "$BIN" init ibc --chain-id "$chain" --home "$home" >/dev/null 2>&1
  "$BIN" keys add val --keyring-backend "$KEYRING" --algo eth_secp256k1 --home "$home" >/dev/null 2>&1
  "$BIN" keys add relayer --keyring-backend "$KEYRING" --algo eth_secp256k1 --home "$home" >/dev/null 2>&1
  local val relayer
  val="$("$BIN" keys show val -a --keyring-backend "$KEYRING" --home "$home")"
  relayer="$("$BIN" keys show relayer -a --keyring-backend "$KEYRING" --home "$home")"
  "$BIN" genesis add-genesis-account "$val" 100000000000000000000000000akra \
    --keyring-backend "$KEYRING" --home "$home" >/dev/null
  "$BIN" genesis add-genesis-account "$relayer" 1000000000000000000000000akra \
    --keyring-backend "$KEYRING" --home "$home" >/dev/null
  "$BIN" genesis gentx val 1000000000000000000000akra \
    --chain-id "$chain" --keyring-backend "$KEYRING" --home "$home" >/dev/null
  "$BIN" genesis collect-gentxs --home "$home" >/dev/null
  sed -i.bak 's/^type = "flood"/type = "app"/' "$home/config/config.toml"
  python3 - "$home/config/genesis.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d["consensus"]["params"]["block"]["max_bytes"] = "3000000"
d["consensus"]["params"]["block"]["max_gas"] = "100000000"
json.dump(d, open(p, "w"), separators=(",", ":"))
PY
  "$BIN" start --home "$home" --chain-id "$chain" --keyring-backend "$KEYRING" \
    --log_level=debug \
    --minimum-gas-prices=0akra --evm.min-tip=0 --json-rpc.enable=true \
    --json-rpc.address "127.0.0.1:${jsonrpc}" \
    --json-rpc.ws-address "127.0.0.1:$((jsonrpc + 1))" \
    --json-rpc.api eth,txpool,personal,net,debug,web3 \
    --rpc.laddr "tcp://127.0.0.1:$rpc" --p2p.laddr "tcp://127.0.0.1:$p2p" \
    --grpc.address "127.0.0.1:$grpc" >"$home/node.log" 2>&1 &
  echo $!
}

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

( cd "$KARSA_ROOT" && go build -o "$BIN" ./cmd/karsad )
FERRY_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
( cd "$FERRY_SRC" && go build -o "$FERRY" . )

echo "setting up chain A"
PID_A="$(setup_chain "$A" karsa_262144-1 26657 26656 9090 18545)"
echo "setting up chain B"
PID_B="$(setup_chain "$B" karsa_262144-2 26658 26659 9091 18547)"

wait_blocks 26657 || { tail -40 "$A/node.log"; exit 1; }
wait_blocks 26658 || { tail -40 "$B/node.log"; exit 1; }
echo "both chains producing blocks"

if [[ "${RUN_BANK:-0}" == "1" ]]; then
  VAL_A="$("$BIN" keys show val -a --keyring-backend "$KEYRING" --home "$A")"
  OUT="$("$BIN" tx bank send relayer "$VAL_A" 1000akra --from relayer \
    --keyring-backend "$KEYRING" --home "$A" --chain-id karsa_262144-1 \
    --node tcp://127.0.0.1:26657 --broadcast-mode sync --gas auto \
    --gas-adjustment 1.3 --gas-prices 0akra -y -o json)"
  echo "bank send: $OUT"
  HASH="$(printf '%s' "$OUT" | sed -nE 's/.*"txhash":"([^"]+)".*/\1/p')"
  sleep 8
  "$BIN" q tx "$HASH" --node tcp://127.0.0.1:26657 --home "$A" -o json || true
  echo "height A: $(curl -s http://127.0.0.1:26657/status)"
  exit 0
fi

if [[ "${RUN_RELAYER:-1}" == "0" ]]; then
  sleep 10
  echo "height A: $(curl -s http://127.0.0.1:26657/status)"
  echo "height B: $(curl -s http://127.0.0.1:26658/status)"
  exit 0
fi

RECEIVER="$("$BIN" keys show relayer -a --keyring-backend "$KEYRING" --home "$B")"
cat >/tmp/ibc_v2_relay_karsa.json <<EOF
{
  "src": {
    "binary": "$BIN", "home": "$A", "node": "tcp://127.0.0.1:26657",
    "rpc": "http://127.0.0.1:26657", "chain_id": "karsa_262144-1",
    "keyring_backend": "$KEYRING", "from": "relayer", "gas_prices": "0akra"
  },
  "dst": {
    "binary": "$BIN", "home": "$B", "node": "tcp://127.0.0.1:26658",
    "rpc": "http://127.0.0.1:26658", "chain_id": "karsa_262144-2",
    "keyring_backend": "$KEYRING", "from": "relayer", "gas_prices": "0akra"
  },
  "src_client": "07-tendermint-0",
  "dst_client": "07-tendermint-0",
  "denom": "akra",
  "amount": "1000",
  "receiver": "$RECEIVER"
}
EOF

"$FERRY" --config /tmp/ibc_v2_relay_karsa.json

echo "receiver balance:"
"$BIN" q bank balances "$RECEIVER" --node tcp://127.0.0.1:26658 --home "$B" -o json
