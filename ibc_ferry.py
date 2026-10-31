#!/usr/bin/env python3
"""Minimal one-way IBC v2 transfer relayer.

Scope: create the two clients needed for a single transfer, send one ICS20
packet, and relay the receive. It intentionally does not update clients, relay
acknowledgements, or timeouts. It uses each chain's own binary to sign and
broadcast JSON transactions, so it does not depend on a particular SDK version.

The destination client is created *after* the packet is sent, at the source's
current height. That makes the packet commitment proof verifiable against the
client's initial consensus state without an UpdateClient transaction.
"""

import argparse
import base64
import json
import os
import subprocess
import sys
import tempfile
import time
from datetime import datetime
import urllib.error
import urllib.request
from pathlib import Path

MERKLE_PREFIX = "aWJj"  # base64("ibc")


def run(cmd):
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        raise RuntimeError(f"command failed: {' '.join(cmd)}\n{proc.stdout}\n{proc.stderr}")
    return proc.stdout


def cli(chain, *args, with_chain=True):
    cmd = [chain["binary"], *args, "--home", chain["home"]]
    if with_chain:
        cmd += ["--chain-id", chain["chain_id"]]
    return cmd


def query_json(chain, *args):
    cmd = cli(chain, "q", *args, "--node", chain["node"], "-o", "json")
    return json.loads(run(cmd))


def rpc_get(chain, path):
    try:
        with urllib.request.urlopen(chain["rpc"] + path, timeout=10) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as exc:
        body = exc.read().decode(errors="replace")
        raise RuntimeError(f"rpc {path}: {exc.code} {body}") from exc


def latest_height(chain):
    return int(rpc_get(chain, "/status")["result"]["sync_info"]["latest_block_height"])


def block(chain, height):
    return rpc_get(chain, f"/block?height={height}")["result"]["block"]


def relayer_address(chain):
    out = run(cli(
        chain, "keys", "show", chain["from"], "-a",
        "--keyring-backend", chain["keyring_backend"], with_chain=False,
    ))
    return out.strip()


def unbonding_period(chain):
    params = query_json(chain, "staking", "params")
    return params["params"]["unbonding_time"]


def revision_number(chain_id):
    suffix = chain_id.rsplit("-", 1)[-1]
    return int(suffix) if suffix.isdigit() else 0


def write_json(data):
    f = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False)
    json.dump(data, f, separators=(",", ":"))
    f.close()
    return f.name


def wait_tx(chain, txhash):
    deadline = time.time() + 45
    while time.time() < deadline:
        proc = subprocess.run(
            cli(chain, "q", "tx", txhash, "--node", chain["node"], "-o", "json"),
            capture_output=True, text=True,
        )
        if proc.returncode == 0 and proc.stdout.strip():
            tx = json.loads(proc.stdout)
            if tx.get("code", 0) != 0:
                raise RuntimeError(f"tx {txhash} failed: {tx.get('raw_log')}")
            return tx
        time.sleep(1)
    raise RuntimeError(f"tx {txhash} not indexed")


def broadcast(chain, tx_json):
    if chain.get("fee"):
        tx_json["auth_info"]["fee"]["amount"] = chain["fee"]
    unsigned = write_json(tx_json)
    signed = unsigned + ".signed.json"
    try:
        run(cli(
            chain, "tx", "sign", unsigned, "--from", chain["from"],
            "--keyring-backend", chain["keyring_backend"], "--node", chain["node"],
            "--output-document", signed,
        ))
        if os.environ.get("SIMULATE") == "1":
            print(f"unsigned: {Path(unsigned).read_text()}")
            out = run(cli(
                chain, "tx", "simulate", signed, "--from", chain["from"],
                "--keyring-backend", chain["keyring_backend"], "--node", chain["node"], "-o", "json",
            ))
            print(f"simulate: {out}")
        out = run(cli(chain, "tx", "broadcast", signed, "--node", chain["node"], "-o", "json"))
        result = json.loads(out)
        if result.get("code", 0) != 0:
            raise RuntimeError(f"broadcast failed: {result}")
        return result["txhash"]
    finally:
        for path in (unsigned, signed):
            Path(path).unlink(missing_ok=True)


