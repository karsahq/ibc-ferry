package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// recoverAcknowledgement finds the original acknowledgement bytes for a packet
// from the destination chain's transaction index. The chain stores only the ack
// hash, but the write_acknowledgement event contains the full payload.
func recoverAcknowledgement(chain ChainConfig, clientID string, sequence int64) (string, error) {
	query := fmt.Sprintf(
		"write_acknowledgement.packet_dest_client='%s' AND write_acknowledgement.packet_sequence='%d'",
		clientID, sequence,
	)
	encodedQuery, err := json.Marshal(query)
	if err != nil {
		return "", err
	}
	res, err := rpcGet(chain, fmt.Sprintf(
		"/tx_search?query=%s&prove=false&page=1&per_page=20",
		url.QueryEscape(string(encodedQuery)),
	))
	if err != nil {
		return "", err
	}
	return parseWriteAckSearch(res, clientID, sequence)
}

func parseWriteAckSearch(res map[string]any, clientID string, sequence int64) (string, error) {
	result, _ := res["result"].(map[string]any)
	txs, _ := result["txs"].([]any)
	for _, rawTx := range txs {
		tx, _ := rawTx.(map[string]any)
		txResult, _ := tx["tx_result"].(map[string]any)
		events, _ := txResult["events"].([]any)
		for _, rawEvent := range events {
			event, _ := rawEvent.(map[string]any)
			if str(event["type"]) != "write_acknowledgement" {
				continue
			}
			attributes, _ := event["attributes"].([]any)
			attrs := attributeMap(attributes)
			if attrs["packet_sequence"] != strconv.FormatInt(sequence, 10) {
				continue
			}
			if attrs["packet_dest_client"] != "" && attrs["packet_dest_client"] != clientID {
				continue
			}
			encoded := attrs["encoded_acknowledgement_hex"]
			if encoded == "" {
				continue
			}
			ack, err := hex.DecodeString(encoded)
			if err != nil {
				return "", fmt.Errorf("decode recovered acknowledgement: %w", err)
			}
			return base64.StdEncoding.EncodeToString(ack), nil
		}
	}
	return "", fmt.Errorf(
		"write_acknowledgement for client %s sequence %d not found in tx index",
		clientID, sequence,
	)
}
