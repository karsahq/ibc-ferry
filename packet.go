package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
)

// packetPayload mirrors ibc.core.channel.v2.Payload. Value stays base64
// encoded because that is what the JSON tx messages expect.
type packetPayload struct {
	SourcePort      string `json:"source_port"`
	DestinationPort string `json:"destination_port"`
	Version         string `json:"version"`
	Encoding        string `json:"encoding"`
	Value           string `json:"value"`
}

func (p packetPayload) message() map[string]any {
	return map[string]any{
		"source_port":      p.SourcePort,
		"destination_port": p.DestinationPort,
		"version":          p.Version,
		"encoding":         p.Encoding,
		"value":            p.Value,
	}
}

type packetData struct {
	Sequence          uint64
	SourceClient      string
	DestinationClient string
	TimeoutTimestamp  uint64
	Payloads          []packetPayload
}

func decodePacketHex(encoded string) (packetData, error) {
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return packetData{}, fmt.Errorf("decode packet hex: %w", err)
	}
	packet, err := decodePacket(raw)
	if err != nil {
		return packetData{}, err
	}
	return packet, nil
}

func decodePacket(raw []byte) (packetData, error) {
	var packet packetData
	err := walkProto(raw, func(field int, wire int, value []byte, number uint64) error {
		switch {
		case field == 1 && wire == 0:
			packet.Sequence = number
		case field == 2 && wire == 2:
			packet.SourceClient = string(value)
		case field == 3 && wire == 2:
			packet.DestinationClient = string(value)
		case field == 4 && wire == 0:
			packet.TimeoutTimestamp = number
		case field == 5 && wire == 2:
			payload, err := decodePayload(value)
			if err != nil {
				return err
			}
			packet.Payloads = append(packet.Payloads, payload)
		}
		return nil
	})
	if err != nil {
		return packetData{}, err
	}
	return packet, nil
}

func decodePayload(raw []byte) (packetPayload, error) {
	var payload packetPayload
	var value []byte
	err := walkProto(raw, func(field int, wire int, data []byte, _ uint64) error {
		if wire != 2 {
			return nil
		}
		switch field {
		case 1:
			payload.SourcePort = string(data)
		case 2:
			payload.DestinationPort = string(data)
		case 3:
			payload.Version = string(data)
		case 4:
			payload.Encoding = string(data)
		case 5:
			value = append([]byte(nil), data...)
		}
		return nil
	})
	if err != nil {
		return packetPayload{}, err
	}
	payload.Value = base64.StdEncoding.EncodeToString(value)
	return payload, nil
}

// walkProto is a minimal protobuf reader for the packet messages. Unknown
// fields are skipped.
func walkProto(raw []byte, visit func(field int, wire int, value []byte, number uint64) error) error {
	for offset := 0; offset < len(raw); {
		tag, n := binary.Uvarint(raw[offset:])
		if n <= 0 {
			return fmt.Errorf("invalid protobuf tag")
		}
		offset += n
		field := int(tag >> 3)
		wire := int(tag & 7)
		switch wire {
		case 0:
			number, n := binary.Uvarint(raw[offset:])
			if n <= 0 {
				return fmt.Errorf("invalid protobuf varint")
			}
			offset += n
			if err := visit(field, wire, nil, number); err != nil {
				return err
			}
		case 1:
			if offset+8 > len(raw) {
				return fmt.Errorf("truncated protobuf fixed64")
			}
			offset += 8
		case 2:
			length, n := binary.Uvarint(raw[offset:])
			if n <= 0 || int(length) > len(raw[offset+n:]) {
				return fmt.Errorf("invalid protobuf length")
			}
			offset += n
			value := raw[offset : offset+int(length)]
			offset += int(length)
			if err := visit(field, wire, value, 0); err != nil {
				return err
			}
		case 5:
			if offset+4 > len(raw) {
				return fmt.Errorf("truncated protobuf fixed32")
			}
			offset += 4
		default:
			return fmt.Errorf("unsupported protobuf wire type %d", wire)
		}
	}
	return nil
}

func packetPayloadMessages(payloads []packetPayload) []map[string]any {
	messages := make([]map[string]any, 0, len(payloads))
	for _, payload := range payloads {
		messages = append(messages, payload.message())
	}
	return messages
}

func sequenceString(sequence int64) string {
	return strconv.FormatInt(sequence, 10)
}
