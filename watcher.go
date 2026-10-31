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
	"sort"
	"strconv"
	"syscall"
	"time"
)

const (
	directionAToB = "a_to_b"
	directionBToA = "b_to_a"
)

type relayOptions struct {
	LinkID        string
	Once          bool
	Interval      time.Duration
	Finality      int64
	Lookback      int64
	HTTPAddr      string
	Retention     time.Duration
	ClientRefresh time.Duration
	BatchSize     int
}

type relayAction int

const (
	relayWait relayAction = iota
	relayRecv
	relayAck
	relayTimeout
	relayDone
)

type preparedPacket struct {
	Packet          PacketRecord
	Action          relayAction
	Status          string
	Message         map[string]any
	BroadcastChain  namedChain
	ClientID        string
	Counterparty    namedChain
	ProofHeight     int64
	AckProofHeight  int64
	Acknowledgement string
}

type batchKey struct {
	ChainID     string
	ClientID    string
	Action      relayAction
	ProofHeight int64
}

func cmdRelay(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("relay", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	linkID := fs.String("link", "", "only relay this link ID")
	once := fs.Bool("once", false, "run one cycle and exit")
	httpAddr := fs.String("http-addr", "", "optional status/metrics listen address")
	retention := fs.Duration("retention", 0, "prune terminal packets older than this; 0 disables")
	clientRefresh := fs.Duration("client-refresh", 0, "refresh idle clients at this interval; 0 disables")
	batchSize := fs.Int("batch-size", 10, "maximum messages per relay transaction")
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
		LinkID:        *linkID,
		Once:          *once,
		Interval:      *interval,
		Finality:      *finality,
		Lookback:      *lookback,
		HTTPAddr:      *httpAddr,
		Retention:     *retention,
		ClientRefresh: *clientRefresh,
		BatchSize:     *batchSize,
	})
}

