package api

import (
	"time"
)

type (
	OKResponse struct {
		Ok          bool           `json:"ok"`
		Description string         `json:"description"`
		Result      map[string]any `json:"result"`
	}

	ErrResponse struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
		ErrCode     string `json:"code"`
	}

	DeployPoolRequest struct {
		Name          string `json:"name" validate:"required"`
		Symbol        string `json:"symbol" validate:"required"`
		Decimals      string `json:"decimals" validate:"required"`
		Owner         string `json:"owner" validate:"required,eth_addr_checksum"`
		QuoterType    string `json:"quoterType" validate:"omitempty,oneof=decimal oracle relative"`
		FeesDecoupled bool   `json:"feesDecoupled"`
		// Where decoupled fees are paid. Defaults to the owner, which is what every
		// deploy did before the field existed; meaningful only with FeesDecoupled,
		// since coupled fees stay in the pool's own liquidity.
		FeeAddress       string   `json:"feeAddress,omitempty" validate:"omitempty,eth_addr_checksum"`
		FeePolicyDefault string   `json:"feePolicyDefault" validate:"required"`
		BaseCurrency     string   `json:"baseCurrency,omitempty" validate:"omitempty,eth_addr_checksum"`
		UnitOfAccount    string   `json:"unitOfAccount" validate:"required,max=16"`
		Description      string   `json:"description,omitempty"`
		Website          string   `json:"website,omitempty" validate:"omitempty,url"`
		Email            string   `json:"email,omitempty" validate:"omitempty,email"`
		Phone            string   `json:"phone,omitempty"`
		Terms            string   `json:"terms,omitempty" validate:"omitempty,max=5000"`
		LocationName     string   `json:"locationName,omitempty"`
		GeoLat           *float64 `json:"geoLat,omitempty" validate:"omitempty,min=-90,max=90"`
		GeoLng           *float64 `json:"geoLng,omitempty" validate:"omitempty,min=-180,max=180"`
		// http_url, not url: the latter accepts ftp:, data: and javascript:, and a
		// deploy is validated long before the catalog's HTTP-only constraint sees
		// the value — by which point the transaction has been signed and its nonce
		// spent, with MaxAttempts 1 and no retry.
		ImageURL  string   `json:"imageUrl,omitempty"  validate:"omitempty,http_url"`
		BannerURL string   `json:"bannerUrl,omitempty" validate:"omitempty,http_url"`
		Tags      []string `json:"tags,omitempty"`
	}

	// Terms is either a link to the publisher's terms or the terms themselves,
	// written as Markdown. The column is TEXT with no format constraint, so the
	// only rule is a size cap; clients tell the two apart by whether the value
	// parses as an http(s) URL.
	DeployTokenRequest struct {
		Name            string   `json:"name" validate:"required"`
		Symbol          string   `json:"symbol" validate:"required"`
		Decimals        string   `json:"decimals" validate:"required"`
		Owner           string   `json:"owner" validate:"required,eth_addr_checksum"`
		ExpiryTimestamp string   `json:"expiryTimestamp,omitempty" validate:"omitempty"`
		Description     string   `json:"description,omitempty"`
		Website         string   `json:"website,omitempty" validate:"omitempty,url"`
		Email           string   `json:"email,omitempty" validate:"omitempty,email"`
		Phone           string   `json:"phone,omitempty"`
		Terms           string   `json:"terms,omitempty" validate:"omitempty,max=5000"`
		VoucherUoa      string   `json:"voucherUoa,omitempty"`
		VoucherValue    *float64 `json:"voucherValue,omitempty" validate:"omitempty,gte=0"`
		LocationName    string   `json:"locationName,omitempty"`
		GeoLat          *float64 `json:"geoLat,omitempty" validate:"omitempty,min=-90,max=90"`
		GeoLng          *float64 `json:"geoLng,omitempty" validate:"omitempty,min=-180,max=180"`
		// Off-chain catalog metadata, carried through the deploy so a voucher
		// created with an icon still has one once its address exists. Never
		// written on-chain.
		//
		// http_url rather than url, for the reason DeployPoolRequest gives above:
		// deployed_tokens_image_urls_http would otherwise reject the metadata in the
		// worker, after the deploy transaction is already signed and broadcast.
		IconURL   string   `json:"iconUrl,omitempty"   validate:"omitempty,http_url"`
		BannerURL string   `json:"bannerUrl,omitempty" validate:"omitempty,http_url"`
		Tags      []string `json:"tags,omitempty"`
	}

	RegisterVersionRequest struct {
		Version               string `json:"version" validate:"required"`
		ContractType          string `json:"contractType" validate:"required,oneof=token pool"`
		ImplementationAddress string `json:"implementationAddress" validate:"required,eth_addr_checksum"`
	}

	UpgradeTokenRequest struct {
		Address string `json:"address" validate:"required,eth_addr_checksum"`
	}

	UpgradePoolRequest struct {
		Address string `json:"address" validate:"required,eth_addr_checksum"`
	}

	TrackOTXRequest struct {
		TrackingID string `param:"trackingId" validate:"required,uuid"`
	}

	CursorPaginationRequest struct {
		PerPage uint64 `query:"perPage"`
		Next    bool   `query:"next"`
		Cursor  uint64 `query:"cursor"`
	}

	OTXByAccountRequest struct {
		Address string `param:"address" validate:"required,eth_addr_checksum"`
		PerPage uint64 `query:"perPage"`
		Next    bool   `query:"next"`
		Cursor  uint64 `query:"cursor"`
	}

	AccountAddressParam struct {
		Address string `param:"address" validate:"required,eth_addr_checksum"`
	}

	PretiumTriggerOnrampRequest struct {
		Address     string  `json:"address" validate:"required,eth_addr_checksum"`
		PhoneNumber string  `json:"phoneNumber,omitempty"`
		Asset       string  `json:"asset" validate:"required,oneof=USDT USDC cUSD"`
		Amount      float64 `json:"amount" validate:"required,gte=20,lte=250000"`
	}

	PretiumLinkRequest struct {
		PublicKey   string `json:"publicKey" validate:"required,eth_addr_checksum"`
		PhoneNumber string `json:"phoneNumber" validate:"required"`
	}

	PhoneNumberParam struct {
		PhoneNumber string `param:"phoneNumber" validate:"required"`
	}

	// Auth request/response types

	SignupRequest struct {
		Message   string `json:"message" validate:"required"`
		Signature string `json:"signature" validate:"required"`
		Name      string `json:"name" validate:"required"`
		Email     string `json:"email" validate:"required,email"`
	}

	SigninRequest struct {
		Message   string `json:"message" validate:"required"`
		Signature string `json:"signature" validate:"required"`
	}

	// Admin request types

	UpdateUserStatusRequest struct {
		// Status is derived from the route; no request body is required.
	}

	UpdateUserLimitsRequest struct {
		MaxPools  int `json:"maxPools" validate:"gte=0"`
		MaxTokens int `json:"maxTokens" validate:"gte=0"`
	}

	// EthAddressParam is a path-parameter type for Ethereum addresses.
	// Address normalisation (lowercasing / checksum) is handled in the handler
	// layer, so checksum validation is intentionally omitted here.
	EthAddressParam struct {
		Address string `param:"address" validate:"required"`
	}

	TxHashParam struct {
		TxHash string `param:"txHash" validate:"required,len=66,startswith=0x,hexadecimal"`
	}

	// Relayer (EIP-7702 / Calibur) types.

	RelayerEOANonceParam struct {
		Address string `param:"address" validate:"required,eth_addr_checksum"`
	}

	CaliburNonceParam struct {
		Address string `param:"address" validate:"required,eth_addr_checksum"`
		KeyHash string `param:"keyHash" validate:"omitempty,len=66,startswith=0x,hexadecimal"`
	}

	EIP7702Authorization struct {
		ChainID         string `json:"chainId"         validate:"required,numeric"`
		ContractAddress string `json:"contractAddress" validate:"required,eth_addr_checksum"`
		Nonce           string `json:"nonce"           validate:"required,numeric"`
		YParity         uint8  `json:"yParity"         validate:"oneof=0 1"`
		R               string `json:"r"               validate:"required,len=66,startswith=0x"`
		S               string `json:"s"               validate:"required,len=66,startswith=0x"`
	}

	DelegateRequest struct {
		EphemeralAddress string               `json:"ephemeralAddress" validate:"required,eth_addr_checksum"`
		Authorization    EIP7702Authorization `json:"authorization"    validate:"required"`
	}

	CaliburCall struct {
		To    string `json:"to"    validate:"required,eth_addr_checksum"`
		Value string `json:"value" validate:"required,numeric"`
		Data  string `json:"data"  validate:"required,startswith=0x"`
	}

	CaliburBatchedCall struct {
		Calls           []CaliburCall `json:"calls"           validate:"required,min=1,max=16,dive"`
		RevertOnFailure bool          `json:"revertOnFailure"`
	}

	CaliburSignedBatch struct {
		BatchedCall CaliburBatchedCall `json:"batchedCall" validate:"required"`
		Nonce       string             `json:"nonce"       validate:"required,numeric"`
		KeyHash     string             `json:"keyHash"     validate:"required,len=66,startswith=0x,hexadecimal"`
		Executor    string             `json:"executor"    validate:"required,eth_addr_checksum"`
		Deadline    string             `json:"deadline"    validate:"required,numeric"`
	}

	CreateSendShareRequest struct {
		TxHash     string `param:"txHash" validate:"required,len=66,startswith=0x,hexadecimal"`
		Permission int    `json:"permission" validate:"gte=0,lte=7"`
		Note       string `json:"note,omitempty" validate:"omitempty,max=500"`
	}

	RelayRequest struct {
		UserAddress   string                `json:"userAddress"   validate:"required,eth_addr_checksum"`
		SignedBatch   CaliburSignedBatch    `json:"signedBatch"   validate:"required"`
		Signature     string                `json:"signature"     validate:"required,startswith=0x"`
		SignatureType string                `json:"signatureType" validate:"required,oneof=secp256k1 webauthn"`
		Authorization *EIP7702Authorization `json:"authorization,omitempty" validate:"omitempty"`
		// Registration-time fields (passed alongside the first secp256k1-signed
		// relay so the backend can persist the WebAuthn credential mapping).
		CredentialID string `json:"credentialId,omitempty" validate:"omitempty"`
		PublicKeyX   string `json:"publicKeyX,omitempty"   validate:"omitempty,numeric"`
		PublicKeyY   string `json:"publicKeyY,omitempty"   validate:"omitempty,numeric"`
	}

	// RelayPreviewRequest asks the relayer to resolve the Calibur nonce and
	// compute the EIP-712 digest for a batch, so the dapp can sign exactly
	// what the relayer will submit. No signature is involved.
	RelayPreviewRequest struct {
		UserAddress string             `json:"userAddress" validate:"required,eth_addr_checksum"`
		BatchedCall CaliburBatchedCall `json:"batchedCall" validate:"required"`
		KeyHash     string             `json:"keyHash"     validate:"omitempty,len=66,startswith=0x,hexadecimal"`
		Executor    string             `json:"executor"    validate:"required,eth_addr_checksum"`
		Deadline    string             `json:"deadline"    validate:"required,numeric"`
	}

	WebAuthnRecoverRequest struct {
		CredentialID      string `json:"credentialId"      validate:"required"`
		AuthenticatorData string `json:"authenticatorData" validate:"required"`
		ClientDataJSON    string `json:"clientDataJSON"    validate:"required"`
		Signature         string `json:"signature"         validate:"required"`
	}

	// SIWEVerifyRequest is the unified sign-in / sign-up request.
	// Both flows share the same endpoint; the server upserts the user.
	SIWEVerifyRequest struct {
		Message   string `json:"message"   validate:"required"`
		Signature string `json:"signature" validate:"required"`
	}

	// UpdatePoolProfileRequest carries a pool's editable off-chain record.
	//
	// Every field is optional in the JSON but assigned unconditionally by the
	// handler, so a caller sends the whole record and omitting a field clears
	// it. Nothing here mirrors SwapPool: name, symbol, decimals and the
	// dependency addresses all come from the chain and are not editable.
	UpdatePoolProfileRequest struct {
		Description   *string  `json:"description,omitempty"   validate:"omitempty,max=500"`
		UnitOfAccount *string  `json:"unitOfAccount,omitempty" validate:"omitempty,max=16"`
		Email         *string  `json:"email,omitempty"         validate:"omitempty,email"`
		Website       *string  `json:"website,omitempty"       validate:"omitempty,url"`
		Phone         *string  `json:"phone,omitempty"`
		Terms         *string  `json:"terms,omitempty"         validate:"omitempty,max=5000"`
		LocationName  *string  `json:"locationName,omitempty"  validate:"omitempty,max=200"`
		GeoLat        *float64 `json:"geoLat,omitempty"        validate:"omitempty,min=-90,max=90"`
		GeoLng        *float64 `json:"geoLng,omitempty"        validate:"omitempty,min=-180,max=180"`
		// Unlike every other field here, these two preserve on omission rather
		// than clearing: update-pool-profile COALESCEs them so a client that does
		// not send images cannot blank a pool's images. Send "" to clear one.
		// `http_url|eq=`: an http(s) URL to set, "" to clear. The alternation is
		// required because omitempty does not skip a pointer to an empty string.
		ImageURL  *string  `json:"imageUrl,omitempty"  validate:"omitempty,http_url|eq="`
		BannerURL *string  `json:"bannerUrl,omitempty" validate:"omitempty,http_url|eq="`
		Tags      []string `json:"tags,omitempty"`
	}

	// UpdateVoucherProfileRequest carries a voucher's editable off-chain record.
	//
	// Assign-all, exactly as UpdatePoolProfileRequest: every field is optional in
	// the JSON and written unconditionally by the handler, so a caller sends the
	// whole record and omitting a field clears it. Except for DisplayName,
	// nothing here mirrors the deployed ERC20 — contract name, symbol, decimals
	// and the owner all come from the chain and are not editable. Terms is a link
	// or the terms themselves as Markdown, as on DeployTokenRequest.
	UpdateVoucherProfileRequest struct {
		// DisplayName is an off-chain override for the deployed ERC20 name.
		// Omitted preserves the existing override; an empty string clears it and
		// makes catalog reads fall back to the immutable contract name.
		DisplayName  *string  `json:"displayName,omitempty"  validate:"omitempty,max=120"`
		Description  *string  `json:"description,omitempty"  validate:"omitempty,max=500"`
		VoucherUoa   *string  `json:"voucherUoa,omitempty"   validate:"omitempty,max=16"`
		VoucherValue *float64 `json:"voucherValue,omitempty" validate:"omitempty,gte=0"`
		Email        *string  `json:"email,omitempty"        validate:"omitempty,email"`
		Website      *string  `json:"website,omitempty"      validate:"omitempty,url"`
		Phone        *string  `json:"phone,omitempty"`
		Terms        *string  `json:"terms,omitempty"        validate:"omitempty,max=5000"`
		LocationName *string  `json:"locationName,omitempty" validate:"omitempty,max=200"`
		GeoLat       *float64 `json:"geoLat,omitempty"       validate:"omitempty,min=-90,max=90"`
		GeoLng       *float64 `json:"geoLng,omitempty"       validate:"omitempty,min=-180,max=180"`
		// These two preserve on omission rather than clearing, for the reason
		// UpdatePoolProfileRequest gives: update-voucher-profile COALESCEs them so
		// a client that sends no image fields cannot blank a voucher's images.
		// Send "" to clear one. `http_url|eq=` because omitempty does not skip a
		// pointer to an empty string, and vouchers_image_urls_http would reject
		// anything that is not http(s) at the database — after the request had
		// already been accepted.
		IconURL   *string  `json:"iconUrl,omitempty"   validate:"omitempty,http_url|eq="`
		BannerURL *string  `json:"bannerUrl,omitempty" validate:"omitempty,http_url|eq="`
		Tags      []string `json:"tags,omitempty"`
	}

	// UpdateVoucherDisplayNameRequest is the narrow owner-facing mutation for a
	// voucher's public label. Contract name, symbol, decimals and ownership stay
	// outside it because they are chain facts.
	UpdateVoucherDisplayNameRequest struct {
		DisplayName string `json:"displayName" validate:"required,max=120"`
	}

	// UpdateProfileRequest carries optional profile fields. Omitted location
	// values preserve stored data; ClearLocation explicitly removes the pin, and
	// ClearAvatar does the same for the profile image.
	UpdateProfileRequest struct {
		Name    string `json:"name"          validate:"omitempty,max=80"`
		Email   string `json:"email"         validate:"omitempty,email"`
		Phone   string `json:"phone"`
		Country string `json:"country"       validate:"omitempty,len=2"`
		// Profile preferences are pointers because clients predating them must be
		// able to update their contact details without clearing them. Website's
		// explicit empty-string alternative clears a saved value; `omitempty` does
		// not skip a pointer to an empty string.
		PreferredCurrency *string  `json:"preferredCurrency,omitempty" validate:"omitempty,len=3,uppercase,alpha"`
		Bio               *string  `json:"bio,omitempty"               validate:"omitempty,max=500"`
		Website           *string  `json:"website,omitempty"           validate:"omitempty,http_url|eq="`
		LocationName      *string  `json:"locationName,omitempty" validate:"omitempty,max=200"`
		GeoLat            *float64 `json:"geoLat,omitempty"       validate:"omitempty,min=-90,max=90"`
		GeoLng            *float64 `json:"geoLng,omitempty"       validate:"omitempty,min=-180,max=180"`
		ClearLocation     bool     `json:"clearLocation,omitempty"`
		// http_url, never url: the latter accepts ftp:, data: and javascript:,
		// which users_avatar_url_http would then reject at the database — after
		// the request had already been accepted.
		AvatarURL   *string `json:"avatarUrl,omitempty" validate:"omitempty,http_url"`
		ClearAvatar bool    `json:"clearAvatar,omitempty"`
	}

	// ReportVoucherRefRequest names a voucher by its stable public identity.
	// Database ids are intentionally absent: imports can remap them between
	// environments, while (chainId, address) survives a catalog transfer.
	ReportVoucherRefRequest struct {
		ChainID int64  `json:"chainId" validate:"required,gt=0"`
		Address string `json:"address" validate:"required"`
	}

	ReportImageRequest struct {
		URL       string  `json:"url"                 validate:"required"`
		Width     *int32  `json:"width,omitempty"     validate:"omitempty,gt=0"`
		Height    *int32  `json:"height,omitempty"    validate:"omitempty,gt=0"`
		MaxWidth  *int32  `json:"maxWidth,omitempty"  validate:"omitempty,gt=0"`
		MaxHeight *int32  `json:"maxHeight,omitempty" validate:"omitempty,gt=0"`
		ByteSize  *int64  `json:"byteSize,omitempty"  validate:"omitempty,gt=0"`
		Caption   *string `json:"caption,omitempty"   validate:"omitempty,max=500"`
	}

	ReportAttachmentRequest struct {
		Kind     string  `json:"kind"               validate:"required,oneof=FILE EMBED"`
		URL      string  `json:"url"                validate:"required"`
		Filename *string `json:"filename,omitempty" validate:"omitempty,max=255"`
		ByteSize *int64  `json:"byteSize,omitempty" validate:"omitempty,gt=0"`
	}

	// ReportWriteRequest is an assign-all post write. PUT replaces every
	// editable scalar and every relation, so an omitted optional field clears it
	// and an omitted collection becomes an empty set.
	ReportWriteRequest struct {
		Title        string                    `json:"title"       validate:"required,max=200"`
		Body         string                    `json:"body"        validate:"max=500000"`
		Description  string                    `json:"description" validate:"max=10000"`
		ImageURL     *string                   `json:"imageUrl,omitempty"`
		GeoLat       *float64                  `json:"geoLat,omitempty"   validate:"omitempty,min=-90,max=90"`
		GeoLng       *float64                  `json:"geoLng,omitempty"   validate:"omitempty,min=-180,max=180"`
		PeriodFrom   *time.Time                `json:"periodFrom,omitempty"`
		PeriodTo     *time.Time                `json:"periodTo,omitempty"`
		HostName     *string                   `json:"hostName,omitempty"     validate:"omitempty,max=200"`
		Participants *string                   `json:"participants,omitempty" validate:"omitempty,max=100"`
		TimeTaken    *string                   `json:"timeTaken,omitempty"    validate:"omitempty,max=100"`
		Tags         []string                  `json:"tags"        validate:"max=12,dive,required,max=100"`
		Vouchers     []ReportVoucherRefRequest `json:"vouchers"    validate:"max=12,dive"`
		Images       []ReportImageRequest      `json:"images"      validate:"max=50,dive"`
		Attachments  []ReportAttachmentRequest `json:"attachments" validate:"max=20,dive"`
	}

	// OfferingWriteRequest is the body of POST and assign-all PUT on an
	// offering.
	//
	// The wire keeps the vocabulary the read path has always used --
	// commodityName, commodityType, price -- so the item read, the mutation
	// read-back and the existing list projection are one representation. A
	// cleaner app-facing naming belongs to the shared TypeScript client, not to
	// a second server shape a previous PWA bundle would not recognise.
	//
	// Absent on purpose: voucherId and sellerAddress. The voucher comes from the
	// route and the seller is server-selected from the locked voucher owner, so
	// there is no field here for a caller to try.
	//
	// `price` is a string because PostgreSQL NUMERIC holds decimals a float64
	// silently rounds. The regexp admits plain decimal text only -- no exponent,
	// no thousands separator, no sign -- and the handler rejects zero, which the
	// pattern cannot express.
	OfferingWriteRequest struct {
		CommodityName string  `json:"commodityName" validate:"required,max=120"`
		CommodityType string  `json:"commodityType" validate:"required,oneof=GOOD SERVICE"`
		Description   string  `json:"description"   validate:"required,max=2000"`
		Unit          string  `json:"unit"          validate:"required,max=32"`
		Price         string  `json:"price"         validate:"required,max=64"`
		ImageURL      *string `json:"imageUrl,omitempty"`
	}
)

