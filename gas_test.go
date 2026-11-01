package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func relayMessage(messageType, memo string) map[string]any {
	packetData := map[string]any{
		"denom": "akra", "amount": "1000",
		"sender": "karsa1sender", "receiver": "karsa1receiver", "memo": memo,
	}
	raw, _ := json.Marshal(packetData)
	payload := map[string]any{
		"source_port": "transfer", "destination_port": "transfer",
		"version": "ics20-1", "encoding": "application/json",
		"value": base64.StdEncoding.EncodeToString(raw),
	}
	return map[string]any{
		"@type": messageType,
		"packet": map[string]any{
			"sequence": "1",
			"payloads": []any{payload},
		},
	}
}

func TestRelayGas(t *testing.T) {
	cases := []struct {
		name    string
		message map[string]any
		want    int64
	}{
		{
			name:    "recv without a memo",
			message: relayMessage("/ibc.core.channel.v2.MsgRecvPacket", ""),
			want:    singleRelayGas,
		},
		{
			name: "recv with destination callback",
			message: relayMessage("/ibc.core.channel.v2.MsgRecvPacket",
				`{"dest_callback":{"address":"0xabc","gas_limit":"1000000"}}`),
			want: singleRelayGas + 1_000_000,
		},
		{
			name: "recv with a smaller destination callback",
			message: relayMessage("/ibc.core.channel.v2.MsgRecvPacket",
				`{"dest_callback":{"address":"0xabc","gas_limit":"250000"}}`),
			want: singleRelayGas + 250_000,
		},
		{
			name: "recv caps the reservation at the chain callback cap",
			message: relayMessage("/ibc.core.channel.v2.MsgRecvPacket",
				`{"dest_callback":{"address":"0xabc","gas_limit":"9000000"}}`),
			want: singleRelayGas + maxCallbackGas,
		},
		{
			name: "recv ignores a source callback",
			message: relayMessage("/ibc.core.channel.v2.MsgRecvPacket",
				`{"src_callback":{"address":"0xabc","gas_limit":"500000"}}`),
			want: singleRelayGas,
		},
		{
			name: "acknowledgement reserves the source callback",
			message: relayMessage("/ibc.core.channel.v2.MsgAcknowledgement",
				`{"src_callback":{"address":"0xabc","gas_limit":"400000"}}`),
			want: singleRelayGas + 400_000,
		},
		{
			name: "timeout reserves the source callback",
			message: relayMessage("/ibc.core.channel.v2.MsgTimeout",
				`{"src_callback":{"address":"0xabc","gas_limit":"100000"}}`),
			want: singleRelayGas + 100_000,
		},
		{
			name: "acknowledgement ignores a destination callback",
			message: relayMessage("/ibc.core.channel.v2.MsgAcknowledgement",
				`{"dest_callback":{"address":"0xabc","gas_limit":"400000"}}`),
			want: singleRelayGas,
		},
		{
			name:    "recv with a memo that is not JSON",
			message: relayMessage("/ibc.core.channel.v2.MsgRecvPacket", "hello"),
			want:    singleRelayGas,
		},
		{
			name:    "recv with a callback entry that has no gas limit",
			message: relayMessage("/ibc.core.channel.v2.MsgRecvPacket", `{"dest_callback":{"address":"0xabc"}}`),
			want:    singleRelayGas,
		},
		{
			name:    "recv with a numeric gas limit",
			message: relayMessage("/ibc.core.channel.v2.MsgRecvPacket", `{"dest_callback":{"gas_limit":250000}}`),
			want:    singleRelayGas + 250_000,
		},
		{
			name:    "send packet cannot trigger a callback",
			message: relayMessage("/ibc.core.channel.v2.MsgSendPacket", `{"dest_callback":{"gas_limit":"1000000"}}`),
			want:    singleRelayGas,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := relayGas(test.message, singleRelayGas)
			if got != test.want {
				t.Fatalf("relayGas = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRelayGasWithoutPayloads(t *testing.T) {
	message := map[string]any{"@type": "/ibc.core.channel.v2.MsgRecvPacket"}
	if got := relayGas(message, singleRelayGas); got != singleRelayGas {
		t.Fatalf("relayGas = %d, want %d", got, singleRelayGas)
	}

	broken := map[string]any{
		"@type": "/ibc.core.channel.v2.MsgRecvPacket",
		"packet": map[string]any{
			"payloads": []any{map[string]any{"value": "not-base64"}},
		},
	}
	if got := relayGas(broken, singleRelayGas); got != singleRelayGas {
		t.Fatalf("relayGas for a broken payload = %d, want %d", got, singleRelayGas)
	}
}

func TestBatchGas(t *testing.T) {
	plain := relayMessage("/ibc.core.channel.v2.MsgRecvPacket", "")
	withCallback := relayMessage("/ibc.core.channel.v2.MsgRecvPacket",
		`{"dest_callback":{"address":"0xabc","gas_limit":"600000"}}`)

	cases := []struct {
		name     string
		messages []map[string]any
		want     int64
	}{
		{
			name:     "one message uses the single-message cost",
			messages: []map[string]any{plain},
			want:     singleRelayGas,
		},
		{
			name:     "one message with a callback adds only the callback gas",
			messages: []map[string]any{withCallback},
			want:     singleRelayGas + 600_000,
		},
		{
			name:     "three messages use the batch cost",
			messages: []map[string]any{plain, plain, plain},
			want:     int64(batchOverheadGas) + 3*batchMessageGas,
		},
		{
			name:     "a batch adds the callback gas of each message",
			messages: []map[string]any{plain, withCallback},
			want:     int64(batchOverheadGas) + 2*batchMessageGas + 600_000,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := batchGas(test.messages); got != test.want {
				t.Fatalf("batchGas = %d, want %d", got, test.want)
			}
		})
	}
}
