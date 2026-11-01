package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"
)

const merklePrefix = "aWJj" // base64("ibc")

var (
	errPacketReceived    = errors.New("packet already received")
	errTimeoutNotReached = errors.New("timeout not reached")
)

func createClient(chain, counterparty ChainConfig, height int64) (string, int64, error) {
	if height == 0 {
		var err error
		height, err = latestHeight(counterparty)
		if err != nil {
			return "", 0, err
		}
	}
	header, err := blockAt(counterparty, height)
	if err != nil {
		return "", 0, err
	}
	unbonding, err := unbondingPeriod(counterparty)
	if err != nil {
		return "", 0, err
	}
	appHash, err := hex.DecodeString(header.AppHash)
	if err != nil {
		return "", 0, fmt.Errorf("decode app hash: %w", err)
	}
	signer, err := relayerAddress(chain)
	if err != nil {
		return "", 0, err
	}

	clientState := map[string]any{
		"@type":            "/ibc.lightclients.tendermint.v1.ClientState",
		"chain_id":         counterparty.ChainID,
		"trust_level":      map[string]any{"numerator": "2", "denominator": "3"},
		"trusting_period":  "1209600s",
		"unbonding_period": unbonding,
		"max_clock_drift":  "10s",
		"frozen_height":    map[string]any{"revision_number": "0", "revision_height": "0"},
		"latest_height": map[string]any{
			"revision_number": strconv.FormatUint(revisionNumber(counterparty.ChainID), 10),
			"revision_height": strconv.FormatInt(height, 10),
		},
		"proof_specs": []any{
			proofSpec(33, 4, 12),
			proofSpec(32, 1, 1),
		},
		"upgrade_path":                    []any{"upgrade", "upgradedIBCState"},
		"allow_update_after_expiry":       false,
		"allow_update_after_misbehaviour": false,
	}
	consensusState := map[string]any{
		"@type":                "/ibc.lightclients.tendermint.v1.ConsensusState",
		"timestamp":            header.Time,
		"root":                 map[string]any{"hash": base64.StdEncoding.EncodeToString(appHash)},
		"next_validators_hash": header.NextValidatorsHash,
	}
	msg := map[string]any{
		"@type":           "/ibc.core.client.v1.MsgCreateClient",
		"client_state":    clientState,
		"consensus_state": consensusState,
		"signer":          signer,
	}

	hash, err := broadcast(chain, emptyTx(msg, "2000000"))
	if err != nil {
		return "", 0, err
	}
	tx, err := waitTxResult(chain, hash)
	if err != nil {
		return "", 0, err
	}
	clientID := txEventValue(tx, "create_client", "client_id")
	if clientID == "" {
		return "", 0, fmt.Errorf("tx %s did not report a create_client client_id", hash)
	}
	return clientID, height, nil
}

func txEventValue(tx map[string]any, eventType, key string) string {
	events, _ := tx["events"].([]any)
	for _, rawEvent := range events {
		event, _ := rawEvent.(map[string]any)
		if event["type"] != eventType {
			continue
		}
		attributes, _ := event["attributes"].([]any)
		for _, rawAttr := range attributes {
			attr, _ := rawAttr.(map[string]any)
			if attr["key"] == key {
				return str(attr["value"])
			}
		}
	}
	return ""
}

func proofSpec(childSize, minPrefix, maxPrefix int) map[string]any {
	return map[string]any{
		"inner_spec": map[string]any{
			"child_order":       []any{0, 1},
			"child_size":        childSize,
			"min_prefix_length": minPrefix,
			"max_prefix_length": maxPrefix,
			"empty_child":       nil,
			"hash":              "SHA256",
		},
		"leaf_spec": map[string]any{
			"hash":          "SHA256",
			"prehash_key":   "NO_HASH",
			"prehash_value": "SHA256",
			"length":        "VAR_PROTO",
			"prefix":        "AA==",
		},
		"max_depth": 0,
		"min_depth": 0,
	}
}

