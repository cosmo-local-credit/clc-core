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
		Name             string   `json:"name" validate:"required"`
		Symbol           string   `json:"symbol" validate:"required"`
		Decimals         string   `json:"decimals" validate:"required"`
		Owner            string   `json:"owner" validate:"required,eth_addr_checksum"`
		QuoterType       string   `json:"quoterType" validate:"omitempty,oneof=decimal oracle relative"`
		FeesDecoupled    bool     `json:"feesDecoupled"`
		FeePolicyDefault string   `json:"feePolicyDefault" validate:"required"`
		BaseCurrency     string   `json:"baseCurrency,omitempty" validate:"omitempty,eth_addr_checksum"`
		Description      string   `json:"description,omitempty"`
		Website          string   `json:"website,omitempty" validate:"omitempty,url"`
		Email            string   `json:"email,omitempty" validate:"omitempty,email"`
		Phone            string   `json:"phone,omitempty"`
		Terms            string   `json:"terms,omitempty" validate:"omitempty,url"`
		LocationName     string   `json:"locationName,omitempty"`
		GeoLat           *float64 `json:"geoLat,omitempty" validate:"omitempty,min=-90,max=90"`
		GeoLng           *float64 `json:"geoLng,omitempty" validate:"omitempty,min=-180,max=180"`
		ImageURL         string   `json:"imageUrl,omitempty" validate:"omitempty,url"`
		BannerURL        string   `json:"bannerUrl,omitempty" validate:"omitempty,url"`
		Tags             []string `json:"tags,omitempty"`
	}

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
		Terms           string   `json:"terms,omitempty" validate:"omitempty,url"`
		VoucherUoa      string   `json:"voucherUoa,omitempty"`
		VoucherValue    *float64 `json:"voucherValue,omitempty" validate:"omitempty,gte=0"`
		LocationName    string   `json:"locationName,omitempty"`
		GeoLat          *float64 `json:"geoLat,omitempty" validate:"omitempty,min=-90,max=90"`
		GeoLng          *float64 `json:"geoLng,omitempty" validate:"omitempty,min=-180,max=180"`
		Tags            []string `json:"tags,omitempty"`
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
		Terms         *string  `json:"terms,omitempty"         validate:"omitempty,url"`
		LocationName  *string  `json:"locationName,omitempty"  validate:"omitempty,max=200"`
		GeoLat        *float64 `json:"geoLat,omitempty"        validate:"omitempty,min=-90,max=90"`
		GeoLng        *float64 `json:"geoLng,omitempty"        validate:"omitempty,min=-180,max=180"`
		Tags          []string `json:"tags,omitempty"`
	}

	// UpdateProfileRequest carries optional profile fields.
	// Geo fields match DeployTokenRequest: optional pointers, range checks.
	// Both-or-neither is enforced in profileUpdateHandler (400 on a partial
	// pair) and backstopped by users_geo_valid at the DB boundary.
	UpdateProfileRequest struct {
		Name         string   `json:"name"         validate:"omitempty,max=80"`
		Email        string   `json:"email"        validate:"omitempty,email"`
		Phone        string   `json:"phone"`
		Country      string   `json:"country"      validate:"omitempty,len=2"`
		LocationName *string  `json:"locationName,omitempty" validate:"omitempty,max=200"`
		GeoLat       *float64 `json:"geoLat,omitempty"       validate:"omitempty,min=-90,max=90"`
		GeoLng       *float64 `json:"geoLng,omitempty"       validate:"omitempty,min=-180,max=180"`
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
)

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
)
