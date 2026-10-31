package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const (
	directionAToB = "a_to_b"
	directionBToA = "b_to_a"
)

type relayOptions struct {
	LinkID   string
	Once     bool
	Interval time.Duration
	Finality int64
	Lookback int64
}

type relayAction int

const (
	relayWait relayAction = iota
	relayRecv
	relayAck
	relayTimeout
	relayDone
)

type relayResult struct {
	Action          relayAction
	Status          string
	TxHash          string
	ProofHeight     int64
	AckProofHeight  int64
	Acknowledgement string
}

func cmdRelay(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("relay", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	linkID := fs.String("link", "", "only relay this link ID")
	once := fs.Bool("once", false, "run one cycle and exit")
	interval := fs.Duration("interval", 5*time.Second, "delay between cycles")
	finality := fs.Int64("finality", 1, "blocks to wait before scanning")
	lookback := fs.Int64("lookback", 1000, "blocks to scan on first run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("relay takes no positional arguments")
	}
	return runRelay(cfg, statePath, relayOptions{
		LinkID:   *linkID,
		Once:     *once,
		Interval: *interval,
		Finality: *finality,
		Lookback: *lookback,
	})
}

func cmdStart(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	interval := fs.Duration("interval", 5*time.Second, "delay between cycles")
	finality := fs.Int64("finality", 1, "blocks to wait before scanning")
	lookback := fs.Int64("lookback", 1000, "blocks to scan on first run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("start takes no positional arguments")
	}
	return runRelay(cfg, statePath, relayOptions{
		Interval: *interval,
		Finality: *finality,
		Lookback: *lookback,
	})
}

func runRelay(cfg Config, statePath string, opts relayOptions) error {
	if opts.Interval <= 0 {
		return fmt.Errorf("interval must be positive")
	}
	if opts.Finality < 0 {
		return fmt.Errorf("finality cannot be negative")
	}
	if opts.Lookback < 1 {
		return fmt.Errorf("lookback must be at least 1")
	}

	store, err := openLinkStore(statePath)
	if err != nil {
		return err
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for {
		if err := relayCycle(store, cfg, opts); err != nil {
			if opts.Once {
				return err
			}
			log.Printf("relay: cycle: %v", err)
		}
		if opts.Once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(opts.Interval):
		}
	}
}

func relayCycle(store *LinkStore, cfg Config, opts relayOptions) error {
	links, err := selectedLinks(store, opts.LinkID)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		log.Printf("relay: no links")
		return nil
	}

	var errs []error
	for _, link := range links {
		if err := processLink(store, cfg, link, opts); err != nil {
			log.Printf("relay: link %s: %v", link.ID, err)
			errs = append(errs, fmt.Errorf("link %s: %w", link.ID, err))
		}
	}
	return errors.Join(errs...)
}

func selectedLinks(store *LinkStore, linkID string) ([]Link, error) {
	if linkID == "" {
		return store.List()
	}
	link, err := store.Get(linkID)
	if err != nil {
		return nil, err
	}
	return []Link{link}, nil
}