func addCounterparty(chain ChainConfig, clientID, counterpartyClientID string) error {
	out, err := runCLI(
		chain, true, "tx", "ibc", "client", "add-counterparty",
		clientID, counterpartyClientID, merklePrefix, "",
		"--from", chain.From,
		"--keyring-backend", chain.KeyringBackend,
		"--node", chain.Node,
		"--gas", "400000",
		"--gas-prices", chain.GasPrices,
		"-y", "-o", "json",
	)
	if err != nil {
		return err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return fmt.Errorf("decode add-counterparty: %w\n%s", err, out)
	}
	if code, ok := result["code"].(float64); ok && code != 0 {
		return fmt.Errorf("add-counterparty failed: %v", result)
	}
	hash, _ := result["txhash"].(string)
	if hash == "" {
		return fmt.Errorf("add-counterparty returned no txhash: %s", out)
	}
	return waitTx(chain, hash)
}

type sendResult struct {
	Sequence int64
	Timeout  int64
	Payload  map[string]any
	Height   int64
}

func sendPacket(src ChainConfig, srcClient, denom, amount, receiver string) (sendResult, error) {
	return sendPacketWithTimeout(src, srcClient, denom, amount, receiver, 600)
}

func sendPacketWithTimeout(src ChainConfig, srcClient, denom, amount, receiver string, timeoutSeconds int64) (sendResult, error) {
	height, err := latestHeight(src)
	if err != nil {
		return sendResult{}, err
	}
	header, err := blockAt(src, height)
	if err != nil {
		return sendResult{}, err
	}
	blockTime, err := time.Parse(time.RFC3339Nano, header.Time)
	if err != nil {
		return sendResult{}, fmt.Errorf("parse block time: %w", err)
	}
	timeout := blockTime.Unix() + timeoutSeconds
	signer, err := relayerAddress(src)
	if err != nil {
		return sendResult{}, err
	}
	packetData := map[string]string{
		"denom":    denom,
		"amount":   amount,
		"sender":   signer,
		"receiver": receiver,
		"memo":     "",
	}
	raw, err := json.Marshal(packetData)
	if err != nil {
		return sendResult{}, err
	}
	payload := map[string]any{
		"source_port":      "transfer",
		"destination_port": "transfer",
		"version":          "ics20-1",
		"encoding":         "application/json",
		"value":            base64.StdEncoding.EncodeToString(raw),
	}

	seqRes, err := queryJSON(src, "ibc", "channelv2", "next-sequence-send", srcClient)
	if err != nil {
		return sendResult{}, err
	}
	sequence, err := asInt64(seqRes["next_sequence_send"])
	if err != nil {
		return sendResult{}, fmt.Errorf("next sequence: %w", err)
	}

	msg := map[string]any{
		"@type":             "/ibc.core.channel.v2.MsgSendPacket",
		"source_client":     srcClient,
		"timeout_timestamp": strconv.FormatInt(timeout, 10),
		"payloads":          []any{payload},
		"signer":            signer,
	}
	hash, err := broadcast(src, emptyTx(msg, "600000"))
	if err != nil {
		return sendResult{}, err
	}
	if err := waitTx(src, hash); err != nil {
		return sendResult{}, err
	}
	height, err = latestHeight(src)
	if err != nil {
		return sendResult{}, err
	}
	return sendResult{Sequence: sequence, Timeout: timeout, Payload: payload, Height: height}, nil
}

type proof struct {
	Commitment string
	Height     map[string]any
	HeightNum  int64
}

func getProof(src ChainConfig, srcClient string, sequence int64) (proof, error) {
	res, err := queryJSON(
		src, "ibc", "channelv2", "packet-commitment",
		srcClient, strconv.FormatInt(sequence, 10), "--prove",
	)
	if err != nil {
		return proof{}, err
	}
	return parseCommitmentProof(res)
}

func getProofAtHeight(src ChainConfig, srcClient string, sequence, height int64) (proof, error) {
	res, err := queryJSON(
		src, "ibc", "channelv2", "packet-commitment",
		srcClient, strconv.FormatInt(sequence, 10), "--prove",
		"--height", strconv.FormatInt(height, 10),
	)
	if err != nil {
		return proof{}, err
	}
	return parseCommitmentProof(res)
}

func parseCommitmentProof(res map[string]any) (proof, error) {
	commitment, _ := res["proof"].(string)
	proofHeight, _ := res["proof_height"].(map[string]any)
	if commitment == "" || proofHeight == nil {
		return proof{}, fmt.Errorf("proof query returned incomplete data: %v", res)
	}
	height, err := asInt64(proofHeight["revision_height"])
	if err != nil {
		return proof{}, fmt.Errorf("proof height: %w", err)
	}
	return proof{Commitment: commitment, Height: proofHeight, HeightNum: height}, nil
}

