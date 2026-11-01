#!/usr/bin/env bash
# Relays a packet whose memo asks for an EVM destination callback that really
# executes. It starts two Karsa nodes, registers the IBC voucher as an ERC20 in
# the destination genesis, deploys the CounterWithCallbacks contract, links the
# chains with `ferry connect`, relays one packet, and checks that the contract
# ended up with the voucher.
#
# The helper under examples/callbackhelper is run inside the Karsa checkout,
# because it needs the chain's Go modules to derive the voucher denom, the
# isolated receiver address that the callback keeper expects, and the contract
# bytecode.
set -euo pipefail

KARSA_ROOT="${KARSA_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../karsa" && pwd)}"
FERRY_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${KARSAD_BIN:-/tmp/karsad-callback}"
FERRY="${FERRY_BIN:-/tmp/ibc-ferry-callback}"
A=/tmp/karsa-callback-a
B=/tmp/karsa-callback-b
KEYRING=test
STATE=/tmp/ferry-callback-state.db
CFG=/tmp/ferry-callback.toml
HELPER_JSON=/tmp/ferry-callback-helper.json
TEST_LOG=/tmp/ferry-callback-test.log
ERC20=0x2000000000000000000000000000000000000002
AMOUNT=1000
RPC_A=26681
RPC_B=26682
EVM_B=18577
PID_A=""
PID_B=""

cleanup() {
  [[ -n "$PID_A" ]] && kill "$PID_A" 2>/dev/null || true
  [[ -n "$PID_B" ]] && kill "$PID_B" 2>/dev/null || true
  [[ -n "$PID_A" ]] && wait "$PID_A" 2>/dev/null || true
  [[ -n "$PID_B" ]] && wait "$PID_B" 2>/dev/null || true
}
trap cleanup EXIT

rpc_b() { curl -s -m "${2:-10}" -X POST -H 'Content-Type: application/json' -d "$1" "http://127.0.0.1:$EVM_B"; }

prep_chain() {
  local home="$1" chain="$2"
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
    --gas-prices 20000000000akra --chain-id "$chain" --keyring-backend "$KEYRING" --home "$home" >/dev/null
  "$BIN" genesis collect-gentxs --home "$home" >/dev/null
  sed -i.bak 's/^type = "flood"/type = "app"/' "$home/config/config.toml"
  python3 - "$home/config/genesis.json" <<'PY'
import json, sys
path = sys.argv[1]
g = json.load(open(path))
g["consensus"]["params"]["block"]["max_bytes"] = "3000000"
g["consensus"]["params"]["block"]["max_gas"] = "100000000"
json.dump(g, open(path, "w"), separators=(",", ":"))
PY
}

start_chain() {
  local home="$1" chain="$2" rpc="$3" p2p="$4" grpc="$5" evm="$6"
  "$BIN" start --home "$home" --chain-id "$chain" --keyring-backend "$KEYRING" \
    --minimum-gas-prices=20000000000akra --evm.min-tip=20000000000 --json-rpc.enable=true \
    --json-rpc.address "127.0.0.1:${evm}" --json-rpc.ws-address "127.0.0.1:$((evm + 1))" \
    --json-rpc.api eth,txpool,personal,net,debug,web3 \
    --rpc.laddr "tcp://127.0.0.1:$rpc" --p2p.laddr "tcp://127.0.0.1:$p2p" \
    --grpc.address "127.0.0.1:$grpc" >"$home/node.log" 2>&1 &
  echo $!
}

wait_blocks() {
  local rpc="$1"
  for _ in $(seq 1 90); do
    out="$(curl -s -m 2 "http://127.0.0.1:$rpc/status" || true)"
    if [[ "$out" == *'"latest_block_height"'* && "$out" != *'"latest_block_height":"0"'* ]]; then return 0; fi
    sleep 1
  done
  return 1
}

echo "== build =="
( cd "$KARSA_ROOT" && go build -o "$BIN" ./cmd/karsad )
( cd "$FERRY_SRC" && go build -o "$FERRY" . )

echo "== prepare chains =="
prep_chain "$A" karsa_262144-1
prep_chain "$B" karsa_262144-2

SENDER="$("$BIN" keys show relayer -a --keyring-backend "$KEYRING" --home "$A")"
( cd "$KARSA_ROOT" && go run "$FERRY_SRC/examples/callbackhelper/main.go" \
    -sender "$SENDER" -dest-client 07-tendermint-0 -erc20 "$ERC20" -amount "$AMOUNT" > "$HELPER_JSON" )

VOUCHER="$(python3 -c 'import json;print(json.load(open("/tmp/ferry-callback-helper.json"))["voucher_denom"])')"
ISOLATED="$(python3 -c 'import json;print(json.load(open("/tmp/ferry-callback-helper.json"))["isolated_bech32"])')"
ISOLATED_HEX="$(python3 -c 'import json;print(json.load(open("/tmp/ferry-callback-helper.json"))["isolated_hex"])')"
BYTECODE="$(python3 -c 'import json;print(json.load(open("/tmp/ferry-callback-helper.json"))["contract_bytecode"])')"
CALLDATA="$(python3 -c 'import json;print(json.load(open("/tmp/ferry-callback-helper.json"))["calldata"])')"
echo "voucher  $VOUCHER"
echo "isolated $ISOLATED"

