# ibc-ferry

A minimal, one-way **IBC v2** packet ferry for native Cosmos chains.

This is the Go implementation. The validated Python prototype remains on the
`prototype/python` branch as a reference.

It is intentionally small: no database, no daemon, no retry loop, and no
`MsgUpdateClient`. It creates the two clients needed for a single transfer,
sends one ICS20 v2 packet, and relays `MsgRecvPacket`.

## What it does

1. Creates a client on the source chain tracking the destination.
2. Adds the counterparty client ID.
3. Sends one `MsgSendPacket` with an ICS20 payload.
4. Queries the packet commitment proof at the source's latest height.
5. Creates the destination client at that exact proof height.
6. Adds the counterparty on the destination.
7. Submits `MsgRecvPacket` to the destination.

Creating the destination client *after* the send, at the proof height, avoids
an `MsgUpdateClient` transaction. That is the trick that keeps this tool
minimal.

## Requirements

- Go 1.24+
- The chain binary for each side (`karsad`, `gaiad`, ...).
- A funded relayer key on both chains, in a keyring the binary can read.
- A JSON config file.

## Build and run

```bash
go build -o ibc-ferry .
./ibc-ferry --config config.json
```

Copy `config.example.json` and fill in:

| Field | Meaning |
|---|---|
| `src` / `dst` | Chain entries |
| `binary` | Path to the chain CLI |
| `home` | Chain home directory |
| `node` | CometBFT RPC, e.g. `tcp://127.0.0.1:26657` |
| `rpc` | CometBFT HTTP RPC, e.g. `http://127.0.0.1:26657` |
| `chain_id` | Cosmos chain ID |
| `keyring_backend` | `test`, `file`, ... |
| `from` | Relayer key name |
| `gas_prices` | Used by CLI transactions such as `add-counterparty` |
| `fee` | Optional fixed fee for JSON transactions (some chains reject zero fee) |
| `src_client` / `dst_client` | Client IDs. Use fresh IDs per run. |
| `denom` / `amount` / `receiver` | ICS20 transfer fields |

## How it works

The tool builds the v2 messages as JSON and lets each chain's own binary sign
and broadcast them:

```text
MsgCreateClient
MsgSendPacket
MsgRecvPacket
```

This avoids compiling against one SDK/ibc-go version for both chains. It has
been tested across Karsa (ibc-go v11) and Gaia v29 (ibc-go v10).

## Limitations

- **Receive only.** Acks and timeouts are not relayed, so the source packet
  stays unacknowledged and escrowed funds are not released.
- **No client updates.** A new destination client is created per run. Use a new
  client ID for the next run.
- **No retries or persistence.** If a step fails, rerun with fresh client IDs.
- **No batching.** One packet per invocation.
- **Experimental.** Not production relayer software.

## Tested

- Karsa <-> Karsa (two local nodes).
- Karsa -> Gaia v29 and Gaia v29 -> Karsa, native ICS20 v2 transfer.

Harnesses live in `examples/`. They expect the Karsa checkout at
`../karsa` by default; override with `KARSA_ROOT=/path/to/karsa`.

## Status

Experimental. Interfaces and the config format may change.