func recvPacket(dst ChainConfig, dstClient string, srcClient string, sequence, timeout int64, payload, proofHeight map[string]any, commitment string) (string, string, error) {
	return recvPacketWithPayloads(dst, dstClient, srcClient, sequence, timeout, []map[string]any{payload}, proofHeight, commitment)
}

func recvPacketWithPayloads(dst ChainConfig, dstClient string, srcClient string, sequence, timeout int64, payloads []map[string]any, proofHeight map[string]any, commitment string) (string, string, error) {
	msg, err := recvMessage(dst, dstClient, srcClient, sequence, timeout, payloads, proofHeight, commitment)
	if err != nil {
		return "", "", err
	}
	hash, err := broadcast(dst, emptyTx(msg, strconv.FormatInt(relayGas(msg, singleRelayGas), 10)))
	if err != nil {
		return "", "", err
	}
	if err := waitTx(dst, hash); err != nil {
		return "", "", err
	}
	ack, err := recvAcknowledgement(dst, hash)
	if err != nil {
		return "", "", err
	}
	return hash, ack, nil
}

func recvMessage(dst ChainConfig, dstClient string, srcClient string, sequence, timeout int64, payloads []map[string]any, proofHeight map[string]any, commitment string) (map[string]any, error) {
	signer, err := relayerAddress(dst)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"@type":            "/ibc.core.channel.v2.MsgRecvPacket",
		"packet":           packetMessageWithPayloads(sequence, timeout, srcClient, dstClient, payloads),
		"proof_commitment": commitment,
		"proof_height":     proofHeight,
		"signer":           signer,
	}, nil
}

func recvAcknowledgement(chain ChainConfig, hash string) (string, error) {
	acks, err := recvAcknowledgements(chain, hash)
	if err != nil {
		return "", err
	}
	for _, ack := range acks {
		return ack, nil
	}
	return "", fmt.Errorf("write_acknowledgement event not found in %s", hash)
}

func recvAcknowledgementForSequence(chain ChainConfig, hash string, sequence int64) (string, error) {
	acks, err := recvAcknowledgements(chain, hash)
	if err != nil {
		return "", err
	}
	ack := acks[sequence]
	if ack == "" {
		return "", fmt.Errorf("acknowledgement for sequence %d not found in %s", sequence, hash)
	}
	return ack, nil
}

func recvAcknowledgements(chain ChainConfig, hash string) (map[int64]string, error) {
	out, err := runCLI(chain, true, "q", "tx", hash, "--node", chain.Node, "-o", "json")
	if err != nil {
		return nil, err
	}
	var tx map[string]any
	if err := json.Unmarshal([]byte(out), &tx); err != nil {
		return nil, fmt.Errorf("decode recv tx: %w", err)
	}
	acks := make(map[int64]string)
	events, _ := tx["events"].([]any)
	for _, rawEvent := range events {
		event, _ := rawEvent.(map[string]any)
		if event["type"] != "write_acknowledgement" {
			continue
		}
		var sequence int64
		var encoded string
		attributes, _ := event["attributes"].([]any)
		for _, rawAttr := range attributes {
			attr, _ := rawAttr.(map[string]any)
			switch attr["key"] {
			case "packet_sequence":
				sequence, _ = strconv.ParseInt(str(attr["value"]), 10, 64)
			case "encoded_acknowledgement_hex":
				encoded = str(attr["value"])
			}
		}
		if sequence == 0 || encoded == "" {
			continue
		}
		ack, err := hex.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode ack event for sequence %d: %w", sequence, err)
		}
		acks[sequence] = base64.StdEncoding.EncodeToString(ack)
	}
	return acks, nil
}

func queryClientHeader(chain ChainConfig, height int64) (map[string]any, error) {
	return queryJSON(chain, "ibc", "client", "header", "--height", strconv.FormatInt(height, 10))
}

func clientLatestHeight(chain ChainConfig, clientID string) (map[string]any, int64, error) {
	res, err := queryJSON(chain, "ibc", "client", "state", clientID)
	if err != nil {
		return nil, 0, err
	}
	state, _ := res["client_state"].(map[string]any)
	height, _ := state["latest_height"].(map[string]any)
	if height == nil {
		return nil, 0, fmt.Errorf("client %s has no latest height: %v", clientID, res)
	}
	value, err := asInt64(height["revision_height"])
	if err != nil {
		return nil, 0, err
	}
	return height, value, nil
}

