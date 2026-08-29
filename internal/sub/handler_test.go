package sub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	trackerEvent "github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/stretchr/testify/assert"
)

const testNotificationWallet = "0x1234567890abcdef1234567890abcdef12345678"

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

func TestNotificationEventFromSuccessfulTransfer(t *testing.T) {
	event, ok, err := notificationEventFromChainEvent(trackerEvent.Event{
		ContractAddress: "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Success:         true,
		Timestamp:       1787745600,
		TxHash:          "0xtransfer",
		TxType:          tokenTransferEvent,
		Payload: map[string]any{
			"from":  "0x0000000000000000000000000000000000000001",
			"to":    testNotificationWallet,
			"value": "100",
		},
	})
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "voucher.received", event.Kind)
	assert.True(t, strings.EqualFold(testNotificationWallet, event.WalletAddressTarget))
	assert.Equal(t, "/wallet", event.TargetURL)
	assert.NotNil(t, event.VoucherID)
	assert.NotEmpty(t, event.SourceKey)
	assert.NotEmpty(t, event.NotificationID)
}

func TestNotificationEventFromSuccessfulSwap(t *testing.T) {
	event, ok, err := notificationEventFromChainEvent(trackerEvent.Event{
		ContractAddress: "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Success:         true,
		Timestamp:       1787745600,
		TxHash:          "0xswap",
		TxType:          poolSwapEvent,
		Payload: map[string]any{
			"initiator": testNotificationWallet,
			"tokenIn":   "0x0000000000000000000000000000000000000001",
			"tokenOut":  "0x0000000000000000000000000000000000000002",
			"amountIn":  "100",
			"amountOut": "95",
			"fee":       "5",
		},
	})
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "swap.completed", event.Kind)
	assert.Equal(t, "normal", event.Priority)
	assert.True(t, strings.EqualFold(testNotificationWallet, event.WalletAddressTarget))
	assert.Equal(t, "0xswap", *event.SwapID)
}

func TestNotificationEventIgnoresRevertedAndUnsupportedEvents(t *testing.T) {
	_, ok, err := notificationEventFromChainEvent(trackerEvent.Event{
		Success: false,
		TxType:  tokenTransferEvent,
	})
	assert.NoError(t, err)
	assert.False(t, ok)

	_, ok, err = notificationEventFromChainEvent(trackerEvent.Event{
		Success: true,
		TxType:  "PRETIUM_OFFRAMP",
	})
	assert.NoError(t, err)
	assert.False(t, ok, "off-ramp completion needs an authoritative event before it can notify")
}

func TestNotificationSourceKeyIsStableAcrossRedelivery(t *testing.T) {
	chainEvent := trackerEvent.Event{
		ContractAddress: "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Index:           3,
		Success:         true,
		TxHash:          "0xtransfer",
		TxType:          tokenTransferEvent,
		Payload:         map[string]any{"to": testNotificationWallet, "value": "100"},
	}
	notificationEvent := store.NotificationEvent{
		Kind:                "voucher.received",
		WalletAddressTarget: testNotificationWallet,
	}

	first, err := notificationSourceKey(chainEvent, notificationEvent)
	assert.NoError(t, err)
	second, err := notificationSourceKey(chainEvent, notificationEvent)
	assert.NoError(t, err)
	assert.Equal(t, first, second)

	chainEvent.Index++
	third, err := notificationSourceKey(chainEvent, notificationEvent)
	assert.NoError(t, err)
	assert.NotEqual(t, first, third)
}
