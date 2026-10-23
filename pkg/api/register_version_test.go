package api

import (
	"testing"

	"github.com/go-playground/validator/v10"
)

// POST /contracts/version stores the type in a contract_type enum column, so
// the request has to carry the enum's values, not a coarse token or pool.
func TestRegisterVersionRequestContractType(t *testing.T) {
	v := validator.New(validator.WithRequiredStructEnabled())
	check := func(contractType string) error {
		return v.StructPartial(RegisterVersionRequest{ContractType: contractType}, "ContractType")
	}

	for _, ok := range []string{
		"ERC20", "ERC20_DEMURRAGE", "SWAPPOOL", "TOKEN_UNIQUE_SYMBOL_INDEX",
		"LIMITER", "FEE_POLICY", "ORACLE_QUOTER", "RELATIVE_QUOTER",
	} {
		if err := check(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "token", "pool", "erc20", "UNKNOWN"} {
		if err := check(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