func updateClient(chain, counterparty ChainConfig, clientID string, height int64) error {
	trustedHeight, trustedHeightNum, err := clientLatestHeight(chain, clientID)
	if err != nil {
		return err
	}
	if height <= trustedHeightNum {
		return fmt.Errorf("update height %d is not above trusted height %d", height, trustedHeightNum)
	}
	for {
		current, err := latestHeight(counterparty)
		if err != nil {
			return err
		}
		if current >= height {
			break
		}
		time.Sleep(time.Second)
	}
	header, err := queryClientHeader(counterparty, height)
	if err != nil {
		return err
	}
	trustedHeader, err := queryClientHeader(counterparty, trustedHeightNum)
	if err != nil {
		return err
	}
	header["@type"] = "/ibc.lightclients.tendermint.v1.Header"
	header["trusted_height"] = trustedHeight
	header["trusted_validators"] = trustedHeader["validator_set"]

	signer, err := relayerAddress(chain)
	if err != nil {
		return err
	}
	msg := map[string]any{
		"@type":          "/ibc.core.client.v1.MsgUpdateClient",
		"client_id":      clientID,
		"client_message": header,
		"signer":         signer,
	}
	hash, err := broadcast(chain, emptyTx(msg, "2000000"))
	if err != nil {
		return err
	}
	return waitTx(chain, hash)
}

func getAckProof(dst ChainConfig, dstClient string, sequence int64) (string, map[string]any, int64, error) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		res, err := queryJSON(
			dst, "ibc", "channelv2", "packet-acknowledgement",
			dstClient, strconv.FormatInt(sequence, 10), "--prove",
		)
		if err == nil {
			proof, _ := res["proof"].(string)
			height, _ := res["proof_height"].(map[string]any)
			if proof != "" && height != nil {
				value, err := asInt64(height["revision_height"])
				if err != nil {
					return "", nil, 0, err
				}
				return proof, height, value, nil
			}
		}
		time.Sleep(time.Second)
	}
	return "", nil, 0, fmt.Errorf("acknowledgement proof for sequence %d not found", sequence)
}

func ackPacket(src, dst ChainConfig, srcClient, dstClient string, sequence, timeout int64, payload map[string]any, ack, proof string, proofHeight map[string]any) (string, error) {
	return ackPacketWithPayloads(src, dst, srcClient, dstClient, sequence, timeout, []map[string]any{payload}, ack, proof, proofHeight)
}

func ackPacketWithPayloads(src, dst ChainConfig, srcClient, dstClient string, sequence, timeout int64, payloads []map[string]any, ack, proof string, proofHeight map[string]any) (string, error) {
	height, err := asInt64(proofHeight["revision_height"])
	if err != nil {
		return "", err
	}
	if err := updateClient(src, dst, srcClient, height); err != nil {
		return "", err
	}
	msg, err := ackMessage(src, srcClient, dstClient, sequence, timeout, payloads, ack, proof, proofHeight)
	if err != nil {
		return "", err
	}
	hash, err := broadcast(src, emptyTx(msg, strconv.FormatInt(relayGas(msg, singleRelayGas), 10)))
	if err != nil {
		return "", err
	}
	if err := waitTx(src, hash); err != nil {
		return "", err
	}
	return hash, nil
}

func ackMessage(src ChainConfig, srcClient, dstClient string, sequence, timeout int64, payloads []map[string]any, ack, proof string, proofHeight map[string]any) (map[string]any, error) {
	signer, err := relayerAddress(src)
	if err != nil {
		return nil, err
	}
	decodedAck, err := decodeAcknowledgement(ack)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"@type":           "/ibc.core.channel.v2.MsgAcknowledgement",
		"packet":          packetMessageWithPayloads(sequence, timeout, srcClient, dstClient, payloads),
		"acknowledgement": decodedAck,
		"proof_acked":     proof,
		"proof_height":    proofHeight,
		"signer":          signer,
	}, nil
}

func packetMessage(sequence, timeout int64, srcClient, dstClient string, payload map[string]any) map[string]any {
	return packetMessageWithPayloads(sequence, timeout, srcClient, dstClient, []map[string]any{payload})
}

func packetMessageWithPayloads(sequence, timeout int64, srcClient, dstClient string, payloads []map[string]any) map[string]any {
	values := make([]any, 0, len(payloads))
	for _, payload := range payloads {
		values = append(values, payload)
	}
	return map[string]any{
		"sequence":           strconv.FormatInt(sequence, 10),
		"source_client":      srcClient,
		"destination_client": dstClient,
		"timeout_timestamp":  strconv.FormatInt(timeout, 10),
		"payloads":           values,
	}
}

