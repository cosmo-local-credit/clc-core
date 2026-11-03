package util

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyRotationStatementRoundTrips(t *testing.T) {
	oldAddress := common.HexToAddress("0x6a347ba6b1ec10348478377c5fcee7ceb6921a69")
	newAddress := common.HexToAddress("0xf4e5a91e30903f508735943a78fe382dfb18da22")
	statement := KeyRotationStatement(oldAddress, newAddress)
	// clc-app's buildKeyRotationStatement writes exactly this text.
	assert.Equal(t, "Replace sign-in card 0x6a347bA6B1eC10348478377c5fCeE7Ceb6921A69 with 0xf4e5A91e30903F508735943a78Fe382dfB18Da22", statement)

	gotOld, gotNew, err := ParseKeyRotationStatement(statement)
	require.NoError(t, err)
	assert.Equal(t, oldAddress, gotOld)
	assert.Equal(t, newAddress, gotNew)

	message := "app.example wants you to sign in with your Ethereum account:\n" + newAddress.Hex() +
		"\n\n" + statement + "\n\nURI: https://app.example\nVersion: 1\nChain ID: 100\nNonce: abc\nIssued At: 2026-11-03T00:00:00Z"
	assert.Equal(t, statement, ParseSIWEStatement(message))
	chainID, err := ParseSIWEChainID(message)
	require.NoError(t, err)
	assert.Equal(t, uint64(100), chainID)
}

func TestKeyRotationStatementRejectsLookalikes(t *testing.T) {
	for name, statement := range map[string]string{
		"sign-in":       "Sign in to CLC App",
		"lowercase":     "Replace sign-in card 0x6a347ba6b1ec10348478377c5fcee7ceb6921a69 with 0xf4e5a91e30903f508735943a78fe382dfb18da22",
		"same address":  "Replace sign-in card 0x6a347bA6B1eC10348478377c5fCeE7Ceb6921A69 with 0x6a347bA6B1eC10348478377c5fCeE7Ceb6921A69",
		"trailing text": "Replace sign-in card 0x6a347bA6B1eC10348478377c5fCeE7Ceb6921A69 with 0xf4e5A91e30903F508735943a78Fe382dfB18Da22 now",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseKeyRotationStatement(statement)
			assert.Error(t, err)
		})
	}
	assert.Empty(t, ParseSIWEStatement("domain wants you to sign in with your Ethereum account:\n0x0\n\nURI: x"))
}
