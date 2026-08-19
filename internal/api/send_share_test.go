package api

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	shareTestChainID = int64(42220)
)

func shareTestTransaction(t *testing.T, privateKeyHex string, to common.Address) *types.Transaction {
	t.Helper()
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(1),
		Gas:      100_000,
		To:       &to,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(shareTestChainID)), privateKey)
	if err != nil {
		t.Fatalf("sign test transaction: %v", err)
	}
	return signed
}

func shareTestAddress(t *testing.T, privateKeyHex string) common.Address {
	t.Helper()
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}
	return crypto.PubkeyToAddress(privateKey.PublicKey)
}

func shareTransferLog(token, owner, recipient common.Address, amount int64) *types.Log {
	return &types.Log{
		Address: token,
		Topics: []common.Hash{
			erc20TransferTopic,
			common.BytesToHash(common.LeftPadBytes(owner.Bytes(), 32)),
			common.BytesToHash(common.LeftPadBytes(recipient.Bytes(), 32)),
		},
		Data: common.LeftPadBytes(big.NewInt(amount).Bytes(), 32),
	}
}

func shareTestReceipt(tx *types.Transaction, logs ...*types.Log) *types.Receipt {
	return &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		TxHash:      tx.Hash(),
		BlockNumber: big.NewInt(100),
		Logs:        logs,
	}
}

func TestConfirmedSendRecipientAcceptsOwnGasTransfer(t *testing.T) {
	ownerKey := strings.Repeat("1", 64)
	owner := shareTestAddress(t, ownerKey)
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	want := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := shareTestTransaction(t, ownerKey, token)

	got, err := confirmedSendRecipient(
		tx,
		shareTestReceipt(tx, shareTransferLog(token, owner, want, 25)),
		tx.Hash(),
		shareTestChainID,
		owner,
	)
	if err != nil {
		t.Fatalf("confirmedSendRecipient() error = %v", err)
	}
	if got != want {
		t.Fatalf("recipient = %s, want %s", got.Hex(), want.Hex())
	}
}

func TestConfirmedSendRecipientAcceptsRelayedSmartAccountTransfer(t *testing.T) {
	owner := common.HexToAddress("0x3333333333333333333333333333333333333333")
	relayerKey := strings.Repeat("2", 64)
	recipient := common.HexToAddress("0x4444444444444444444444444444444444444444")
	token := common.HexToAddress("0x5555555555555555555555555555555555555555")
	tx := shareTestTransaction(t, relayerKey, owner)

	got, err := confirmedSendRecipient(
		tx,
		shareTestReceipt(tx, shareTransferLog(token, owner, recipient, 10)),
		tx.Hash(),
		shareTestChainID,
		owner,
	)
	if err != nil {
		t.Fatalf("confirmedSendRecipient() error = %v", err)
	}
	if got != recipient {
		t.Fatalf("recipient = %s, want %s", got.Hex(), recipient.Hex())
	}
}

func TestConfirmedSendRecipientRejectsUnconfirmedOrAmbiguousTransfers(t *testing.T) {
	ownerKey := strings.Repeat("3", 64)
	owner := shareTestAddress(t, ownerKey)
	token := common.HexToAddress("0x6666666666666666666666666666666666666666")
	recipient := common.HexToAddress("0x7777777777777777777777777777777777777777")
	secondRecipient := common.HexToAddress("0x8888888888888888888888888888888888888888")
	tx := shareTestTransaction(t, ownerKey, token)

	failed := shareTestReceipt(tx, shareTransferLog(token, owner, recipient, 10))
	failed.Status = types.ReceiptStatusFailed
	unrelatedTx := shareTestTransaction(t, strings.Repeat("4", 64), token)
	multiple := shareTestReceipt(tx,
		shareTransferLog(token, owner, recipient, 10),
		shareTransferLog(token, owner, secondRecipient, 5),
	)

	for name, tc := range map[string]struct {
		tx      *types.Transaction
		receipt *types.Receipt
	}{
		"missing receipt":      {tx: tx},
		"failed receipt":       {tx: tx, receipt: failed},
		"unrelated outer tx":   {tx: unrelatedTx, receipt: shareTestReceipt(unrelatedTx, shareTransferLog(token, owner, recipient, 10))},
		"no outgoing transfer": {tx: tx, receipt: shareTestReceipt(tx)},
		"multiple transfers":   {tx: tx, receipt: multiple},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := confirmedSendRecipient(tc.tx, tc.receipt, tc.tx.Hash(), shareTestChainID, owner); err == nil {
				t.Fatal("expected transaction to be unavailable for private sharing")
			}
		})
	}
}