def empty_tx(message, gas="400000"):
    return {
        "body": {
            "messages": [message],
            "memo": "",
            "timeout_height": "0",
            "unordered": False,
            "timeout_timestamp": None,
            "extension_options": [],
            "non_critical_extension_options": [],
        },
        "auth_info": {
            "signer_infos": [],
            "fee": {"amount": [], "gas_limit": gas, "payer": "", "granter": ""},
            "tip": None,
        },
        "signatures": [],
    }


def create_client(chain, counterparty, client_id, height=None):
    if height is None:
        height = latest_height(counterparty)
    blk = block(counterparty, height)
    header = blk["header"]
    rev = revision_number(counterparty["chain_id"])
    client_state = {
        "@type": "/ibc.lightclients.tendermint.v1.ClientState",
        "chain_id": counterparty["chain_id"],
        "trust_level": {"numerator": "2", "denominator": "3"},
        "trusting_period": "1209600s",
        "unbonding_period": unbonding_period(counterparty),
        "max_clock_drift": "10s",
        "frozen_height": {"revision_number": "0", "revision_height": "0"},
        "latest_height": {"revision_number": str(rev), "revision_height": str(height)},
        "proof_specs": [
            {
                "inner_spec": {
                    "child_order": [0, 1], "child_size": 33,
                    "min_prefix_length": 4, "max_prefix_length": 12,
                    "empty_child": None, "hash": "SHA256",
                },
                "leaf_spec": {
                    "hash": "SHA256", "prehash_key": "NO_HASH",
                    "prehash_value": "SHA256", "length": "VAR_PROTO",
                    "prefix": "AA==",
                },
                "max_depth": 0, "min_depth": 0,
            },
            {
                "inner_spec": {
                    "child_order": [0, 1], "child_size": 32,
                    "min_prefix_length": 1, "max_prefix_length": 1,
                    "empty_child": None, "hash": "SHA256",
                },
                "leaf_spec": {
                    "hash": "SHA256", "prehash_key": "NO_HASH",
                    "prehash_value": "SHA256", "length": "VAR_PROTO",
                    "prefix": "AA==",
                },
                "max_depth": 0, "min_depth": 0,
            },
        ],
        "upgrade_path": ["upgrade", "upgradedIBCState"],
        "allow_update_after_expiry": False,
        "allow_update_after_misbehaviour": False,
    }
    consensus_state = {
        "@type": "/ibc.lightclients.tendermint.v1.ConsensusState",
        "timestamp": header["time"],
        # MerkleRoot.hash is base64 JSON, while next_validators_hash is hex.
        "root": {"hash": base64.b64encode(bytes.fromhex(header["app_hash"])).decode()},
        "next_validators_hash": header["next_validators_hash"],
    }
    msg = {
        "@type": "/ibc.core.client.v1.MsgCreateClient",
        "client_state": client_state,
        "consensus_state": consensus_state,
        "signer": relayer_address(chain),
    }
    txhash = broadcast(chain, empty_tx(msg, gas="2000000"))
    wait_tx(chain, txhash)
    return client_id, height


def add_counterparty(chain, client_id, counterparty_client_id):
    out = run(cli(
        chain, "tx", "ibc", "client", "add-counterparty", client_id,
        counterparty_client_id, MERKLE_PREFIX, "",
        "--from", chain["from"], "--keyring-backend", chain["keyring_backend"],
        "--node", chain["node"], "--gas", "400000", "--gas-prices", chain["gas_prices"],
        "-y", "-o", "json",
    ))
    result = json.loads(out)
    if result.get("code", 0) != 0:
        raise RuntimeError(f"add-counterparty failed: {result}")
    wait_tx(chain, result["txhash"])


