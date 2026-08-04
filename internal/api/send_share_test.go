package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
)

func transferBatch(recipient string, amount uint64) apiresp.CaliburSignedBatch {
	addressHex := strings.TrimPrefix(recipient, "0x")
	addressWord := strings.Repeat("0", 64-len(addressHex)) + addressHex
	amountWord := fmt.Sprintf("%064x", amount)
	return apiresp.CaliburSignedBatch{
		BatchedCall: apiresp.CaliburBatchedCall{
			Calls: []apiresp.CaliburCall{{
				To:    "0x1111111111111111111111111111111111111111",
				Value: "0",
				Data:  "0xa9059cbb" + addressWord + amountWord,
			}},
		},
	}
}

func TestSendShareRecipientDecodesERC20Transfer(t *testing.T) {
	want := common.HexToAddress("0x2222222222222222222222222222222222222222")
	got, err := sendShareRecipient(transferBatch(want.Hex(), 25))
	if err != nil {
		t.Fatalf("sendShareRecipient() error = %v", err)
	}
	if got != want {
		t.Fatalf("recipient = %s, want %s", got.Hex(), want.Hex())
	}
}

func TestSendShareRecipientRejectsUnsupportedBatch(t *testing.T) {
	tests := []struct {
		name  string
		batch apiresp.CaliburSignedBatch
	}{
		{"no calls", apiresp.CaliburSignedBatch{}},
		{"zero amount", transferBatch("0x2222222222222222222222222222222222222222", 0)},
		{"native value", func() apiresp.CaliburSignedBatch {
			batch := transferBatch("0x2222222222222222222222222222222222222222", 1)
			batch.BatchedCall.Calls[0].Value = "1"
			return batch
		}()},
		{"wrong selector", func() apiresp.CaliburSignedBatch {
			batch := transferBatch("0x2222222222222222222222222222222222222222", 1)
			batch.BatchedCall.Calls[0].Data = "0x095ea7b3" + batch.BatchedCall.Calls[0].Data[10:]
			return batch
		}()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sendShareRecipient(tc.batch); err == nil {
				t.Fatal("expected unsupported batch to fail")
			}
		})
	}
}

func TestValidateSendShare(t *testing.T) {
	share := &apiresp.SendShareRequest{Permission: store.SharePermissionEmail, Note: "  hello  "}
	if err := validateSendShare(share); err != nil {
		t.Fatalf("validateSendShare() error = %v", err)
	}
	if share.Note != "hello" {
		t.Fatalf("note = %q, want trimmed note", share.Note)
	}

	for _, invalid := range []*apiresp.SendShareRequest{
		{Permission: 0, Note: "   "},
		{Permission: 8},
		{Note: strings.Repeat("ž", 501)},
	} {
		if err := validateSendShare(invalid); err == nil {
			t.Fatalf("expected invalid share %#v to fail", invalid)
		}
	}
}

func TestValidateShareProfileRequiresSelectedValues(t *testing.T) {
	owner := store.User{Email: "alice@example.com"}
	if err := validateShareProfile(apiresp.SendShareRequest{Permission: store.SharePermissionEmail}, owner); err != nil {
		t.Fatalf("email share failed: %v", err)
	}
	if err := validateShareProfile(apiresp.SendShareRequest{Permission: store.SharePermissionPhone}, owner); err == nil {
		t.Fatal("expected missing phone to fail")
	}
	if err := validateShareProfile(
		apiresp.SendShareRequest{Permission: store.SharePermissionEmail},
		store.User{Email: "pending+abc@clc.local"},
	); err == nil {
		t.Fatal("expected placeholder email to fail")
	}
	if err := validateShareProfile(apiresp.SendShareRequest{Permission: store.SharePermissionLocation}, owner); err == nil {
		t.Fatal("expected missing location to fail")
	}
}

func TestUserShareResultOmitsUnselectedContactFields(t *testing.T) {
	result := userShareResult(store.UserShareDetails{
		ID:              1,
		TrackingID:      "tracking",
		SharePermission: store.SharePermissionEmail,
		ExtraNote:       "hello",
		OwnerName:       "Alice",
		Email:           "alice@example.com",
		CreatedAt:       time.Unix(1, 0),
	})

	sharedBy, ok := result["sharedBy"].(map[string]any)
	if !ok {
		t.Fatalf("sharedBy = %#v", result["sharedBy"])
	}
	if sharedBy["email"] != "alice@example.com" {
		t.Fatalf("email = %#v", sharedBy["email"])
	}
	if _, exists := sharedBy["phone"]; exists {
		t.Fatal("unselected phone must not be serialized")
	}
	if _, exists := sharedBy["location"]; exists {
		t.Fatal("unselected location must not be serialized")
	}
}