func cmdStart(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	interval := fs.Duration("interval", 5*time.Second, "delay between cycles")
	finality := fs.Int64("finality", 1, "blocks to wait before scanning")
	lookback := fs.Int64("lookback", 1000, "blocks to scan on first run")
	httpAddr := fs.String("http-addr", "127.0.0.1:8080", "status/metrics listen address")
	retention := fs.Duration("retention", 7*24*time.Hour, "prune terminal packets older than this; 0 disables")
	clientRefresh := fs.Duration("client-refresh", time.Hour, "refresh idle clients at this interval; 0 disables")
	batchSize := fs.Int("batch-size", 10, "maximum messages per relay transaction")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("start takes no positional arguments")
	}
	return runRelay(cfg, statePath, relayOptions{
		Interval:      *interval,
		Finality:      *finality,
		Lookback:      *lookback,
		HTTPAddr:      *httpAddr,
		Retention:     *retention,
		ClientRefresh: *clientRefresh,
		BatchSize:     *batchSize,
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
	if opts.BatchSize < 1 {
		return fmt.Errorf("batch-size must be at least 1")
	}

	store, err := openLinkStore(statePath)
	if err != nil {
		return err
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stats := newRelayStats()
	refreshed := make(map[string]time.Time)
	if opts.HTTPAddr != "" {
		if err := serveStatus(ctx, opts.HTTPAddr, store, stats, opts.Interval); err != nil {
			return err
		}
	}

	for {
		err := relayCycle(store, cfg, opts, stats, refreshed)
		stats.recordCycle(err)
		if pruneErr := pruneTerminal(store, stats, opts.Retention); pruneErr != nil {
			log.Printf("relay: prune: %v", pruneErr)
		}
		if err != nil {
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

func relayCycle(store *LinkStore, cfg Config, opts relayOptions, stats *relayStats, refreshed map[string]time.Time) error {
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
		if err := processLink(store, cfg, link, opts, stats, refreshed); err != nil {
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

func processLink(store *LinkStore, cfg Config, link Link, opts relayOptions, stats *relayStats, refreshed map[string]time.Time) error {
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
	if err := processLinkPackets(store, cfg, link, stats, opts); err != nil {
		errs = append(errs, err)
	}
	if err := refreshLinkClients(chainA, chainB, link, opts, stats, refreshed); err != nil {
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

func processLinkPackets(store *LinkStore, cfg Config, link Link, stats *relayStats, opts relayOptions) error {
	packets, err := store.Packets(link.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	proofHeights := make(map[string]int64)
	var updated []PacketRecord
	var actionable []preparedPacket

	for _, packet := range packets {
		if packet.Status == PacketAcked || packet.Status == PacketTimedOut {
			continue
		}
		if !packet.NextAttemptAt.IsZero() && packet.NextAttemptAt.After(now) {
			continue
		}
		prepared, err := preparePacket(cfg, link, packet, opts.Finality, proofHeights)
		if err != nil {
			packet.Attempts++
			packet.LastError = err.Error()
			packet.NextAttemptAt = now.Add(relayBackoff(packet.Attempts))
			updated = append(updated, packet)
			log.Printf("relay: %s packet %s/%d: %v", link.ID, packet.Direction, packet.Sequence, err)
			continue
		}
		switch prepared.Action {
		case relayDone:
			packet.Status = prepared.Status
			packet.Attempts = 0
			packet.LastError = ""
			packet.NextAttemptAt = time.Time{}
			stats.addAction(prepared.Status)
			log.Printf("relay: %s packet %s/%d: %s", link.ID, packet.Direction, packet.Sequence, prepared.Status)
			updated = append(updated, packet)
		case relayWait:
			if packet.Status == PacketPending {
				packet.Status = PacketRecvSent
			}
			packet.Attempts = 0
			packet.LastError = ""
			packet.NextAttemptAt = now.Add(5 * time.Second)
			updated = append(updated, packet)
		default:
			actionable = append(actionable, prepared)
		}
	}
	if len(updated) > 0 {
		if err := store.UpdatePackets(updated); err != nil {
			return err
		}
	}

	for _, group := range groupPrepared(actionable, opts.BatchSize) {
		if err := processPreparedGroup(store, group, stats); err != nil {
			return err
		}
	}
	return nil
}

func preparePacket(cfg Config, link Link, packet PacketRecord, finality int64, proofHeights map[string]int64) (preparedPacket, error) {
	src, dst, srcClient, dstClient, err := packetEndpoints(cfg, link, packet.Direction)
	if err != nil {
		return preparedPacket{}, err
	}
	if len(packet.Payloads) == 0 {
		return preparedPacket{}, fmt.Errorf("packet has no payloads")
	}
	payloads := packetPayloadMessages(packet.Payloads)

	commitment, err := packetCommitment(src.Config, srcClient, packet.Sequence)
	if err != nil {
		return preparedPacket{}, err
	}
	_, ackExists, err := packetAcknowledgement(dst.Config, dstClient, packet.Sequence)
	if err != nil {
		return preparedPacket{}, err
	}

	if commitment == "" {
		if ackExists {
			return preparedPacket{Action: relayDone, Status: PacketAcked}, nil
		}
		return preparedPacket{Action: relayDone, Status: PacketTimedOut}, nil
	}

	if ackExists {
		ack := packet.Acknowledgement
		if ack == "" && packet.RecvTxHash != "" {
			ack, err = recvAcknowledgementForSequence(dst.Config, packet.RecvTxHash, packet.Sequence)
			if err != nil {
				return preparedPacket{}, fmt.Errorf("read acknowledgement from recv tx: %w", err)
			}
		}
		if ack == "" {
			return preparedPacket{}, fmt.Errorf("acknowledgement bytes not captured yet")
		}
		proof, proofHeight, height, err := getAckProof(dst.Config, dstClient, packet.Sequence)
		if err != nil {
			return preparedPacket{}, fmt.Errorf("ack proof: %w", err)
		}
		message, err := ackMessage(
			src.Config, srcClient, dstClient,
			packet.Sequence, packet.TimeoutTimestamp, payloads, ack, proof, proofHeight,
		)
		if err != nil {
			return preparedPacket{}, err
		}
		return preparedPacket{
			Packet: packet, Action: relayAck, Status: PacketAcked, Message: message,
			BroadcastChain: src, ClientID: srcClient, Counterparty: dst,
			ProofHeight: height, AckProofHeight: height, Acknowledgement: ack,
		}, nil
	}

	received, err := packetReceipt(dst.Config, dstClient, packet.Sequence)
	if err != nil {
		return preparedPacket{}, err
	}
	if received {
		return preparedPacket{Action: relayWait, Status: PacketRecvSent}, nil
	}
	// A successful recv tx writes the receipt and ack, but a query issued
	// immediately afterwards can still see the previous height. Wait for the
	// next cycle rather than sending a duplicate recv.
	if packet.RecvTxHash != "" {
		return preparedPacket{Action: relayWait, Status: PacketRecvSent}, nil
	}

	timeoutProof, timeoutProofHeight, timeoutHeight, err := getTimeoutProof(
		dst.Config, dstClient, packet.Sequence, packet.TimeoutTimestamp,
	)
	if err == nil {
		message, err := timeoutMessage(
			src.Config, srcClient, dstClient,
			packet.Sequence, packet.TimeoutTimestamp, payloads, timeoutProof, timeoutProofHeight,
		)
		if err != nil {
			return preparedPacket{}, err
		}
		return preparedPacket{
			Packet: packet, Action: relayTimeout, Status: PacketTimedOut, Message: message,
			BroadcastChain: src, ClientID: srcClient, Counterparty: dst,
			ProofHeight: timeoutHeight,
		}, nil
	}
	if errors.Is(err, errPacketReceived) {
		return preparedPacket{Action: relayWait, Status: PacketRecvSent}, nil
	}
	if !errors.Is(err, errTimeoutNotReached) {
		return preparedPacket{}, err
	}

	commitmentProof, err := getRecvProof(src.Config, srcClient, packet.Sequence, finality, proofHeights)
	if err != nil {
		return preparedPacket{}, err
	}
	message, err := recvMessage(
		dst.Config, dstClient, srcClient,
		packet.Sequence, packet.TimeoutTimestamp, payloads,
		commitmentProof.Height, commitmentProof.Commitment,
	)
	if err != nil {
		return preparedPacket{}, err
	}
	return preparedPacket{
		Packet: packet, Action: relayRecv, Status: PacketRecvSent, Message: message,
		BroadcastChain: dst, ClientID: dstClient, Counterparty: src,
		ProofHeight: commitmentProof.HeightNum,
	}, nil
}

func getRecvProof(src ChainConfig, srcClient string, sequence, finality int64, proofHeights map[string]int64) (proof, error) {
	if finality < 1 {
		return getProof(src, srcClient, sequence)
	}
	height, ok := proofHeights[src.ChainID]
	if !ok {
		var err error
		height, err = latestHeight(src)
		if err != nil {
			return proof{}, err
		}
		proofHeights[src.ChainID] = height
	}
	commitmentProof, err := getProofAtHeight(src, srcClient, sequence, height)
	if err == nil {
		return commitmentProof, nil
	}
	// Fall back to a per-packet proof if the common height is not usable.
	return getProof(src, srcClient, sequence)
}

func groupPrepared(prepared []preparedPacket, batchSize int) [][]preparedPacket {
	if batchSize < 1 {
		batchSize = 1
	}
	groups := make(map[batchKey][]preparedPacket)
	for _, packet := range prepared {
		key := batchKey{
			ChainID:     packet.BroadcastChain.Config.ChainID,
			ClientID:    packet.ClientID,
			Action:      packet.Action,
			ProofHeight: packet.ProofHeight,
		}
		groups[key] = append(groups[key], packet)
	}
	keys := make([]batchKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ChainID != keys[j].ChainID {
			return keys[i].ChainID < keys[j].ChainID
		}
		if keys[i].ClientID != keys[j].ClientID {
			return keys[i].ClientID < keys[j].ClientID
		}
		if keys[i].Action != keys[j].Action {
			return keys[i].Action < keys[j].Action
		}
		return keys[i].ProofHeight < keys[j].ProofHeight
	})

	var chunks [][]preparedPacket
	for _, key := range keys {
		group := groups[key]
		sort.Slice(group, func(i, j int) bool {
			return group[i].Packet.Sequence < group[j].Packet.Sequence
		})
		for len(group) > 0 {
			size := batchSize
			if size > len(group) {
				size = len(group)
			}
			chunks = append(chunks, group[:size])
			group = group[size:]
		}
	}
	return chunks
}

func processPreparedGroup(store *LinkStore, group []preparedPacket, stats *relayStats) error {
	first := group[0]
	if err := ensureClientAtHeight(
		first.BroadcastChain.Config,
		first.Counterparty.Config,
		first.ClientID,
		first.ProofHeight,
	); err != nil {
		return markPreparedFailure(store, group, err)
	}

	messages := make([]map[string]any, 0, len(group))
	for _, packet := range group {
		messages = append(messages, packet.Message)
	}
	hash, err := broadcastMessages(first.BroadcastChain.Config, messages)
	if err != nil {
		if len(group) > 1 {
			log.Printf("relay: %s: %s batch failed, retrying individually: %v",
				first.Packet.LinkID, relayActionName(first.Action), err)
			for _, packet := range group {
				if err := processPreparedGroup(store, []preparedPacket{packet}, stats); err != nil {
					return err
				}
			}
			return nil
		}
		return markPreparedFailure(store, group, err)
	}
	if first.Action == relayRecv {
		if acks, err := recvAcknowledgements(first.BroadcastChain.Config, hash); err == nil {
			for i := range group {
				group[i].Acknowledgement = acks[group[i].Packet.Sequence]
			}
		}
	}
	return markPreparedSuccess(store, group, hash, stats)
}

func markPreparedFailure(store *LinkStore, group []preparedPacket, relayErr error) error {
	now := time.Now().UTC()
	packets := make([]PacketRecord, 0, len(group))
	for _, prepared := range group {
		packet := prepared.Packet
		packet.Attempts++
		packet.LastError = relayErr.Error()
		packet.NextAttemptAt = now.Add(relayBackoff(packet.Attempts))
		packets = append(packets, packet)
		log.Printf("relay: %s packet %s/%d: %v", packet.LinkID, packet.Direction, packet.Sequence, relayErr)
	}
	return store.UpdatePackets(packets)
}

func markPreparedSuccess(store *LinkStore, group []preparedPacket, hash string, stats *relayStats) error {
	packets := make([]PacketRecord, 0, len(group))
	for _, prepared := range group {
		packet := prepared.Packet
		switch prepared.Action {
		case relayRecv:
			packet.Status = PacketRecvSent
			packet.RecvTxHash = hash
			packet.ProofHeight = prepared.ProofHeight
			packet.Acknowledgement = prepared.Acknowledgement
		case relayAck:
			packet.Status = PacketAcked
			packet.AckTxHash = hash
			packet.AckProofHeight = prepared.AckProofHeight
			packet.Acknowledgement = prepared.Acknowledgement
		case relayTimeout:
			packet.Status = PacketTimedOut
			packet.TimeoutTxHash = hash
			packet.ProofHeight = prepared.ProofHeight
		}
		packet.Attempts = 0
		packet.LastError = ""
		packet.NextAttemptAt = time.Time{}
		packets = append(packets, packet)
		stats.addAction(relayActionName(prepared.Action))
	}
	if err := store.UpdatePackets(packets); err != nil {
		return err
	}
	log.Printf("relay: %s: %s tx %s (%d packet(s))",
		group[0].Packet.LinkID, relayActionName(group[0].Action), hash, len(group))
	return nil
}

func relayActionName(action relayAction) string {
	switch action {
	case relayRecv:
		return "recv"
	case relayAck:
		return "ack"
	case relayTimeout:
		return "timeout"
	default:
		return "relay"
	}
}

func oppositeDirection(direction string) string {
	if direction == directionAToB {
		return directionBToA
	}
	return directionAToB
}

func refreshLinkClients(chainA, chainB namedChain, link Link, opts relayOptions, stats *relayStats, refreshed map[string]time.Time) error {
	if opts.ClientRefresh <= 0 {
		return nil
	}
	if last, ok := refreshed[link.ID]; ok && time.Since(last) < opts.ClientRefresh {
		return nil
	}

	now := time.Now().UTC()
	var errs []error
	updated := false
	if didUpdate, err := refreshClient(chainA, chainB, link.ClientA); err != nil {
		errs = append(errs, fmt.Errorf("refresh %s on %s: %w", link.ClientA, link.ChainA, err))
	} else if didUpdate {
		updated = true
		stats.addAction("client_update")
	}
	if didUpdate, err := refreshClient(chainB, chainA, link.ClientB); err != nil {
		errs = append(errs, fmt.Errorf("refresh %s on %s: %w", link.ClientB, link.ChainB, err))
	} else if didUpdate {
		updated = true
		stats.addAction("client_update")
	}
	if len(errs) == 0 {
		refreshed[link.ID] = now
	}
	if updated {
		log.Printf("relay: %s: refreshed clients", link.ID)
	}
	return errors.Join(errs...)
}

func refreshClient(chain, counterparty namedChain, clientID string) (bool, error) {
	latest, err := latestHeight(counterparty.Config)
	if err != nil {
		return false, err
	}
	_, trusted, err := clientLatestHeight(chain.Config, clientID)
	if err != nil {
		return false, err
	}
	if latest <= trusted {
		return false, nil
	}
	if err := updateClient(chain.Config, counterparty.Config, clientID, latest); err != nil {
		return false, err
	}
	return true, nil
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
