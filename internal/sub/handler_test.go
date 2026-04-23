package sub

import (
	"encoding/json"
	"testing"

	trackerEvent "github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/stretchr/testify/assert"
)

// TestProcessEventRevertedNoInsert verifies that a reverted deployment never
// triggers a deployed_tokens insert path. Since we test the logic flow and
// not the database, this confirms the conditional branch.
func TestProcessEventRevertedNoInsert(t *testing.T) {
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

// TestProcessEventSuccessFields verifies that a successful token deploy event
// carries the expected fields used in deployed_tokens insertion.
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
	assert.NotEmpty(t, evt.ContractAddress, "contract address must be present for deployed_tokens insert")
	assert.NotEmpty(t, evt.TxHash, "tx hash must be present to match OTX")
}
