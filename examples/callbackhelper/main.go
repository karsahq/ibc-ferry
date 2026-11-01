//go:build ignore

// Helper used by examples/karsa-callback.sh. Prints the values the
// relayer and the chain must agree on: the IBC voucher denom on the receiving
// chain, the isolated receiver address, the callback contract bytecode, and the
// calldata that pulls the voucher.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"

	ibctestutil "github.com/cosmos/evm/x/ibc/callbacks/testutil"
	callbacktypes "github.com/cosmos/evm/x/ibc/callbacks/types"
	transfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
)

func main() {
	destClient := flag.String("dest-client", "07-tendermint-0", "destination client id")
	sender := flag.String("sender", "", "packet sender address on the source chain")
	erc20 := flag.String("erc20", "0x2000000000000000000000000000000000000002", "ERC20 address of the voucher")
	amount := flag.Int64("amount", 1000, "transfer amount")
	flag.Parse()

	if *sender == "" {
		fmt.Fprintln(os.Stderr, "-sender is required")
		os.Exit(1)
	}

	contract, err := ibctestutil.LoadCounterWithCallbacksContract()
	if err != nil {
		panic(err)
	}
	calldata, err := contract.ABI.Pack("add", common.HexToAddress(*erc20), big.NewInt(*amount))
	if err != nil {
		panic(err)
	}

	denom := transfertypes.Denom{
		Base:  "akra",
		Trace: []transfertypes.Hop{transfertypes.NewHop(transfertypes.PortID, *destClient)},
	}
	isolated := callbacktypes.GenerateIsolatedAddress(*destClient, *sender)
	isolatedBech32, err := sdk.Bech32ifyAddressBytes("karsa", isolated)
	if err != nil {
		panic(err)
	}

	out := map[string]string{
		"voucher_denom":     denom.IBCDenom(),
		"isolated_bech32":   isolatedBech32,
		"isolated_hex":      common.BytesToAddress(isolated).Hex(),
		"contract_bytecode": "0x" + common.Bytes2Hex(contract.Bin),
		"calldata":          common.Bytes2Hex(calldata),
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(encoded))
}
