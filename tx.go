package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	applyFee(tx, c)

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

// gasLimitOf reads the gas limit recorded in an unsigned transaction.
func gasLimitOf(tx map[string]any) int64 {
	auth, _ := tx["auth_info"].(map[string]any)
	fee, _ := auth["fee"].(map[string]any)
	value, _ := fee["gas_limit"].(string)
	limit, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return limit
}

// parseGasPrice splits one gas price such as "20000000000akra" into a rational
// price and its denom. Comma-separated lists and zero prices report false so
// the configured fee is used unchanged.
func parseGasPrice(gasPrices string) (*big.Rat, string, bool) {
	value := strings.TrimSpace(gasPrices)
	if value == "" || strings.Contains(value, ",") {
		return nil, "", false
	}
	index := 0
	for index < len(value) {
		character := value[index]
		if (character < '0' || character > '9') && character != '.' {
			break
		}
		index++
	}
	if index == 0 || index == len(value) {
		return nil, "", false
	}
	price, ok := new(big.Rat).SetString(value[:index])
	if !ok || price.Sign() <= 0 {
		return nil, "", false
	}
	return price, value[index:], true
}

// feeForGas returns the fee a chain requires for gasLimit at gasPrices.
func feeForGas(gasPrices string, gasLimit int64) (FeeCoin, bool) {
	if gasLimit <= 0 {
		return FeeCoin{}, false
	}
	price, denom, ok := parseGasPrice(gasPrices)
	if !ok {
		return FeeCoin{}, false
	}
	required := price.Mul(price, new(big.Rat).SetInt64(gasLimit))
	amount := new(big.Int).Quo(required.Num(), required.Denom())
	if new(big.Int).Rem(required.Num(), required.Denom()).Sign() != 0 {
		amount.Add(amount, big.NewInt(1))
	}
	return FeeCoin{Denom: denom, Amount: amount.String()}, true
}

func compareAmount(left, right string) int {
	leftValue, ok := new(big.Int).SetString(left, 10)
	if !ok {
		return -1
	}
	rightValue, ok := new(big.Int).SetString(right, 10)
	if !ok {
		return 1
	}
	return leftValue.Cmp(rightValue)
}

// applyFee writes the transaction fee and raises a configured amount when the
// gas limit and gas price demand more. Chains check the fee against the gas
// limit, so a fixed fee that covers one message can still be too small for a
// batch that shares a single proof height.
func applyFee(tx map[string]any, c ChainConfig) {
	auth, ok := tx["auth_info"].(map[string]any)
	if !ok {
		return
	}
	fee, ok := auth["fee"].(map[string]any)
	if !ok {
		return
	}

	coins := make([]FeeCoin, 0, len(c.Fee)+1)
	coins = append(coins, c.Fee...)
	if required, ok := feeForGas(c.GasPrices, gasLimitOf(tx)); ok {
		found := false
		for index := range coins {
			if coins[index].Denom != required.Denom {
				continue
			}
			found = true
			if compareAmount(coins[index].Amount, required.Amount) < 0 {
				coins[index].Amount = required.Amount
			}
			break
		}
		if !found {
			coins = append(coins, required)
		}
	}

	amount := make([]any, 0, len(coins))
	for _, coin := range coins {
		if coin.Denom == "" || coin.Amount == "" {
			continue
		}
		amount = append(amount, map[string]any{"denom": coin.Denom, "amount": coin.Amount})
	}
	fee["amount"] = amount
}