func getTimeoutProof(dst ChainConfig, dstClient string, sequence, timeout int64) (string, map[string]any, int64, error) {
	res, err := queryJSON(
		dst, "ibc", "channelv2", "packet-receipt",
		dstClient, strconv.FormatInt(sequence, 10), "--prove",
	)
	if err != nil {
		return "", nil, 0, err
	}
	if received, _ := res["received"].(bool); received {
		return "", nil, 0, errPacketReceived
	}
	proof, _ := res["proof"].(string)
	proofHeight, _ := res["proof_height"].(map[string]any)
	if proof == "" || proofHeight == nil {
		return "", nil, 0, fmt.Errorf("timeout proof query returned incomplete data: %v", res)
	}
	height, err := asInt64(proofHeight["revision_height"])
	if err != nil {
		return "", nil, 0, fmt.Errorf("timeout proof height: %w", err)
	}

	for {
		current, err := latestHeight(dst)
		if err != nil {
			return "", nil, 0, err
		}
		if current >= height {
			break
		}
		time.Sleep(time.Second)
	}
	header, err := blockAt(dst, height)
	if err != nil {
		return "", nil, 0, err
	}
	blockTime, err := time.Parse(time.RFC3339Nano, header.Time)
	if err != nil {
		return "", nil, 0, fmt.Errorf("parse proof block time: %w", err)
	}
	if blockTime.Unix() < timeout {
		return "", nil, 0, fmt.Errorf(
			"%w: proof time %d is before timeout %d",
			errTimeoutNotReached, blockTime.Unix(), timeout,
		)
	}
	return proof, proofHeight, height, nil
}

func ensureClientAtHeight(chain, counterparty ChainConfig, clientID string, height int64) error {
	_, trustedHeight, err := clientLatestHeight(chain, clientID)
	if err != nil {
		return err
	}
	if height <= trustedHeight {
		return nil
	}
	if err := updateClient(chain, counterparty, clientID, height); err != nil {
		return err
	}
	waitNextBlock(chain)
	return nil
}

func timeoutPacket(src, dst ChainConfig, srcClient, dstClient string, sequence, timeout int64, payload map[string]any, proof string, proofHeight map[string]any) (string, error) {
	return timeoutPacketWithPayloads(src, dst, srcClient, dstClient, sequence, timeout, []map[string]any{payload}, proof, proofHeight)
}

func timeoutPacketWithPayloads(src, dst ChainConfig, srcClient, dstClient string, sequence, timeout int64, payloads []map[string]any, proof string, proofHeight map[string]any) (string, error) {
	height, err := asInt64(proofHeight["revision_height"])
	if err != nil {
		return "", err
	}
	if err := ensureClientAtHeight(src, dst, srcClient, height); err != nil {
		return "", err
	}
	msg, err := timeoutMessage(src, srcClient, dstClient, sequence, timeout, payloads, proof, proofHeight)
	if err != nil {
		return "", err
	}
	hash, err := broadcast(src, emptyTx(msg, strconv.FormatInt(relayGas(msg, singleRelayGas), 10)))
	if err != nil {
		return "", err
	}
	if err := waitTx(src, hash); err != nil {
		return "", err
	}
	return hash, nil
}

func timeoutMessage(src ChainConfig, srcClient, dstClient string, sequence, timeout int64, payloads []map[string]any, proof string, proofHeight map[string]any) (map[string]any, error) {
	signer, err := relayerAddress(src)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"@type":            "/ibc.core.channel.v2.MsgTimeout",
		"packet":           packetMessageWithPayloads(sequence, timeout, srcClient, dstClient, payloads),
		"proof_unreceived": proof,
		"proof_height":     proofHeight,
		"signer":           signer,
	}, nil
}

func decodeAcknowledgement(encoded string) (map[string]any, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode acknowledgement: %w", err)
	}
	apps := []any{}
	for len(raw) > 0 {
		key, n := binary.Uvarint(raw)
		if n <= 0 {
			return nil, fmt.Errorf("invalid acknowledgement key")
		}
		raw = raw[n:]
		if key>>3 != 1 || key&7 != 2 {
			return nil, fmt.Errorf("unexpected acknowledgement field: %d", key)
		}
		length, n := binary.Uvarint(raw)
		if n <= 0 || int(length) > len(raw[n:]) {
			return nil, fmt.Errorf("invalid acknowledgement length")
		}
		raw = raw[n:]
		apps = append(apps, base64.StdEncoding.EncodeToString(raw[:length]))
		raw = raw[length:]
	}
	return map[string]any{"app_acknowledgements": apps}, nil
}

