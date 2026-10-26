package safe

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/lmittmann/w3"
	"github.com/stretchr/testify/require"
)

// Execution against real Safe contracts on a Gnosis fork:
//
//	anvil --fork-url <gnosis rpc> --port 8547
//	SAFE_FORK_RPC=http://127.0.0.1:8547 go test ./internal/safe -run RealSafes
//
// A third party (anvil's first account, standing in for clc-core's system
// signer) executes with signatures packed by PackSignatures.

var (
	createProxyFunc   = w3.MustNewFunc("createProxyWithNonce(address,bytes,uint256)", "address")
	setupFunc         = w3.MustNewFunc("setup(address[],uint256,address,bytes,address,address,uint256,address)", "")
	getTxHashFunc     = w3.MustNewFunc("getTransactionHash(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,uint256)", "bytes32")
	forkNonceFunc     = w3.MustNewFunc("nonce()", "uint256")
	anvilFirstAccount = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
)

func TestExecutionAgainstRealSafes(t *testing.T) {
	rpcURL := os.Getenv("SAFE_FORK_RPC")
	if rpcURL == "" {
		t.Skip("SAFE_FORK_RPC not set; run anvil --fork-url <gnosis rpc>")
	}
	ctx := context.Background()
	client, err := ethclient.Dial(rpcURL)
	require.NoError(t, err)
	chainID, err := client.ChainID(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 100, chainID.Int64(), "fork Gnosis")
	payer, err := crypto.HexToECDSA(anvilFirstAccount)
	require.NoError(t, err)

	for _, version := range []struct{ name, singleton, factory string }{
		{"1.3.0", "0x3E5c63644E683549055b9Be8653de26E0B4CD36E", "0xa6B71E26C5e0845f74c812102Ca7114b6a896AB2"},
		{"1.5.0", "0xEdd160fEBBD92E350D4D398fb636302fccd67C7e", "0x14F2982D601c9458F93bd70B218933A6f8165e7b"},
	} {
		t.Run(version.name, func(t *testing.T) {
			owners := newOwners(t, 3)
			setup, err := setupFunc.EncodeArgs(addresses(owners), big.NewInt(2), common.Address{}, []byte{},
				common.Address{}, common.Address{}, big.NewInt(0), common.Address{})
			require.NoError(t, err)
			create, err := createProxyFunc.EncodeArgs(common.HexToAddress(version.singleton), setup, big.NewInt(time.Now().UnixNano()))
			require.NoError(t, err)
			factory := common.HexToAddress(version.factory)
			out, err := client.CallContract(ctx, ethereum.CallMsg{From: crypto.PubkeyToAddress(payer.PublicKey), To: &factory, Data: create}, nil)
			require.NoError(t, err)
			var proxy common.Address
			require.NoError(t, createProxyFunc.DecodeReturns(out, &proxy))
			sendFork(t, client, payer, factory, create)

			fields := TxFields{
				To: owners[2].addr.Hex(), Value: "0", Data: "0x", Operation: 0,
				SafeTxGas: "0", BaseGas: "0", GasPrice: "0",
				GasToken: common.Address{}.Hex(), RefundReceiver: common.Address{}.Hex(), Nonce: "0",
			}
			hash, err := SafeTxHash(100, proxy, fields)
			require.NoError(t, err)
			exec, err := fields.Exec()
			require.NoError(t, err)
			query, err := getTxHashFunc.EncodeArgs(exec.To, exec.Value, exec.Data, exec.Operation, exec.SafeTxGas,
				exec.BaseGas, exec.GasPrice, exec.GasToken, exec.RefundReceiver, big.NewInt(0))
			require.NoError(t, err)
			out, err = client.CallContract(ctx, ethereum.CallMsg{To: &proxy, Data: query}, nil)
			require.NoError(t, err)
			var onchain common.Hash
			require.NoError(t, getTxHashFunc.DecodeReturns(out, &onchain))
			require.Equal(t, onchain, hash, "our SafeTx digest must be the Safe's own")

			// One typed-data and one eth_sign confirmation, as the Safe app
			// and wallets produce them.
			exec.Signatures, err = PackSignatures(hash, []Confirmation{
				typedSig(t, owners[0], hash), ethSignSig(t, owners[1], hash),
			}, addresses(owners), 2)
			require.NoError(t, err)
			calldata, err := EncodeExecTransaction(exec)
			require.NoError(t, err)
			receipt := sendFork(t, client, payer, proxy, calldata)
			require.EqualValues(t, types.ReceiptStatusSuccessful, receipt.Status)

			nonceCall, err := forkNonceFunc.EncodeArgs()
			require.NoError(t, err)
			out, err = client.CallContract(ctx, ethereum.CallMsg{To: &proxy, Data: nonceCall}, nil)
			require.NoError(t, err)
			var nonce big.Int
			require.NoError(t, forkNonceFunc.DecodeReturns(out, &nonce))
			require.EqualValues(t, 1, nonce.Int64(), "the Safe executed nonce 0")
		})
	}
}

func sendFork(t *testing.T, client *ethclient.Client, key *ecdsa.PrivateKey, to common.Address, data []byte) *types.Receipt {
	t.Helper()
	ctx := context.Background()
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := client.PendingNonceAt(ctx, from)
	require.NoError(t, err)
	gas, err := client.EstimateGas(ctx, ethereum.CallMsg{From: from, To: &to, Data: data})
	require.NoError(t, err)
	tip, err := client.SuggestGasTipCap(ctx)
	require.NoError(t, err)
	head, err := client.HeaderByNumber(ctx, nil)
	require.NoError(t, err)
	feeCap := new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tip)
	signed, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(100), Nonce: nonce, GasTipCap: tip, GasFeeCap: feeCap,
		Gas: gas + gas/4, To: &to, Data: data,
	}), types.LatestSignerForChainID(big.NewInt(100)), key)
	require.NoError(t, err)
	require.NoError(t, client.SendTransaction(ctx, signed))
	for range 50 {
		receipt, err := client.TransactionReceipt(ctx, signed.Hash())
		if err == nil {
			return receipt
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no receipt for %s", signed.Hash().Hex())
	return nil
}
