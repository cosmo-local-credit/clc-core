package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeployedTokenModel(t *testing.T) {
	addr := "0xabc"
	_dt := DeployedToken{
		ID:                 1,
		OTXID:              2,
		PlatformContractID: 3,
		Status:             DeployPending,
		Address:            &addr,
		Symbol:             "TKN",
		Name:               "Token",
		Decimals:           6,
		Admin:              "0xadmin",
	}

	assert.Equal(t, uint64(1), _dt.ID)
	assert.Equal(t, uint64(2), _dt.OTXID)
	assert.Equal(t, uint64(3), _dt.PlatformContractID)
	assert.Equal(t, DeployPending, _dt.Status)
	assert.Equal(t, "0xabc", *_dt.Address)
	assert.Equal(t, "TKN", _dt.Symbol)
	assert.Equal(t, "Token", _dt.Name)
	assert.Equal(t, uint8(6), _dt.Decimals)
	assert.Equal(t, "0xadmin", _dt.Admin)
}

func TestDeployedPoolModel(t *testing.T) {
	addr := "0xpool"
	dp := DeployedPool{
		ID:       1,
		OTXID:    2,
		Status:   DeployConfirmed,
		Address:  &addr,
		Name:     "Pool",
		Symbol:   "PL",
		Decimals: 18,
		Admin:    "0xadmin",
	}

	assert.Equal(t, DeployConfirmed, dp.Status)
	assert.Equal(t, "0xpool", *dp.Address)
	assert.Equal(t, uint8(18), dp.Decimals)
}

func TestStoreInterfaceCompile(t *testing.T) {
	var _ Store = (*Pg)(nil)
}