python3 - "$B/config/genesis.json" "$VOUCHER" "$ERC20" <<'PY'
import json, sys
path, voucher, erc20 = sys.argv[1], sys.argv[2], sys.argv[3]
g = json.load(open(path))
pair = {"erc20_address": erc20, "denom": voucher, "enabled": True, "contract_owner": "OWNER_MODULE"}
g["app_state"]["erc20"]["token_pairs"].append(pair)
g["app_state"]["erc20"]["dynamic_precompiles"].append(erc20)
g["app_state"]["bank"]["denom_metadata"].append({
    "description": "IBC voucher of akra",
    "denom_units": [{"denom": voucher, "exponent": 0, "aliases": []}],
    "base": voucher, "display": voucher, "name": "Karsa voucher", "symbol": "KRA.V",
    "uri": "", "uri_hash": "",
})
json.dump(g, open(path, "w"), separators=(",", ":"))
PY

echo "== start chains =="
PID_A="$(start_chain "$A" karsa_262144-1 "$RPC_A" 26680 9099 18575)"
PID_B="$(start_chain "$B" karsa_262144-2 "$RPC_B" 26683 9100 "$EVM_B")"
wait_blocks "$RPC_A" || { tail -30 "$A/node.log"; exit 1; }
wait_blocks "$RPC_B" || { tail -30 "$B/node.log"; exit 1; }

cat > "$CFG" <<TOML
[chains.a]
binary = "$BIN"
home = "$A"
node = "tcp://127.0.0.1:$RPC_A"
rpc = "http://127.0.0.1:$RPC_A"
chain_id = "karsa_262144-1"
keyring_backend = "$KEYRING"
from = "relayer"
gas_prices = "22000000000akra"

[chains.b]
binary = "$BIN"
home = "$B"
node = "tcp://127.0.0.1:$RPC_B"
rpc = "http://127.0.0.1:$RPC_B"
chain_id = "karsa_262144-2"
keyring_backend = "$KEYRING"
from = "relayer"
gas_prices = "22000000000akra"
TOML

rm -f "$STATE" "$STATE-shm" "$STATE-wal"
echo "== connect =="
"$FERRY" --config "$CFG" --state "$STATE" connect a b

echo "== deploy the callback contract on b =="
VAL_B_EVM="$("$BIN" debug addr "$("$BIN" keys show val -a --keyring-backend "$KEYRING" --home "$B")" --home "$B" | sed -nE 's/.*Address hex: (0x[0-9a-fA-F]+).*/\1/p')"
DEPLOY="$(rpc_b "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"$VAL_B_EVM\",\"gas\":\"0x300000\",\"gasPrice\":\"0x9502f9000\",\"data\":\"$BYTECODE\"}]}")"
DEPLOY_HASH="$(printf '%s' "$DEPLOY" | sed -nE 's/.*"result":"(0x[0-9a-fA-F]+)".*/\1/p')"
[[ -n "$DEPLOY_HASH" ]] || { echo "deploy failed: $DEPLOY"; exit 1; }
for _ in $(seq 1 40); do
  RECEIPT="$(rpc_b "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getTransactionReceipt\",\"params\":[\"$DEPLOY_HASH\"]}")"
  if printf '%s' "$RECEIPT" | grep -q '"contractAddress":"0x'; then break; fi
  sleep 1
done
CONTRACT="$(printf '%s' "$RECEIPT" | sed -nE 's/.*"contractAddress":"(0x[0-9a-fA-F]+)".*/\1/p')"
[[ -n "$CONTRACT" ]] || { echo "no contract address: $RECEIPT"; exit 1; }
echo "contract $CONTRACT"

MEMO="{\"dest_callback\":{\"address\":\"$CONTRACT\",\"gas_limit\":\"1000000\",\"calldata\":\"$CALLDATA\"}}"

echo "== relay a packet with a destination callback =="
sleep 12
cd "$FERRY_SRC"
FERRY_LIVE_CONFIG="$CFG" FERRY_LIVE_STATE="$STATE" FERRY_LIVE_SRC=a FERRY_LIVE_DST=b \
  FERRY_LIVE_DENOM=akra FERRY_LIVE_RECEIVER="$ISOLATED" FERRY_LIVE_MEMO="$MEMO" \
  go test -tags=live -run '^TestLiveCallbackGas$' -v -count=1 -timeout 10m 2>&1 | tee "$TEST_LOG" | tail -8

echo "== check the result =="
BALANCE="$(rpc_b "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_call\",\"params\":[{\"to\":\"$ERC20\",\"data\":\"0x70a08231000000000000000000000000${CONTRACT:2}\"},\"latest\"]}")"
STUCK="$(rpc_b "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_call\",\"params\":[{\"to\":\"$ERC20\",\"data\":\"0x70a08231000000000000000000000000${ISOLATED_HEX:2}\"},\"latest\"]}")"
echo "contract voucher balance: $BALANCE"
echo "isolated voucher balance: $STUCK"
printf '%s' "$BALANCE" | grep -q "$(printf '0x%064x' "$AMOUNT")" || { echo "callback did not move the voucher"; exit 1; }
printf '%s' "$STUCK" | grep -q '0x0000000000000000000000000000000000000000000000000000000000000000' \
  || { echo "voucher stuck at the isolated address"; exit 1; }
echo "destination callback executed end to end"