// OfferingUnits is the closed set a write may choose from.
//
// Duplicated from the offering_unit lookup that migrations/023_offering_crud.sql
// seeds, on purpose. The database is the backstop, but it cannot be the primary
// check: the unit foreign key is NOT VALID so imported free text survives, and a
// foreign key's update trigger short-circuits when the column is unchanged — so
// re-saving a row that already holds "50kg sack" would pass the constraint
// untouched. The API therefore has to be what closes the set. When either side
// moves, both move; TestOfferingUnitsMatchTheMigration fails if they drift.
var OfferingUnits = []string{
	"kg",
	"g",
	"litre",
	"item",
	"hour",
	"day",
	"bag",
	"unit",
	"bundle",
	"piece",
	"dozen",
	"serving",
	"trip",
	"session",
	"metre",
}

const (
	ErrCodeInternalServerError = "E01"
	ErrCodeInvalidJSON         = "E02"
	ErrCodeNotFound            = "E03"
	ErrCodeValidationFailed    = "E04"
	ErrCodeMethodNotAllowed    = "E05"
	ErrJWTAuth                 = "E06"
	ErrNoRecordFound           = "E07"
	ErrCodePayloadTooLarge     = "E08"
	ErrCodeRequestTimeout      = "E09"
	ErrCodeForbidden           = "E10"
	ErrCodeLimitExceeded       = "E11"

	ErrCodeRelayerDisabled            = "E12"
	ErrCodeAuthorizationRequired      = "E13"
	ErrCodeRelayerExecutorMismatch    = "E14"
	ErrCodeWebAuthnVerificationFailed = "E15"
	ErrCodeConflict                   = "E16"
	ErrCodeEmailConflict              = "E17"
	ErrCodePhoneConflict              = "E18"
	ErrCodeShareUnavailable           = "E19"
)
