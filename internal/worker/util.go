package worker

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func createSalt(signerAddress common.Address, trackingID string, suffix string) common.Hash {
	hash := crypto.Keccak256Hash([]byte(trackingID + "-" + suffix))
	var salt common.Hash
	copy(salt[:20], signerAddress.Bytes())
	copy(salt[20:], hash[20:])
	return salt
}