func processLink(store *LinkStore, cfg Config, link Link, opts relayOptions) error {
	chainA, err := cfg.chainByID(link.ChainA)
	if err != nil {
		return err
	}
	chainB, err := cfg.chainByID(link.ChainB)
	if err != nil {
		return err
	}

	var errs []error
	if err := scanLinkSide(store, link, chainA, chainB, link.ClientA, link.ClientB, directionAToB, opts); err != nil {
		errs = append(errs, fmt.Errorf("scan %s: %w", chainA.Config.ChainID, err))
	}
	if err := scanLinkSide(store, link, chainB, chainA, link.ClientB, link.ClientA, directionBToA, opts); err != nil {
		errs = append(errs, fmt.Errorf("scan %s: %w", chainB.Config.ChainID, err))
	}
	if err := processLinkPackets(store, cfg, link); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func scanLinkSide(store *LinkStore, link Link, chain, counterparty namedChain, sourceClient, destinationClient, direction string, opts relayOptions) error {
	latest, err := latestHeight(chain.Config)
	if err != nil {
		return err
	}
	end := latest - opts.Finality
	if end < 1 {
		return nil
	}

	last, ok, err := store.ScanHeight(link.ID, chain.Config.ChainID)
	if err != nil {
		return err
	}
	start := int64(1)
	if ok {
		start = last + 1
	} else if latest > opts.Lookback {
		start = latest - opts.Lookback
	}
	if start > end {
		return nil
	}

	for height := start; height <= end; height++ {
		events, err := blockEvents(chain.Config, height)
		if err != nil {
			return fmt.Errorf("block %d: %w", height, err)
		}
		for _, event := range events {
			if event.Type == "write_acknowledgement" {
				if event.Attributes["packet_dest_client"] != sourceClient ||
					event.Attributes["packet_source_client"] != destinationClient {
					continue
				}
				sequence, err := strconv.ParseInt(event.Attributes["packet_sequence"], 10, 64)
				if err != nil {
					continue
				}
				ackHex := event.Attributes["encoded_acknowledgement_hex"]
				if ackHex == "" {
					continue
				}
				ack, err := hex.DecodeString(ackHex)
				if err != nil {
					return fmt.Errorf("decode acknowledgement at %s block %d: %w", chain.Config.ChainID, height, err)
				}
				if err := store.SetPacketAcknowledgement(
					link.ID, oppositeDirection(direction), sequence,
					base64.StdEncoding.EncodeToString(ack),
				); err != nil {
					return err
				}
				continue
			}
			if event.Type != "send_packet" {
				continue
			}
			if event.Attributes["packet_source_client"] != sourceClient ||
				event.Attributes["packet_dest_client"] != destinationClient {
				continue
			}
			encoded := event.Attributes["encoded_packet_hex"]
			if encoded == "" {
				log.Printf("relay: %s block %d: send_packet without encoded packet", chain.Config.ChainID, height)
				continue
			}
			packet, err := decodePacketHex(encoded)
			if err != nil {
				return fmt.Errorf("decode packet at %s block %d: %w", chain.Config.ChainID, height, err)
			}
			if packet.SourceClient != sourceClient || packet.DestinationClient != destinationClient {
				continue
			}
			record := PacketRecord{
				LinkID:            link.ID,
				Direction:         direction,
				Sequence:          int64(packet.Sequence),
				SourceClient:      packet.SourceClient,
				DestinationClient: packet.DestinationClient,
				Payloads:          packet.Payloads,
				TimeoutTimestamp:  int64(packet.TimeoutTimestamp),
				Status:            PacketPending,
				CreatedAt:         time.Now().UTC(),
			}
			inserted, err := store.InsertPacket(record)
			if err != nil {
				return fmt.Errorf("store packet %d: %w", record.Sequence, err)
			}
			if inserted {
				log.Printf("relay: %s block %d: packet %s/%d pending", link.ID, height, direction, record.Sequence)
			}
		}
		if err := store.SetScanHeight(link.ID, chain.Config.ChainID, height); err != nil {
			return err
		}
	}
	return nil
}

func blockEvents(chain ChainConfig, height int64) ([]blockEvent, error) {
	res, err := rpcGet(chain, fmt.Sprintf("/block_results?height=%d", height))
	if err != nil {
		return nil, err
	}
	result, _ := res["result"].(map[string]any)
	if result == nil {
		return nil, fmt.Errorf("block_results returned no result")
	}
	txs, _ := result["txs_results"].([]any)
	events := []blockEvent{}
	for _, rawTx := range txs {
		tx, _ := rawTx.(map[string]any)
		rawEvents, _ := tx["events"].([]any)
		for _, rawEvent := range rawEvents {
			event, _ := rawEvent.(map[string]any)
			attributes := make(map[string]string)
			rawAttributes, _ := event["attributes"].([]any)
			for _, rawAttribute := range rawAttributes {
				attribute, _ := rawAttribute.(map[string]any)
				key, encoded := decodeAttributeText(str(attribute["key"]))
				value := str(attribute["value"])
				if encoded {
					value, _ = decodeAttributeText(value)
				}
				attributes[key] = value
			}
			events = append(events, blockEvent{
				Type:       str(event["type"]),
				Attributes: attributes,
			})
		}
	}
	return events, nil
}

type blockEvent struct {
	Type       string
	Attributes map[string]string
}

// Some CometBFT versions base64-encode block_results attributes and some
// return them as plain strings. The key tells us which mode this response uses.
func decodeAttributeText(value string) (string, bool) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return value, false
	}
	return string(decoded), true
}