func TestPostConfirmationShareRouteIsRegistered(t *testing.T) {
	a := newTestRouter(t)
	for _, route := range a.router.Router().Routes() {
		if route.Method == "POST" && route.Path == "/api/v2/users/me/shares/:txHash" {
			return
		}
	}
	t.Fatal("post-confirmation private-share route is not registered")
}

func TestValidateSendShare(t *testing.T) {
	share := &apiresp.CreateSendShareRequest{Permission: store.SharePermissionEmail, Note: "  hello  "}
	if err := validateSendShare(share); err != nil {
		t.Fatalf("validateSendShare() error = %v", err)
	}
	if share.Note != "hello" {
		t.Fatalf("note = %q, want trimmed note", share.Note)
	}

	for _, invalid := range []*apiresp.CreateSendShareRequest{
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
	if err := validateShareProfile(apiresp.CreateSendShareRequest{Note: "note only"}, store.User{}); err != nil {
		t.Fatalf("a note without PII permissions must not require profile data: %v", err)
	}

	owner := store.User{Email: "alice@example.com"}
	if err := validateShareProfile(apiresp.CreateSendShareRequest{Permission: store.SharePermissionEmail}, owner); err != nil {
		t.Fatalf("email share failed: %v", err)
	}
	if err := validateShareProfile(apiresp.CreateSendShareRequest{Permission: store.SharePermissionPhone}, owner); err == nil {
		t.Fatal("expected missing phone to fail")
	}
	if err := validateShareProfile(
		apiresp.CreateSendShareRequest{Permission: store.SharePermissionEmail},
		store.User{Email: "pending+abc@clc.local"},
	); err == nil {
		t.Fatal("expected placeholder email to fail")
	}
	if err := validateShareProfile(apiresp.CreateSendShareRequest{Permission: store.SharePermissionLocation}, owner); err == nil {
		t.Fatal("expected missing location to fail")
	}
}

func TestMatchesExistingShareRequiresExactRetry(t *testing.T) {
	req := apiresp.CreateSendShareRequest{
		Permission: store.SharePermissionEmail,
		Note:       "confirmed note",
	}
	existing := store.UserShareDetails{
		OwnerUserID:      1,
		AuthorizedUserID: 2,
		SharePermission:  req.Permission,
		ExtraNote:        req.Note,
	}
	if !matchesExistingShare(existing, 1, 2, req) {
		t.Fatal("identical receipt retry must match the existing attachment")
	}

	changed := req
	changed.Note = "replacement"
	if matchesExistingShare(existing, 1, 2, changed) {
		t.Fatal("changed private metadata must not replace the existing attachment")
	}
}

func TestUserShareResultOmitsUnselectedContactFields(t *testing.T) {
	lat := -1.286389
	lng := 36.817223
	result := userShareResult(store.UserShareDetails{
		ID:              1,
		SharePermission: store.SharePermissionEmail,
		ExtraNote:       "hello",
		OwnerName:       "Alice",
		Email:           "alice@example.com",
		Phone:           "+254700000000",
		LocationName:    "Nairobi",
		GeoLat:          &lat,
		GeoLng:          &lng,
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
