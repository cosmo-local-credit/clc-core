package worker

import (
	"errors"
	"math/big"
)

var ErrInvalidNumberString = errors.New("invalid number string")

func StringToBigInt(s string) (*big.Int, error) {
	n := new(big.Int)
	n, ok := n.SetString(s, 10)
	if !ok {
		return nil, ErrInvalidNumberString
	}
	return n, nil
}
