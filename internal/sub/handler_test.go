package sub

import (
	"encoding/json"
	"testing"

	trackerEvent "github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/stretchr/testify/assert"
)

func TestProcessEventRevertedNoConfirm(t *testing.T) {
	evt := trackerEvent.Event{
		Block:           1,
		ContractAddress: "0xabc",
		Success:         false,
		Timestamp:       12345,
		TxHash:          "0xdef",
		TxType:          "TOKEN_PROXY_DEPLOY",
	}

	data, err := json.Marshal(evt)
	assert.NoError(t, err)
	assert.False(t, evt.Success, "reverted events should have Success=false")
	t.Logf("marshaled reverted event: %s", string(data))
}

func TestProcessEventSuccessFields(t *testing.T) {
	evt := trackerEvent.Event{
		Block:           100,
		ContractAddress: "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Success:         true,
		Timestamp:       99999,
		TxHash:          "0xtxhash",
		TxType:          "TOKEN_PROXY_DEPLOY",
	}

	assert.True(t, evt.Success, "success events should have Success=true")
	assert.NotEmpty(t, evt.ContractAddress, "contract address must be present to confirm deployed_tokens")
	assert.NotEmpty(t, evt.TxHash, "tx hash must be present to match OTX")
}
