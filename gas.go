package main

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
)

const (
	// singleRelayGas covers one recv, acknowledgement, or timeout message.
	singleRelayGas = 700_000
	// batchMessageGas covers one message that shares a transaction with others.
	batchMessageGas = 800_000
	// batchOverheadGas covers the fixed cost of a batched transaction.
	batchOverheadGas = 300_000
	// maxCallbackGas mirrors the per-packet EVM callback cap that IBC callback
	// stacks apply. Reserving more than the chain allows would only cost fee.
	maxCallbackGas = 1_000_000
)

// relayGas returns the gas limit one relay message needs. An IBC callback stack
// runs the requested EVM callback on the transaction gas meter, so a packet
// whose memo asks for a destination or source callback needs that gas on top of
// the base relay cost. Without the reservation the callback either fails or
// runs with what is left of the transaction gas.
func relayGas(message map[string]any, base int64) int64 {
	return base + callbackGas(message)
}

// batchGas returns the gas limit for a transaction that carries messages. A
// group of one is not a batch: it uses the single-message cost, which is lower
// because the transaction carries no batching overhead.
func batchGas(messages []map[string]any) int64 {
	if len(messages) == 1 {
		return relayGas(messages[0], singleRelayGas)
	}
	total := int64(batchOverheadGas)
	for _, message := range messages {
		total += relayGas(message, batchMessageGas)
	}
	return total
}

// callbackKey returns the memo key that makes the chain run an EVM callback for
// this message, or an empty string when the message cannot trigger one.
func callbackKey(message map[string]any) string {
	switch str(message["@type"]) {
	case "/ibc.core.channel.v2.MsgRecvPacket":
		return "dest_callback"
	case "/ibc.core.channel.v2.MsgAcknowledgement", "/ibc.core.channel.v2.MsgTimeout":
		return "src_callback"
	}
	return ""
}

// callbackGas sums the callback gas the packet memo asks for. Unknown fields,
// unparsable memos, and non-callback packets contribute nothing.
func callbackGas(message map[string]any) int64 {
	key := callbackKey(message)
	if key == "" {
		return 0
	}
	var total int64
	for _, payload := range messagePayloads(message) {
		total += payloadCallbackGas(payload, key)
	}
	return total
}

func messagePayloads(message map[string]any) []map[string]any {
	packet, ok := message["packet"].(map[string]any)
	if !ok {
		return nil
	}
	switch payloads := packet["payloads"].(type) {
	case []any:
		result := make([]map[string]any, 0, len(payloads))
		for _, item := range payloads {
			if payload, ok := item.(map[string]any); ok {
				result = append(result, payload)
			}
		}
		return result
	case []map[string]any:
		return payloads
	}
	return nil
}

func payloadCallbackGas(payload map[string]any, key string) int64 {
	encoded := str(payload["value"])
	if encoded == "" {
		return 0
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return 0
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0
	}
	memo := str(data["memo"])
	if memo == "" {
		return 0
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(memo), &fields); err != nil {
		return 0
	}
	callback, ok := fields[key].(map[string]any)
	if !ok {
		return 0
	}
	limit := gasLimitValue(callback["gas_limit"])
	if limit <= 0 {
		return 0
	}
	if limit > maxCallbackGas {
		return maxCallbackGas
	}
	return limit
}

// gasLimitValue reads a callback gas limit, which chains encode as a string but
// some callers write as a number.
func gasLimitValue(value any) int64 {
	switch raw := value.(type) {
	case string:
		limit, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0
		}
		return limit
	case float64:
		return int64(raw)
	}
	return 0
}
