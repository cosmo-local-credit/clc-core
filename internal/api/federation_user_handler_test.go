package api

import (
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

func TestExactRawUnitValue(t *testing.T) {
	tests := []struct {
		name       string
		amountRaw  string
		decimals   int32
		claimValue string
		want       string
		valid      bool
	}{
		{name: "whole units", amountRaw: "10000000", decimals: 6, claimValue: "1", want: "10", valid: true},
		{name: "fractional claim", amountRaw: "123456789", decimals: 6, claimValue: "1.25", want: "154.32098625", valid: true},
		{name: "large exact value", amountRaw: "900719925474099325000000", decimals: 6, claimValue: "2.5", want: "2251799813685248312.5", valid: true},
		{name: "zero transfer", amountRaw: "0", decimals: 18, claimValue: "1.5", want: "0", valid: true},
		{name: "zero claim", amountRaw: "1", decimals: 0, claimValue: "0", valid: false},
		{name: "negative amount", amountRaw: "-1", decimals: 0, claimValue: "1", valid: false},
		{name: "exponent claim", amountRaw: "1", decimals: 0, claimValue: "1e3", valid: false},
		{name: "invalid decimals", amountRaw: "1", decimals: -1, claimValue: "1", valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := exactRawUnitValue(test.amountRaw, test.decimals, test.claimValue)
			if valid != test.valid || got != test.want {
				t.Fatalf("exactRawUnitValue() = (%q, %v), want (%q, %v)", got, valid, test.want, test.valid)
			}
		})
	}
}

func TestActivityLegUsesImmutableClaimAndExactRawAmount(t *testing.T) {
	name, symbol, decimals := "Voucher", "VCH", int32(6)
	address := "0x1111111111111111111111111111111111111111"
	txHash := "0xtransaction"
	metadata := map[string]store.VoucherMetadata{
		address: {Address: address, Name: &name, Symbol: &symbol, Decimals: &decimals},
	}
	valuations := map[string]store.TransferValuationSnapshot{
		transferValuationKey(txHash, address): {
			TxHash:       txHash,
			TokenAddress: address,
			VoucherValue: "1.25",
			VoucherUOA:   "Community Hours",
		},
	}

	leg := activityLeg(address, "900719925474099325000000", metadata, valuations, txHash)
	require.Equal(t, "Voucher", leg.TokenName)
	require.Equal(t, "VCH", leg.TokenSymbol)
	require.EqualValues(t, 6, leg.TokenDecimals)
	require.Equal(t, "1125899906842624156.25", leg.UOAValue)
	require.Equal(t, "Community Hours", leg.UOA)

	legacy := activityLeg(address, "1000000", metadata, nil, txHash)
	require.Empty(t, legacy.UOAValue)
	require.Empty(t, legacy.UOA)
}
