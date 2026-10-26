package safe

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

type (
	// TxFields is a Safe transaction as clients and the Transaction Service
	// carry it: every number a decimal string, data 0x-hex.
	TxFields struct {
		To             string `json:"to"`
		Value          string `json:"value"`
		Data           string `json:"data"`
		Operation      uint8  `json:"operation"`
		SafeTxGas      string `json:"safeTxGas"`
		BaseGas        string `json:"baseGas"`
		GasPrice       string `json:"gasPrice"`
		GasToken       string `json:"gasToken"`
		RefundReceiver string `json:"refundReceiver"`
		Nonce          string `json:"nonce"`
	}

	Confirmation struct {
		Owner       string `json:"owner"`
		Signature   string `json:"signature"`
		SubmittedAt string `json:"submittedAt"`
	}

	// Transaction is the object every /safe endpoint returns.
	Transaction struct {
		TxFields

		Safe                  string         `json:"safe"`
		SafeTxHash            string         `json:"safeTxHash"`
		Confirmations         []Confirmation `json:"confirmations"`
		ConfirmationsRequired int            `json:"confirmationsRequired"`
		IsExecuted            bool           `json:"isExecuted"`
		IsSuccessful          *bool          `json:"isSuccessful"`
		TransactionHash       *string        `json:"transactionHash"`
	}
)

var (
	domainTypeHash = crypto.Keccak256Hash([]byte("EIP712Domain(uint256 chainId,address verifyingContract)"))
	safeTxTypeHash = crypto.Keccak256Hash([]byte("SafeTx(address to,uint256 value,bytes data,uint8 operation,uint256 safeTxGas,uint256 baseGas,uint256 gasPrice,address gasToken,address refundReceiver,uint256 nonce)"))
)

func uintOf(name, s string) (*big.Int, error) {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() < 0 || n.BitLen() > 256 {
		return nil, fmt.Errorf("invalid %s", name)
	}
	return n, nil
}

func addressOf(name, s string) (common.Address, error) {
	if !common.IsHexAddress(s) {
		return common.Address{}, fmt.Errorf("invalid %s", name)
	}
	return common.HexToAddress(s), nil
}

// Exec parses the fields into the form the policy checks. Malformed input is
// an error, never a zero value.
func (f TxFields) Exec() (*Exec, error) {
	var (
		e   Exec
		err error
	)
	if e.To, err = addressOf("to", f.To); err != nil {
		return nil, err
	}
	if e.GasToken, err = addressOf("gasToken", f.GasToken); err != nil {
		return nil, err
	}
	if e.RefundReceiver, err = addressOf("refundReceiver", f.RefundReceiver); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name string
		src  string
		dst  **big.Int
	}{
		{"value", f.Value, &e.Value}, {"safeTxGas", f.SafeTxGas, &e.SafeTxGas},
		{"baseGas", f.BaseGas, &e.BaseGas}, {"gasPrice", f.GasPrice, &e.GasPrice},
	} {
		if *field.dst, err = uintOf(field.name, field.src); err != nil {
			return nil, err
		}
	}
	if f.Operation > OperationDelegateCall {
		return nil, fmt.Errorf("invalid operation")
	}
	e.Operation = f.Operation
	if e.Data, err = hexutil.Decode(f.Data); err != nil {
		return nil, fmt.Errorf("invalid data")
	}
	return &e, nil
}

// SafeTxHash is the EIP-712 digest owners sign: domain {chainId,
// verifyingContract}, matching Safe 1.3.0 and 1.4.1.
func SafeTxHash(chainID int64, safe common.Address, f TxFields) (common.Hash, error) {
	e, err := f.Exec()
	if err != nil {
		return common.Hash{}, err
	}
	nonce, err := uintOf("nonce", f.Nonce)
	if err != nil {
		return common.Hash{}, err
	}
	typ := func(s string) abi.Type { t, _ := abi.NewType(s, "", nil); return t }
	domainArgs := abi.Arguments{{Type: typ("bytes32")}, {Type: typ("uint256")}, {Type: typ("address")}}
	domain, err := domainArgs.Pack(domainTypeHash, big.NewInt(chainID), safe)
	if err != nil {
		return common.Hash{}, err
	}
	structArgs := abi.Arguments{
		{Type: typ("bytes32")}, {Type: typ("address")}, {Type: typ("uint256")}, {Type: typ("bytes32")},
		{Type: typ("uint8")}, {Type: typ("uint256")}, {Type: typ("uint256")}, {Type: typ("uint256")},
		{Type: typ("address")}, {Type: typ("address")}, {Type: typ("uint256")},
	}
	body, err := structArgs.Pack(safeTxTypeHash, e.To, e.Value, crypto.Keccak256Hash(e.Data), e.Operation,
		e.SafeTxGas, e.BaseGas, e.GasPrice, e.GasToken, e.RefundReceiver, nonce)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, crypto.Keccak256(domain), crypto.Keccak256(body)), nil
}

// RecoverSigner returns the address that signed hash with a 65-byte r|s|v
// signature, v 27 or 28. Other signature kinds (contract, approved-hash,
// eth_sign) are rejected, as the app produces none of them.
func RecoverSigner(hash common.Hash, signature []byte) (common.Address, error) {
	if len(signature) != 65 || (signature[64] != 27 && signature[64] != 28) {
		return common.Address{}, fmt.Errorf("signature must be 65 bytes with v 27 or 28")
	}
	raw := make([]byte, 65)
	copy(raw, signature)
	raw[64] -= 27
	pub, err := crypto.SigToPub(hash[:], raw)
	if err != nil {
		return common.Address{}, fmt.Errorf("invalid signature")
	}
	return crypto.PubkeyToAddress(*pub), nil
}
