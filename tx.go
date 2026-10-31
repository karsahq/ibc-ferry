package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func emptyTx(message map[string]any, gas string) map[string]any {
	return emptyBatchTx([]map[string]any{message}, gas)
}

func emptyBatchTx(messages []map[string]any, gas string) map[string]any {
	encoded := make([]any, 0, len(messages))
	for _, message := range messages {
		encoded = append(encoded, message)
	}
	return map[string]any{
		"body": map[string]any{
			"messages":                       encoded,
			"memo":                           "",
			"timeout_height":                 "0",
			"unordered":                      false,
			"timeout_timestamp":              nil,
			"extension_options":              []any{},
			"non_critical_extension_options": []any{},
		},
		"auth_info": map[string]any{
			"signer_infos": []any{},
			"fee": map[string]any{
				"amount":    []any{},
				"gas_limit": gas,
				"payer":     "",
				"granter":   "",
			},
			"tip": nil,
		},
		"signatures": []any{},
	}
}

func broadcast(c ChainConfig, tx map[string]any) (string, error) {
	if len(c.Fee) > 0 {
		fee := make([]any, 0, len(c.Fee))
		for _, coin := range c.Fee {
			fee = append(fee, map[string]any{"denom": coin.Denom, "amount": coin.Amount})
		}
		auth := tx["auth_info"].(map[string]any)
		auth["fee"].(map[string]any)["amount"] = fee
	}

	dir, err := os.MkdirTemp("", "ibc-ferry-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	unsigned := filepath.Join(dir, "tx.json")
	signed := filepath.Join(dir, "tx.signed.json")
	data, err := json.Marshal(tx)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(unsigned, data, 0o600); err != nil {
		return "", err
	}

	if _, err := runCLI(
		c, true, "tx", "sign", unsigned,
		"--from", c.From,
		"--keyring-backend", c.KeyringBackend,
		"--node", c.Node,
		"--output-document", signed,
	); err != nil {
		return "", err
	}

	out, err := runCLI(c, true, "tx", "broadcast", signed, "--node", c.Node, "-o", "json")
	if err != nil {
		return "", err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return "", fmt.Errorf("decode broadcast result: %w\n%s", err, out)
	}
	if code, ok := result["code"].(float64); ok && code != 0 {
		return "", fmt.Errorf("broadcast failed: %v", result)
	}
	hash, _ := result["txhash"].(string)
	if hash == "" {
		return "", fmt.Errorf("broadcast returned no txhash: %s", out)
	}
	return hash, nil
}
