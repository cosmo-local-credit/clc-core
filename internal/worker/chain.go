package worker

import "github.com/lmittmann/w3"

const (
	Approve           = "approve"
	Check             = "check"
	GiveTo            = "giveTo"
	Have              = "have"
	MintTo            = "mintTo"
	NextTime          = "nextTime"
	Register          = "register"
	Transfer          = "transfer"
	TransferFrom      = "transferFrom"
	TransferOwnership = "transferOwnership"
	Withdraw          = "withdraw"
	Deposit           = "deposit"
	Sweep             = "sweep"
	Add               = "add"
	Remove            = "remove"
	SetQuoter         = "setQuoter"
	AddressOf         = "addressOf"
)

var Abi = map[string]*w3.Func{
	// ERC20
	Approve:           w3.MustNewFunc("approve(address, uint256)", "bool"),
	MintTo:            w3.MustNewFunc("mintTo(address, uint256)", "bool"),
	Transfer:          w3.MustNewFunc("transfer(address,uint256)", "bool"),
	Sweep:             w3.MustNewFunc("sweep(address)", "uint256"),
	TransferOwnership: w3.MustNewFunc("transferOwnership(address)", "bool"),
	// GasFaucet
	Check:    w3.MustNewFunc("check(address)", "bool"),
	GiveTo:   w3.MustNewFunc("giveTo(address)", "uint256"),
	NextTime: w3.MustNewFunc("nextTime(address)", "uint256"),
	// CustodialRegistrationProxy
	Register: w3.MustNewFunc("register(address)", ""),
	// Pool
	Withdraw:  w3.MustNewFunc("withdraw(address,address,uint256)", ""),
	Deposit:   w3.MustNewFunc("deposit(address,uint256)", ""),
	SetQuoter: w3.MustNewFunc("setQuoter(address)", ""),
	// AccountIndex / TokenIndex
	Have:      w3.MustNewFunc("have(address)", "bool"),
	Add:       w3.MustNewFunc("add(address)", "bool"),
	Remove:    w3.MustNewFunc("remove(address)", "bool"),
	AddressOf: w3.MustNewFunc("addressOf(bytes32)", "address"),
}
