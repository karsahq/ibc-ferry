package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func cliArgs(c ChainConfig, withChain bool, args ...string) []string {
	out := make([]string, 0, len(args)+4)
	out = append(out, c.Binary)
	out = append(out, args...)
	out = append(out, "--home", c.Home)
	if withChain {
		out = append(out, "--chain-id", c.ChainID)
	}
	return out
}

func runCLI(c ChainConfig, withChain bool, args ...string) (string, error) {
	full := cliArgs(c, withChain, args...)
	cmd := exec.Command(full[0], full[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w\n%s", strings.Join(cmd.Args, " "), err, stderr.String())
	}
	return string(out), nil
}

func queryJSON(c ChainConfig, args ...string) (map[string]any, error) {
	full := append(append([]string{"q"}, args...), "--node", c.Node, "-o", "json")
	out, err := runCLI(c, true, full...)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return nil, fmt.Errorf("decode query output: %w\n%s", err, out)
	}
	return result, nil
}

func rpcGet(c ChainConfig, path string) (map[string]any, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(c.RPC + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpc %s: %s: %s", path, resp.Status, body)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode rpc %s: %w", path, err)
	}
	return result, nil
}

func latestHeight(c ChainConfig) (int64, error) {
	res, err := rpcGet(c, "/status")
	if err != nil {
		return 0, err
	}
	result, _ := res["result"].(map[string]any)
	sync, _ := result["sync_info"].(map[string]any)
	height, _ := sync["latest_block_height"].(string)
	return strconv.ParseInt(height, 10, 64)
}

type blockHeader struct {
	Time               string
	AppHash            string
	NextValidatorsHash string
}

func blockAt(c ChainConfig, height int64) (blockHeader, error) {
	res, err := rpcGet(c, fmt.Sprintf("/block?height=%d", height))
	if err != nil {
		return blockHeader{}, err
	}
	result, _ := res["result"].(map[string]any)
	block, _ := result["block"].(map[string]any)
	header, _ := block["header"].(map[string]any)
	return blockHeader{
		Time:               str(header["time"]),
		AppHash:            str(header["app_hash"]),
		NextValidatorsHash: str(header["next_validators_hash"]),
	}, nil
}

func relayerAddress(c ChainConfig) (string, error) {
	out, err := runCLI(c, false, "keys", "show", c.From, "-a", "--keyring-backend", c.KeyringBackend)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func unbondingPeriod(c ChainConfig) (string, error) {
	res, err := queryJSON(c, "staking", "params")
	if err != nil {
		return "", err
	}
	params, _ := res["params"].(map[string]any)
	return str(params["unbonding_time"]), nil
}

func revisionNumber(chainID string) uint64 {
	suffix := chainID
	if i := strings.LastIndex(chainID, "-"); i >= 0 {
		suffix = chainID[i+1:]
	}
	value, err := strconv.ParseUint(suffix, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func waitTx(c ChainConfig, hash string) error {
	_, err := waitTxResult(c, hash)
	return err
}

func waitTxResult(c ChainConfig, hash string) (map[string]any, error) {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		out, err := runCLI(c, true, "q", "tx", hash, "--node", c.Node, "-o", "json")
		if err == nil {
			var tx map[string]any
			if json.Unmarshal([]byte(out), &tx) == nil {
				if code, ok := tx["code"].(float64); ok && code != 0 {
					return nil, fmt.Errorf("tx %s failed: %v", hash, tx["raw_log"])
				}
				return tx, nil
			}
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("tx %s not indexed", hash)
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