func run(cfg Config) error {
	if err := cfg.validateTransfer(); err != nil {
		return err
	}
	log.Printf("creating client on %s for %s", cfg.Src.ChainID, cfg.Dst.ChainID)
	srcClient, _, err := createClient(cfg.Src, cfg.Dst, 0)
	if err != nil {
		return err
	}
	log.Printf("adding counterparty %s to %s", cfg.DstClient, srcClient)
	if err := addCounterparty(cfg.Src, srcClient, cfg.DstClient); err != nil {
		return err
	}

	log.Printf("sending %s %s to %s", cfg.Amount, cfg.Denom, cfg.Receiver)
	sent, err := sendPacket(cfg.Src, srcClient, cfg.Denom, cfg.Amount, cfg.Receiver)
	if err != nil {
		return err
	}
	log.Printf("packet sequence %d; waiting for a block after %d", sent.Sequence, sent.Height)
	for {
		height, err := latestHeight(cfg.Src)
		if err != nil {
			return err
		}
		if height > sent.Height {
			break
		}
		time.Sleep(time.Second)
	}

	p, err := getProof(cfg.Src, srcClient, sent.Sequence)
	if err != nil {
		return err
	}
	log.Printf("proof height: %v", p.Height)
	for {
		height, err := latestHeight(cfg.Src)
		if err != nil {
			return err
		}
		if height >= p.HeightNum {
			break
		}
		time.Sleep(time.Second)
	}

	log.Printf("creating client on %s at source height %d", cfg.Dst.ChainID, p.HeightNum)
	dstClient, _, err := createClient(cfg.Dst, cfg.Src, p.HeightNum)
	if err != nil {
		return err
	}
	if dstClient != cfg.DstClient {
		return fmt.Errorf("destination client id %s does not match dst_client %s; use a fresh config", dstClient, cfg.DstClient)
	}
	log.Printf("adding counterparty %s to %s", srcClient, dstClient)
	if err := addCounterparty(cfg.Dst, dstClient, srcClient); err != nil {
		return err
	}

	log.Printf("relaying MsgRecvPacket")
	hash, recvAck, err := recvPacket(
		cfg.Dst, dstClient, srcClient,
		sent.Sequence, sent.Timeout, sent.Payload, p.Height, p.Commitment,
	)
	if err != nil {
		return err
	}
	log.Printf("recv tx: %s", hash)

	if cfg.relayAck() {
		log.Printf("waiting for acknowledgement proof")
		proof, proofHeight, _, err := getAckProof(cfg.Dst, dstClient, sent.Sequence)
		if err != nil {
			return err
		}
		log.Printf("updating %s on %s for ack at height %v", srcClient, cfg.Src.ChainID, proofHeight)
		ackHash, err := ackPacket(
			cfg.Src, cfg.Dst, srcClient, dstClient,
			sent.Sequence, sent.Timeout, sent.Payload, recvAck, proof, proofHeight,
		)
		if err != nil {
			return err
		}
		log.Printf("ack tx: %s", ackHash)
	}

	return nil
}

func asInt64(v any) (int64, error) {
	switch value := v.(type) {
	case string:
		return strconv.ParseInt(value, 10, 64)
	case float64:
		return int64(value), nil
	default:
		return 0, fmt.Errorf("not an integer: %v", v)
	}
}

func broadcastMessages(chain ChainConfig, messages []map[string]any) (string, error) {
	if len(messages) == 0 {
		return "", fmt.Errorf("no messages to broadcast")
	}
	hash, err := broadcast(chain, emptyBatchTx(messages, strconv.FormatInt(batchGas(messages), 10)))
	if err != nil {
		return "", err
	}
	// Let the account sequence catch up before the next transaction is signed.
	waitNextBlock(chain)
	return hash, nil
}

func waitNextBlock(chain ChainConfig) {
	height, err := latestHeight(chain)
	if err != nil {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current, err := latestHeight(chain)
		if err == nil && current > height {
			return
		}
		time.Sleep(time.Second)
	}
}