func processLinkPackets(store *LinkStore, cfg Config, link Link) error {
	packets, err := store.Packets(link.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, packet := range packets {
		if packet.Status == PacketAcked || packet.Status == PacketTimedOut {
			continue
		}
		if !packet.NextAttemptAt.IsZero() && packet.NextAttemptAt.After(now) {
			continue
		}
		result, err := processPacket(cfg, link, packet)
		if err != nil {
			packet.Attempts++
			packet.LastError = err.Error()
			packet.NextAttemptAt = now.Add(relayBackoff(packet.Attempts))
			if updateErr := store.UpdatePacket(packet); updateErr != nil {
				return updateErr
			}
			log.Printf("relay: %s packet %s/%d: %v", link.ID, packet.Direction, packet.Sequence, err)
			continue
		}

		switch result.Action {
		case relayRecv:
			packet.Status = PacketRecvSent
			packet.RecvTxHash = result.TxHash
			packet.Acknowledgement = result.Acknowledgement
			packet.ProofHeight = result.ProofHeight
			packet.Attempts = 0
			packet.LastError = ""
			packet.NextAttemptAt = time.Time{}
			log.Printf("relay: %s packet %s/%d: recv tx %s", link.ID, packet.Direction, packet.Sequence, result.TxHash)
		case relayAck:
			packet.Status = PacketAcked
			packet.AckTxHash = result.TxHash
			packet.Acknowledgement = result.Acknowledgement
			packet.AckProofHeight = result.AckProofHeight
			packet.Attempts = 0
			packet.LastError = ""
			packet.NextAttemptAt = time.Time{}
			log.Printf("relay: %s packet %s/%d: ack tx %s", link.ID, packet.Direction, packet.Sequence, result.TxHash)
		case relayTimeout:
			packet.Status = PacketTimedOut
			packet.TimeoutTxHash = result.TxHash
			packet.ProofHeight = result.ProofHeight
			packet.Attempts = 0
			packet.LastError = ""
			packet.NextAttemptAt = time.Time{}
			log.Printf("relay: %s packet %s/%d: timeout tx %s", link.ID, packet.Direction, packet.Sequence, result.TxHash)
		case relayDone:
			packet.Status = result.Status
			packet.Attempts = 0
			packet.LastError = ""
			packet.NextAttemptAt = time.Time{}
			log.Printf("relay: %s packet %s/%d: %s", link.ID, packet.Direction, packet.Sequence, result.Status)
		case relayWait:
			if packet.Status == PacketPending {
				packet.Status = PacketRecvSent
			}
			packet.Attempts = 0
			packet.LastError = ""
			packet.NextAttemptAt = now.Add(5 * time.Second)
		}
		if err := store.UpdatePacket(packet); err != nil {
			return err
		}
	}
	return nil
}

func processPacket(cfg Config, link Link, packet PacketRecord) (relayResult, error) {
	src, dst, srcClient, dstClient, err := packetEndpoints(cfg, link, packet.Direction)
	if err != nil {
		return relayResult{}, err
	}
	if len(packet.Payloads) == 0 {
		return relayResult{}, fmt.Errorf("packet has no payloads")
	}
	payloads := packetPayloadMessages(packet.Payloads)

	commitment, err := packetCommitment(src.Config, srcClient, packet.Sequence)
	if err != nil {
		return relayResult{}, err
	}
	_, ackExists, err := packetAcknowledgement(dst.Config, dstClient, packet.Sequence)
	if err != nil {
		return relayResult{}, err
	}

	if commitment == "" {
		if ackExists {
			return relayResult{Action: relayDone, Status: PacketAcked}, nil
		}
		return relayResult{Action: relayDone, Status: PacketTimedOut}, nil
	}

	if ackExists {
		ack := packet.Acknowledgement
		if ack == "" && packet.RecvTxHash != "" {
			ack, err = recvAcknowledgement(dst.Config, packet.RecvTxHash)
			if err != nil {
				return relayResult{}, fmt.Errorf("read acknowledgement from recv tx: %w", err)
			}
		}
		if ack == "" {
			return relayResult{}, fmt.Errorf("acknowledgement bytes not captured yet")
		}
		proof, proofHeight, height, err := getAckProof(dst.Config, dstClient, packet.Sequence)
		if err != nil {
			return relayResult{}, fmt.Errorf("ack proof: %w", err)
		}
		hash, err := ackPacketWithPayloads(
			src.Config, dst.Config, srcClient, dstClient,
			packet.Sequence, packet.TimeoutTimestamp, payloads, ack, proof, proofHeight,
		)
		if err != nil {
			return relayResult{}, err
		}
		return relayResult{
			Action: relayAck, Status: PacketAcked, TxHash: hash,
			AckProofHeight: height, Acknowledgement: ack,
		}, nil
	}

	received, err := packetReceipt(dst.Config, dstClient, packet.Sequence)
	if err != nil {
		return relayResult{}, err
	}
	if received {
		return relayResult{Action: relayWait, Status: PacketRecvSent}, nil
	}

	timeoutProof, timeoutProofHeight, timeoutHeight, err := getTimeoutProof(
		dst.Config, dstClient, packet.Sequence, packet.TimeoutTimestamp,
	)
	if err == nil {
		hash, err := timeoutPacketWithPayloads(
			src.Config, dst.Config, srcClient, dstClient,
			packet.Sequence, packet.TimeoutTimestamp, payloads, timeoutProof, timeoutProofHeight,
		)
		if err != nil {
			return relayResult{}, err
		}
		return relayResult{Action: relayTimeout, Status: PacketTimedOut, TxHash: hash, ProofHeight: timeoutHeight}, nil
	}
	if errors.Is(err, errPacketReceived) {
		return relayResult{Action: relayWait, Status: PacketRecvSent}, nil
	}
	if !errors.Is(err, errTimeoutNotReached) {
		return relayResult{}, err
	}

	commitmentProof, err := getProof(src.Config, srcClient, packet.Sequence)
	if err != nil {
		return relayResult{}, err
	}
	if err := ensureClientAtHeight(dst.Config, src.Config, dstClient, commitmentProof.HeightNum); err != nil {
		return relayResult{}, err
	}
	hash, recvAck, err := recvPacketWithPayloads(
		dst.Config, dstClient, srcClient,
		packet.Sequence, packet.TimeoutTimestamp, payloads, commitmentProof.Height, commitmentProof.Commitment,
	)
	if err != nil {
		return relayResult{}, err
	}
	return relayResult{
		Action: relayRecv, Status: PacketRecvSent, TxHash: hash,
		ProofHeight: commitmentProof.HeightNum, Acknowledgement: recvAck,
	}, nil
}

func oppositeDirection(direction string) string {
	if direction == directionAToB {
		return directionBToA
	}
	return directionAToB
}

func packetEndpoints(cfg Config, link Link, direction string) (namedChain, namedChain, string, string, error) {
	chainA, err := cfg.chainByID(link.ChainA)
	if err != nil {
		return namedChain{}, namedChain{}, "", "", err
	}
	chainB, err := cfg.chainByID(link.ChainB)
	if err != nil {
		return namedChain{}, namedChain{}, "", "", err
	}
	switch direction {
	case directionAToB:
		return chainA, chainB, link.ClientA, link.ClientB, nil
	case directionBToA:
		return chainB, chainA, link.ClientB, link.ClientA, nil
	default:
		return namedChain{}, namedChain{}, "", "", fmt.Errorf("unknown packet direction %q", direction)
	}
}

func packetCommitment(chain ChainConfig, clientID string, sequence int64) (string, error) {
	res, err := queryJSON(
		chain, "ibc", "channelv2", "packet-commitment",
		clientID, strconv.FormatInt(sequence, 10), "--prove=false",
	)
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return str(res["commitment"]), nil
}

func packetAcknowledgement(chain ChainConfig, clientID string, sequence int64) (string, bool, error) {
	res, err := queryJSON(
		chain, "ibc", "channelv2", "packet-acknowledgement",
		clientID, strconv.FormatInt(sequence, 10), "--prove=false",
	)
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	ack := str(res["acknowledgement"])
	return ack, ack != "", nil
}

func packetReceipt(chain ChainConfig, clientID string, sequence int64) (bool, error) {
	res, err := queryJSON(
		chain, "ibc", "channelv2", "packet-receipt",
		clientID, strconv.FormatInt(sequence, 10), "--prove=false",
	)
	if err != nil {
		return false, err
	}
	received, _ := res["received"].(bool)
	return received, nil
}

func relayBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 5 {
		attempts = 5
	}
	return time.Duration(1<<(attempts-1)) * 5 * time.Second
}