def send_packet(src, src_client, denom, amount, receiver):
    block_time = block(src, latest_height(src))["header"]["time"]
    now = int(datetime.fromisoformat(block_time.replace("Z", "+00:00")).timestamp())
    timeout = now + 600
    packet_data = {
        "denom": denom,
        "amount": amount,
        "sender": relayer_address(src),
        "receiver": receiver,
        "memo": "",
    }
    value = base64.b64encode(json.dumps(packet_data, separators=(",", ":")).encode()).decode()
    sequence = int(query_json(src, "ibc", "channelv2", "next-sequence-send", src_client)["next_sequence_send"])
    msg = {
        "@type": "/ibc.core.channel.v2.MsgSendPacket",
        "source_client": src_client,
        "timeout_timestamp": str(timeout),
        "payloads": [{
            "source_port": "transfer",
            "destination_port": "transfer",
            "version": "ics20-1",
            "encoding": "application/json",
            "value": value,
        }],
        "signer": relayer_address(src),
    }
    txhash = broadcast(src, empty_tx(msg, gas="600000"))
    wait_tx(src, txhash)
    return sequence, timeout, msg["payloads"][0], latest_height(src)


def get_proof(src, src_client, sequence):
    proof = query_json(
        src, "ibc", "channelv2", "packet-commitment", src_client, str(sequence),
        "--prove",
    )
    print(f"proof height: {proof.get('proof_height')}")
    return proof


def recv_packet(dst, dst_client, src, src_client, sequence, timeout, payload, proof):
    packet = {
        "sequence": str(sequence),
        "source_client": src_client,
        "destination_client": dst_client,
        "timeout_timestamp": str(timeout),
        "payloads": [payload],
    }
    msg = {
        "@type": "/ibc.core.channel.v2.MsgRecvPacket",
        "packet": packet,
        "proof_commitment": proof["proof"],
        "proof_height": proof["proof_height"],
        "signer": relayer_address(dst),
    }
    txhash = broadcast(dst, empty_tx(msg, gas="700000"))
    wait_tx(dst, txhash)
    return txhash


def main():
    parser = argparse.ArgumentParser(description="Minimal one-way IBC v2 relayer")
    parser.add_argument("--config", required=True, help="JSON configuration file")
    args = parser.parse_args()

    cfg = json.loads(Path(args.config).read_text())
    src, dst = cfg["src"], cfg["dst"]
    src_client = cfg.get("src_client", "07-tendermint-0")
    dst_client = cfg.get("dst_client", "07-tendermint-0")

    print(f"creating {src_client} on {src['chain_id']}")
    src_client, _ = create_client(src, dst, src_client)
    print(f"adding counterparty {dst_client} to {src_client}")
    add_counterparty(src, src_client, dst_client)

    print(f"sending {cfg['amount']} {cfg['denom']} to {cfg['receiver']}")
    sequence, timeout, payload, send_height = send_packet(
        src, src_client, cfg["denom"], cfg["amount"], cfg["receiver"],
    )
    print(f"packet sequence {sequence}; waiting for a block after {send_height}")
    while latest_height(src) <= send_height:
        time.sleep(1)

    proof = get_proof(src, src_client, sequence)
    proof_height = int(proof["proof_height"]["revision_height"])
    while latest_height(src) < proof_height:
        time.sleep(1)
    print(f"creating {dst_client} on {dst['chain_id']} at source height {proof_height}")
    dst_client, _ = create_client(dst, src, dst_client, height=proof_height)
    print(f"adding counterparty {src_client} to {dst_client}")
    add_counterparty(dst, dst_client, src_client)
    print("relaying MsgRecvPacket")
    txhash = recv_packet(dst, dst_client, src, src_client, sequence, timeout, payload, proof)
    print(f"recv tx: {txhash}")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        sys.exit(1)
