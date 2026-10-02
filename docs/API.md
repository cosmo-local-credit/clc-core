# CLC Core — API Reference

> **Version:** 2.0  
> **License:** [AGPL-3.0](../LICENSE)

---

## Table of Contents

1. [Overview](#1-overview)
2. [Authentication](#2-authentication)
   - [Token Types](#21-token-types)
   - [SIWE Flow Summary](#22-siwe-flow-summary)
   - [Service Token Usage](#23-service-token-usage)
3. [Response Format](#3-response-format)
   - [Success](#31-success)
   - [Error](#32-error)
   - [Error Codes](#33-error-codes)
4. [Public Auth Endpoints (`/auth/*`)](#4-public-auth-endpoints-auth)
   - [POST /auth/nonce](#41-post-authnonce)
   - [POST /auth/signup](#42-post-authsignup)
   - [POST /auth/signin](#43-post-authsignin)
   - [POST /auth/logout](#44-post-authlogout)
   - [POST /auth/siwe/nonce](#45-post-authsiwenonce)
   - [POST /auth/siwe/verify](#46-post-authsiweverify)
   - [GET /auth/session](#47-get-authsession)
   - [GET /auth/profile](#48-get-authprofile)
   - [POST /auth/profile](#49-post-authprofile)
   - [POST /auth/storage-token](#410-post-authstorage-token)
5. [Protected Endpoints (`/api/v2/*`)](#5-protected-endpoints-apiv2)
   - [GET /api/v2/system](#51-get-apiv2system)
   - [GET /api/v2/otx/track/:trackingId](#52-get-apiv2otxtracktrackingid)
   - [GET /api/v2/account/otx/:address](#53-get-apiv2accountotxaddress)
   - [GET /api/v2/account/status/:address](#54-get-apiv2accountstatusaddress)
   - [POST /api/v2/contracts/token](#55-post-apiv2contractstoken)
   - [POST /api/v2/contracts/pool](#56-post-apiv2contractspool)
   - [GET /api/v2/names](#57-get-apiv2names)
   - [Pretium Backend Endpoints (`/api/v2/pretium/*`)](#58-pretium-backend-endpoints-apiv2pretium)
   - [GET /api/v2/pools](#59-get-apiv2pools)
   - [GET /api/v2/pools/:address](#510-get-apiv2poolsaddress)
6. [User-Only Endpoints (`/api/v2/users/*`)](#6-user-only-endpoints-apiv2users)
   - [GET /api/v2/users/me](#61-get-apiv2usersme)
   - [GET /api/v2/users/me/tokens](#62-get-apiv2usersmetokens)
   - [GET /api/v2/users/me/pools](#63-get-apiv2usersmepools)
   - [GET /api/v2/users/me/pools/:address](#64-get-apiv2usersmepoolsaddress)
   - [GET /api/v2/users/me/deployments](#65-get-apiv2usersmedeployments)
7. [Superadmin Endpoints (`/api/v2/admin/*`)](#7-superadmin-endpoints-apiv2admin)
   - [GET /api/v2/admin/users](#71-get-apiv2adminusers)
   - [PUT /api/v2/admin/users/:address/approve](#72-put-apiv2adminusersaddressapprove)
   - [PUT /api/v2/admin/users/:address/reject](#73-put-apiv2adminusersaddressreject)
   - [PUT /api/v2/admin/users/:address/limits](#74-put-apiv2adminusersaddresslimits)
   - [PUT /api/v2/admin/users/:address/profile](#75-put-apiv2adminusersaddressprofile)
   - [GET /api/v2/admin/users/:address/pools](#76-get-apiv2adminusersaddresspools)
   - [GET /api/v2/admin/users/:address/stats](#77-get-apiv2adminusersaddressstats)
   - [GET /api/v2/admin/tokens](#78-get-apiv2admintokens)
   - [GET /api/v2/admin/pools](#79-get-apiv2adminpools)
   - [GET /api/v2/admin/deployments](#710-get-apiv2admindeployments)
   - [GET /api/v2/admin/dashboard/stats](#711-get-apiv2admindashboardstats)
8. [SIWE Message Construction](#8-siwe-message-construction)
9. [Public Market Catalog (`GET /api/v2/vouchers`)](#9-public-market-catalog-get-apiv2vouchers)
   - [9.6 Voucher detail (`GET /api/v2/vouchers/:address`)](#96-voucher-detail-get-apiv2vouchersaddress)
   - [9.7 Voucher offerings (`GET /api/v2/vouchers/:address/offerings`)](#97-voucher-offerings-get-apiv2vouchersaddressofferings)
10. [Pool offerings (`GET /api/v2/pools/:address/offerings`)](#10-pool-offerings-get-apiv2poolsaddressofferings)
11. [Reports / Posts (`/api/v2/reports`)](#11-reports--posts-apiv2reports)
   - [Feed (`GET /api/v2/reports`)](#111-feed-get-apiv2reports)
   - [Detail (`GET /api/v2/reports/:id`)](#112-detail-get-apiv2reportsid)
   - [Write contract](#113-write-contract)
   - [Create (`POST /api/v2/reports`)](#114-create-post-apiv2reports)
   - [Update (`PUT /api/v2/reports/:id`)](#115-update-put-apiv2reportsid)
   - [Approve (`PUT /api/v2/reports/:id/approve`)](#116-approve-put-apiv2reportsidapprove)
   - [Delete (`DELETE /api/v2/reports/:id`)](#117-delete-delete-apiv2reportsid)
12. [Shared vocabulary (`token_tag`, `offering_unit`)](#12-shared-vocabulary-token_tag-offering_unit)
   - [Reads](#121-reads)
   - [Creation is a side effect of the owning write](#122-creation-is-a-side-effect-of-the-owning-write)
   - [Tag filters are still closed](#123-tag-filters-are-still-closed)
   - [GET /api/v2/admin/vocabulary](#124-get-apiv2adminvocabulary)
   - [DELETE /api/v2/admin/vocabulary/tags and .../units](#125-delete-apiv2adminvocabularytags-and-units)
   - [PUT /api/v2/admin/vocabulary/tags and .../units](#126-put-apiv2adminvocabularytags-and-units)

---

## 1. Overview

| Property | Value |
|---|---|
| Base URL | `http(s)://<host>/` |
| Protected API prefix | `/api/v2` |
| Public `/api/v2` routes | [`GET /api/v2/vouchers`](#9-public-market-catalog-get-apiv2vouchers), [`GET /api/v2/vouchers/:address`](#96-voucher-detail-get-apiv2vouchersaddress), [`GET /api/v2/vouchers/:address/offerings`](#97-voucher-offerings-get-apiv2vouchersaddressofferings) |
| Auth mechanism | SIWE (Sign-In With Ethereum, EIP-4361) |
| Default chain | Celo Mainnet (`chainId: 42220`) |
| Content-Type | `application/json` |

---

## 2. Authentication

### 2.1 Token Types

Two mutually exclusive token types are accepted on protected routes.

#### User JWT (`__clc_auth` cookie)

Issued after a successful `/auth/signin` or `/auth/siwe/verify` for an approved user. Delivered and expected as an **HttpOnly cookie** named `__clc_auth`.

| Property | Value |
|---|---|
| Algorithm | EdDSA |
| Expiry | 30 days |
| Transport | `Set-Cookie: __clc_auth=<token>; HttpOnly` |

**Claims:**

| Claim | Type | Description |
|---|---|---|
| `userId` | `number` | Internal user identifier |
| `ethereumAddress` | `string` | Checksummed Ethereum address |
| `role` | `"USER"` \| `"SUPERADMIN"` | Authorization role |

#### Service JWT (`Authorization` header)

Long-lived tokens issued offline by the `gen-service-token` CLI tool. Intended for backend-to-backend integrations.

| Property | Value |
|---|---|
| Algorithm | EdDSA |
| Expiry | Long-lived (no hard expiry) |
| Transport | `Authorization: Bearer <token>` header **or** `__ge_cust_auth` cookie |

**Claims:**

| Claim | Type | Description |
|---|---|---|
| `publicKey` | `string` | Service identity key |
| `service` | `true` | Marks the token as a service credential |

> **Important:** Service JWTs bypass user quota checks but **cannot** access user-only routes (`/api/v2/users/*`). Those routes require a user JWT.

### 2.2 SIWE Flow Summary

```
1. POST /auth/nonce        → receive a one-time nonce (5-min TTL)
2. Client builds an EIP-4361 message embedding the nonce, then signs it
   with a connected wallet
3. Either:
   POST /auth/signup       → create a new PENDING account with profile fields
   POST /auth/signin       → authenticate an existing APPROVED account
   POST /auth/siwe/verify  → unified flow: create unknown addresses as PENDING,
                             authenticate APPROVED accounts
                             → server sets __clc_auth HttpOnly cookie on success
4. Include cookie automatically on all /api/v2/* requests
5. POST /auth/logout       → invalidates the cookie
```

### 2.3 Service Token Usage

Generate a service token using the bundled CLI:

```clc-core/cmd/gen-service-token/main.go#L1-1
go run ./cmd/gen-service-token -config config.toml -service my-service
```

Pass the token with every request:

```/dev/null/http.txt#L1-1
Authorization: Bearer <token>
```

Service tokens are intended for backend-to-backend flows such as contract
upgrade/version registration. `/api/v2/pretium/*` also accepts service tokens
for service callers, but browser clients may use normal user authentication.

---

## 3. Response Format

All responses are JSON objects. HTTP status codes are only used for top-level categories; use `ok` and `code` for programmatic branching.

### 3.1 Success

```/dev/null/success.json#L1-5
{
  "ok": true,
  "description": "Human-readable status message",
  "result": { ... }
}
```

### 3.2 Error

```/dev/null/error.json#L1-5
{
  "ok": false,
  "description": "Human-readable error message",
  "code": "E0X"
}
```

### 3.3 Error Codes

| Code | Meaning |
|---|---|
| `E01` | Internal server error |
| `E02` | Invalid or malformed JSON body |
| `E03` | Resource not found |
| `E04` | Validation failed |
| `E05` | Method not allowed |
| `E06` | JWT authentication failure |
| `E07` | No record found |
| `E08` | Payload too large |
| `E09` | Request timeout |
| `E10` | Forbidden (insufficient role or permissions) |
| `E11` | Rate or quota limit exceeded |
| `E12` | Relayer disabled |
| `E13` | EIP-7702 authorization required |
| `E14` | Relayer executor mismatch |
| `E15` | WebAuthn verification failed |
| `E16` | Address conflict |
| `E17` | Email conflict |
| `E18` | Phone conflict |
| `E19` | Private share or confirmed send is unavailable |

---

## 4. Public Auth Endpoints (`/auth/*`)

These endpoints require **no JWT**. They handle the SIWE authentication lifecycle.

---

### 4.1 `POST /auth/nonce`

Generates a one-time 32-character hex nonce to embed in a SIWE message. The nonce expires after **5 minutes** and is invalidated once consumed by a signup or signin attempt.

**Request body:** none

**Response `200`:**

```/dev/null/nonce-response.json#L1-7
{
  "ok": true,
  "description": "Nonce generated for SIWE challenge",
  "result": {
    "nonce": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
  }
}
```

---

### 4.2 `POST /auth/signup`

Registers a new Ethereum address. The client constructs and signs an EIP-4361 message client-side before submitting here.

**Verification steps performed by the server:**
1. Recovers the signing address from the SIWE message and signature.
2. Confirms the recovered address matches the `address` field in the message.
3. Validates the nonce is active and consumes it (one-time use).
4. Returns `409 Conflict` if the address is already registered.

**Account defaults:**
- Status starts as `PENDING` and requires approval before the account can sign in.
- `/auth/signup` does not auto-approve on the request path.
- Legacy federation auto-approval is triggered asynchronously by `/auth/siwe/verify` for pending users when the address exists in `sarafu_network.accounts.blockchain_address` on the configured federation database.
- New signups receive role `USER`. Superadmin role is granted by an operator updating records outside the public signup flow.

**Request body:**

```/dev/null/signup-request.json#L1-6
{
  "message":   "<EIP-4361 SIWE message string>",
  "signature": "0x<65-byte hex signature>",
  "name":      "Alice",
  "email":     "alice@example.com"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `message` | `string` | Yes | Full EIP-4361 plaintext message |
| `signature` | `string` | Yes | `0x`-prefixed 65-byte hex ECDSA signature |
| `name` | `string` | Yes | Display name for the account |
| `email` | `string` | Yes | Contact email address |

**Response `200`:**

```/dev/null/signup-response.json#L1-8
{
  "ok": true,
  "description": "Registration successful",
  "result": {
    "userId": 123,
    "status": "PENDING",
    "role":   "USER"
  }
}
```

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `409` | `E04` | Ethereum address already registered |
| `400` | `E02` | Malformed request body |
| `400` | `E04` | Invalid or expired nonce, signature mismatch |

---

### 4.3 `POST /auth/signin`

Authenticates an existing registered address via SIWE. On success, sets the `__clc_auth` HttpOnly JWT cookie (30-day expiry).

**Request body:**

```/dev/null/signin-request.json#L1-4
{
  "message":   "<EIP-4361 SIWE message string>",
  "signature": "0x<65-byte hex signature>"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `message` | `string` | Yes | Full EIP-4361 plaintext message |
| `signature` | `string` | Yes | `0x`-prefixed 65-byte hex ECDSA signature |

**Response `200`:**

Sets `Set-Cookie: __clc_auth=<jwt>; HttpOnly; Path=/; Max-Age=2592000`.

```/dev/null/signin-response.json#L1-8
{
  "ok": true,
  "description": "Signed in successfully",
  "result": {
    "userId": 123,
    "role":   "USER"
  }
}
```

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `404` | `E03` | Ethereum address not registered |
| `403` | `E10` | Account status is `PENDING` or `REJECTED` |
| `400` | `E04` | Invalid or expired nonce, signature mismatch |

---

### 4.4 `POST /auth/logout`

Clears the `__clc_auth` session cookie. Safe to call even when not authenticated.

**Request body:** none

**Response `200`:**

```/dev/null/logout-response.json#L1-6
{
  "ok": true,
  "description": "Signed out successfully",
  "result": {}
}
```

---

### 4.5 `POST /auth/siwe/nonce`

Alias for `POST /auth/nonce`. Generates a one-time SIWE nonce for clients that expect SIWE routes under `/auth/siwe/*`.

**Request body:** none

**Response `200`:** same shape as [`POST /auth/nonce`](#41-post-authnonce).

---

### 4.6 `POST /auth/siwe/verify`

Unified SIWE sign-in / registration endpoint. The server verifies the SIWE signature and nonce, then:

- If the address already exists and is `APPROVED`, issues a JWT cookie and returns a token in the response body.
- If the address already exists and is `PENDING` or `REJECTED`, returns `403`.
- If the address is unknown, inserts a `PENDING` user record, queues a background legacy-federation approval check, and returns `403` until approval completes.

The legacy check is performed asynchronously by a River worker against `sarafu_network.accounts.blockchain_address` on the configured federation database, so this route does not block on the old database.

**Request body:**

```/dev/null/siwe-verify-request.json#L1-4
{
  "message":   "<EIP-4361 SIWE message string>",
  "signature": "0x<65-byte hex signature>"
}
```

**Response `200` — approved user:**

```/dev/null/siwe-verify-response.json#L1-10
{
  "ok": true,
  "description": "Authenticated successfully",
  "result": {
    "token": "<jwt>",
    "address": "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
    "expiresAt": "2026-07-17T12:00:00Z"
  }
}
```

Also sets `Set-Cookie: __clc_auth=<jwt>; HttpOnly; SameSite=Strict; Path=/; Expires=<30 days>`.

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `403` | `E10` | Account is newly registered as `PENDING`, still pending approval, or rejected |
| `400` | `E04` | Invalid or expired nonce |
| `401` | `E04` | Signature verification failed or signer/address mismatch |

---

### 4.7 `GET /auth/session`

Returns the active session address and chain ID. Designed for use by wallet SDKs (e.g. Reown AppKit) that need to detect existing session state on page load.

**Request body:** none

**Response `200` — authenticated:**

```/dev/null/session-response.json#L1-4
{
  "address": "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
  "chainId": 42220
}
```

**Response `204 No Content` — no active session.**

No body is returned. Use this to redirect the user to a sign-in flow.

---

### 4.8 `GET /auth/profile`

Returns the authenticated user's editable contact profile. A user JWT cookie is
required. Location fields and `avatarUrl` are nullable:

```json
{
  "ok": true,
  "result": {
    "address": "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
    "name": "Alice",
    "email": "alice@example.com",
    "phone": "+254700000000",
    "country": "KE",
    "locationName": "Nairobi, Kenya",
    "geoLat": -1.286389,
    "geoLng": 36.817223,
    "avatarUrl": "https://cdn.cosmolocal.credit/profile/ab12_400_a3f91c2e.webp",
    "createdAt": "2026-08-04T12:00:00Z"
  }
}
```

### 4.9 `POST /auth/profile`

Updates contact fields and, optionally, the saved location pin and profile image.
Omitted location fields preserve their current values. Send `clearLocation: true`
to remove the label and coordinates together. Supplying `clearLocation` with any
location field is rejected.

```json
{
  "name": "Alice",
  "email": "alice@example.com",
  "phone": "+254700000000",
  "country": "KE",
  "locationName": "Nairobi, Kenya",
  "geoLat": -1.286389,
  "geoLng": 36.817223,
  "avatarUrl": "https://cdn.cosmolocal.credit/profile/ab12_400_a3f91c2e.webp"
}
```

`geoLat` and `geoLng` must be supplied together and must be valid latitude and
longitude values. A blank `locationName` is rejected.

`avatarUrl` behaves like the location fields: omitted preserves the stored image,
and `clearAvatar: true` removes it. Sending both is rejected. The URL must be
`http` or `https` — `users_avatar_url_http` refuses anything else at the database,
so the request is refused first. Upload the image to the storage service (§4.10)
and send the URL it returns; bytes never reach this endpoint.

The response carries the saved row, `avatarUrl` included, so a client can seed its
cache instead of re-reading the profile.

### 4.10 `POST /auth/storage-token`

Returns a readable copy of the caller's own session token, for uploading images to
the storage service.

Requires an authenticated user — the session cookie, or the same user JWT as a
bearer token. A service token is rejected with `403`: it has no browser to hand a
token to, and it already holds its own credential.

The storage service accepts an ordinary `clc-core` user JWT in an `Authorization`
header. It reads no cookies and is served from another host, so the session cookie
can neither be read by JavaScript nor be sent there. This endpoint bridges that
gap, and it is needed even though `POST /auth/siwe/verify` already returns a token:
the cookie survives a page reload and an in-memory token does not, so a signed-in
user must be able to ask for one again.

**Request body:** none

**Response `200`:**

```json
{
  "ok": true,
  "description": "Storage token issued",
  "result": {
    "token": "eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9...",
    "expiresAt": "2026-09-19T10:00:00Z"
  }
}
```

The response carries `Cache-Control: no-store`.

**This is the full session token, not a scoped one.** It is echoed, never
re-minted — minting here would issue a fresh 30-day token on every upload and roll
the session forward indefinitely — so two calls in a row return the same string and
the same `expiresAt`.

Because it is the session itself, and because `flexibleAuthMiddleware` accepts a
user JWT as a bearer token as well as in the cookie, a leaked copy is API access as
that user until it expires; signing out clears the cookie but there is no token
blocklist. Clients must therefore keep it in memory only — never `localStorage`,
`sessionStorage`, a persisted cache, or a server-rendered payload — and drop it at
every identity boundary. If that trade needs reversing, this endpoint is where a
short-lived, audience-scoped token would be minted instead, with no client change.

---

## 5. Protected Endpoints (`/api/v2/*`)

> The three public voucher catalog routes are the only exceptions to the
> `/api/v2` prefix being protected. They are not part of this section — see
> [section 9](#9-public-market-catalog-get-apiv2vouchers). Every statement
> below applies unchanged to all other `/api/v2` routes.

All routes in this section require **one** of:

- `__clc_auth` cookie containing a valid user JWT, **or**
- `Authorization: Bearer <token>` header containing a valid service JWT.

A missing or invalid credential returns:

```/dev/null/auth-error.json#L1-5
{
  "ok": false,
  "description": "Authentication required",
  "code": "E06"
}
```

---

### 5.1 `GET /api/v2/system`

Returns the server's signer public key and current build version. Useful for verifying the identity of the signing node.

**Auth:** user JWT or service JWT

**Response `200`:**

```/dev/null/system-response.json#L1-8
{
  "ok": true,
  "description": "Current system information",
  "result": {
    "systemSigner": "0x4a9f1234...",
    "build":        "v1.2.3"
  }
}
```

---

### 5.2 `GET /api/v2/otx/track/:trackingId`

Tracks an on-chain transaction (OTX) by its UUID. Returns the full transaction chain associated with the tracking ID.

**Auth:** user JWT or service JWT

**Access control:**
- Regular users (`USER` role) may only query tracking IDs that belong to their own account.
- Service JWTs may query any tracking ID.
- `SUPERADMIN` users may query any tracking ID.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `trackingId` | `string` (UUID) | The tracking UUID returned at deploy/operation time |

**Response `200`:**

```/dev/null/otx-track-response.json#L1-17
{
  "ok": true,
  "description": "OTX chain for tracking ID f47ac10b-...",
  "result": {
    "otx": [
      {
        "id":         1,
        "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "otxType":    "TOKEN_PROXY_DEPLOY",
        "txHash":     "0xabc123...",
        "nonce":      5,
        "replaced":   false,
        "createdAt":  "2024-01-15T10:30:00Z"
      }
    ]
  }
}
```

**OTX object fields:**

| Field | Type | Description |
|---|---|---|
| `id` | `int` | Internal OTX record ID |
| `trackingId` | `string` (UUID) | Tracking UUID |
| `otxType` | `string` | Operation type (e.g. `TOKEN_PROXY_DEPLOY`) |
| `txHash` | `string` | On-chain transaction hash (`0x...`) |
| `nonce` | `int` | Account nonce at submission time |
| `replaced` | `bool` | Whether this tx was replaced by a later one |
| `createdAt` | `string` (ISO 8601) | Submission timestamp |

---

### 5.3 `GET /api/v2/account/otx/:address`

Returns a paginated list of OTXs for a custodial account address.

**Auth:** user JWT or service JWT

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Checksummed Ethereum address |

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, paginates forward from `cursor` |
| `cursor` | `int` | — | Last seen OTX `id` from the previous page |

**Response `200`:**

```/dev/null/account-otx-response.json#L1-12
{
  "ok": true,
  "description": "Successfully fetched OTX for 0xd8dA6BF2...",
  "result": {
    "otx":   [ { "id": 1, "trackingId": "...", "otxType": "...", "txHash": "0x...", "nonce": 1, "replaced": false, "createdAt": "..." } ],
    "first": 1,
    "last":  50
  }
}
```

| Field | Type | Description |
|---|---|---|
| `otx` | `array` | Page of OTX objects (see [§5.2](#52-get-apiv2otxtracktrackingid) for field definitions) |
| `first` | `int` | Lowest OTX `id` in this page |
| `last` | `int` | Highest OTX `id` in this page — use as `cursor` for the next page |

---

### 5.4 `GET /api/v2/account/status/:address`

Returns on-chain and internal state for a custodial account.

**Auth:** user JWT or service JWT

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Checksummed Ethereum address |

**Response `200`:**

```/dev/null/account-status-response.json#L1-10
{
  "ok": true,
  "description": "Custodial account status",
  "result": {
    "gasBalance":    "1000000000000000000",
    "networkNonce":  4,
    "internalNonce": 5,
    "active":        true
  }
}
```

| Field | Type | Description |
|---|---|---|
| `gasBalance` | `string` (wei) | Native gas token balance as a decimal string |
| `networkNonce` | `int` | Last confirmed nonce on-chain |
| `internalNonce` | `int` | Next nonce tracked internally (may be ahead of network) |
| `active` | `bool` | Whether the account is active and usable |

---

### 5.5 `POST /api/v2/contracts/token`

Submits a request to deploy an ERC-20 token contract. The deployment is queued and processed asynchronously; use the returned `trackingId` with [`GET /api/v2/otx/track/:trackingId`](#52-get-apiv2otxtracktrackingid) to monitor progress.

**Auth:** user JWT or service JWT

**Quota:** Regular `USER` accounts are limited to `maxTokens` deployments (default: **20**). `SUPERADMIN` users and service JWTs are exempt.

**Request body:**

```/dev/null/token-deploy-request.json#L1-8
{
  "name":            "My Token",
  "symbol":          "MTK",
  "decimals":        "6",
  "owner":           "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
  "expiryTimestamp": "2026-01-01T00:00:00Z"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | Yes | Full token name, up to 120 characters. Must be unique — see below |
| `symbol` | `string` | Yes | Ticker symbol (e.g. `MTK`). Not required to be unique |
| `decimals` | `string` (int) | Yes | Token decimal places (e.g. `"6"`) |
| `owner` | `string` | Yes | Checksummed Ethereum address of the token owner |
| `expiryTimestamp` | `string` (ISO 8601) | No | Optional expiry date for the token |
| `iconUrl` | `string` (http/https URL) | No | Off-chain catalog image, carried to the voucher on confirmation |
| `bannerUrl` | `string` (http/https URL) | No | Off-chain catalog header image, same path as `iconUrl` |

Image URLs must be `http` or `https`. Other schemes — including `data:` — are
rejected with `400` before the deploy is queued, because the catalog column that
finally receives them is HTTP-only and by then the transaction would already be
signed and its nonce spent.

`name` is trimmed and its inner whitespace collapsed before anything else
happens, and the collapsed form is what reaches the ERC-20 constructor. It must
not already be in use by another voucher, whether by a catalog row or by a
deploy still in flight. The comparison ignores case and whitespace, so
`"  community   MARKET "` collides with `"Community Market"`. A deploy that
previously failed releases its name. Use
[`GET /api/v2/names`](#57-get-apiv2names) to check before submitting.

**Response `200`:**

```/dev/null/token-deploy-response.json#L1-7
{
  "ok": true,
  "description": "Token deploy request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
  }
}
```

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `400` | `E04` | Validation failure (missing fields, bad address, name over 120 characters or blank after trimming) |
| `403` | `E11` | User has reached their `maxTokens` quota |
| `409` | `E16` | `name` is already used by another voucher or an in-flight deploy |

---

### 5.6 `POST /api/v2/contracts/pool`

Submits a request to deploy a SwapPool instance. Like token deployments, processing is asynchronous.

**Auth:** user JWT or service JWT

**Quota:** Regular `USER` accounts are limited to `maxPools` deployments (default: **10**). `SUPERADMIN` users and service JWTs are exempt.

**Request body:**

```/dev/null/pool-deploy-request.json#L1-10
{
  "name":             "My Pool",
  "symbol":           "MYP",
  "decimals":         "6",
  "owner":            "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
  "quoterType":       "relative",
  "feesDecoupled":    false,
  "feePolicyDefault": "",
  "baseCurrency":     "",
  "unitOfAccount":    "KES"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | Yes | Pool name, up to 80 characters. Unique among pools, on the same terms as a token name |
| `symbol` | `string` | Yes | Pool symbol. Not required to be unique |
| `decimals` | `string` (int) | Yes | Decimal precision |
| `owner` | `string` | Yes | Checksummed owner address |
| `quoterType` | `string` | Yes | Pricing strategy — see below |
| `feesDecoupled` | `bool` | No | If `true`, fee policy executes independently of swaps (default: `false`) |
| `feePolicyDefault` | `string` | No | Default fee policy identifier |
| `baseCurrency` | `string` | Conditional | Checksummed non-zero token address; **required** when `quoterType` is `"oracle"` |
| `unitOfAccount` | `string` | Yes | ISO 4217 currency code (or another supported unit) used to describe pool values |
| `terms` | `string` | No | HTTP(S) link or Markdown terms, up to 5,000 characters |
| `imageUrl` | `string` (http/https URL) | No | Optional pool image URL |
| `bannerUrl` | `string` | No | Optional pool banner image URL |

**`quoterType` values:**

| Value | Description |
|---|---|
| `"decimal"` | Equal-value pricing adjusted only for token decimals |
| `"oracle"` | Price sourced from an on-chain oracle; `baseCurrency` must be set |
| `"relative"` | Per-voucher rates relative to a common unit *(default)* |

**Response `200`:**

```/dev/null/pool-deploy-response.json#L1-7
{
  "ok": true,
  "description": "Pool deploy request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
  }
}
```

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `400` | `E04` | Validation failure (missing fields, `baseCurrency` absent for oracle quoter, name over 80 characters or blank after trimming) |
| `403` | `E11` | User has reached their `maxPools` quota |
| `409` | `E16` | `name` is already used by another pool or an in-flight deploy |

---

### 5.7 `GET /api/v2/names`

Reports whether a voucher or pool name is still free, so a creation form can say
so before the user has filled in everything else.

This is an advisory read, not a reservation. Nothing is held: the deploy request
is the claim, and it re-runs the same check. Two clients told a name is free
moments apart can still race, and the loser gets `409` from the deploy.

**Auth:** user JWT or service JWT

**Query parameters:**

| Name | Required | Description |
|---|---|---|
| `kind` | Yes | `voucher` or `pool`. The two are separate namespaces |
| `name` | Yes | The name to check. Trimmed and inner-whitespace-collapsed before comparison; at most 120 characters for a voucher, 80 for a pool |
| `exclude` | No | A contract address whose own name should not count as a conflict, for a rename form checking the value already in its field |

A name is occupied by a catalog row or by a deploy that may still land on-chain
(`PENDING` or `CONFIRMED`). A deploy that `FAILED` releases its name. Where a
voucher or pool carries an off-chain display name, that label is what is
compared, not the immutable contract name it overrides.

**Response `200`:**

```/dev/null/name-availability-response.json#L1-9
{
  "ok": true,
  "description": "Name availability",
  "result": {
    "kind": "voucher",
    "name": "Community Market",
    "available": false
  }
}
```

`result.name` echoes the normalized form the comparison used, so a client can
show the user exactly what was checked. Responses are sent `Cache-Control: no-store`.

**Error cases:**

| HTTP | `code` | Condition |
|---|---|---|
| `400` | `E04` | `kind` is not `voucher` or `pool`; `name` is blank or over the limit for that kind; `exclude` is not an address |
| `401` | `E06` | Missing or invalid credentials |

---

### 5.8 `Pretium Backend Endpoints (/api/v2/pretium/*)`

These routes proxy the configured Pretium Ramp service for cash in/out flows.

**Auth:** user JWT cookie or service JWT

**Mount condition:** the route group is only registered when
`pretium.base_url` is configured.

**Config:**

```toml
[pretium]
base_url = "https://pretium.example.com/api/v1"
```

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v2/pretium/trigger-onramp` | Initiate fiat-to-crypto onramp |
| `POST` | `/api/v2/pretium/link` | Link a wallet address to a phone number (re-links on phone change) |
| `GET` | `/api/v2/pretium/link/:phoneNumber` | Fetch wallet links for a phone number |
| `GET` | `/api/v2/pretium/link-by-address/:address` | Fetch the active link (phone) for a wallet address |
| `GET` | `/api/v2/pretium/transactions/:phoneNumber` | Fetch transactions for a phone number |
| `GET` | `/api/v2/pretium/transactions-by-address/:address` | Fetch transactions for an address |
| `GET` | `/api/v2/pretium/transactions-recent` | Fetch recent transactions |
| `GET` | `/api/v2/pretium/rates` | Fetch current Pretium rates |

**Behaviour:**
- Success responses are passed through from the upstream Pretium service.
- Upstream `5xx` responses are surfaced as `502 Bad Gateway`.
- Local validation failures still use this API's standard error envelope.

See [PRETIUM.md](./PRETIUM.md) for request bodies and integration notes.

---

### 5.9 `GET /api/v2/pools`

Returns **all confirmed** (on-chain) SwapPool contracts across all users — the global pools catalog for the app. Cursor pagination matches activity and `/users/me/pools`.

**Auth:** user JWT cookie or service JWT

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, page toward older records from `cursor` (load more) |
| `cursor` | `int` | — | Use `last` with `next=true` for the next older page, or `first` without `next` to move back toward newer records |
| `q` | `string` | — | Pool name/symbol search; normalized length must be `2`–`64` characters |
| `tags` | `string` or repeated | — | Comma-separated pool tags; at most `12` distinct values |
| `tagMatch` | `any` \| `all` | `any` | Whether a pool must carry any or all selected tags |
| `vouchers` | `string` or repeated | — | Comma-separated stable voucher references (`chainId:0xAddress`); at most `12` |
| `voucherMatch` | `any` \| `all` | `any` | Whether a pool must contain any or all selected vouchers |
| `author` | `string` | — | Only reports authored by this address. Compared case-insensitively; a malformed address is a `400` |
| `facets` | `bool` | `false` | With `true` on the first page, include tag and voucher facet counts |

Voucher membership uses allowed `pool_tokens` rows, matching the pool voucher
list and `voucherCount`; a historical disallowed row does not satisfy the
filter. Voucher references are resolved by `(chainId, address)` rather than by
environment-local numeric IDs. An invalid or unknown reference rejects the
whole filter with `400` instead of silently broadening it.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Confirmed pools",
  "result": {
    "pools": [
      {
        "id": 51,
        "address": "0xAbCd5678...",
        "name": "My Pool",
        "symbol": "MYP",
        "description": "...",
        "tags": ["Market"],
        "imageUrl": null,
        "tokenRegistryAddress": "0x...",
        "feePolicyAddress": "0x...",
        "quoterAddress": "0x...",
        "feesDecoupled": false,
        "locationName": null,
        "createdAt": "2024-01-15T10:30:00Z",
        "updatedAt": "2024-01-15T10:30:00Z"
      }
    ],
    "first": 51,
    "last": 44
  }
}
```

| Field | Type | Description |
|---|---|---|
| `pools` | `array` | Current page of confirmed pools (`[]` when empty) |
| `first` | `int` | Highest record ID in the current page |
| `last` | `int` | Lowest record ID in the current page — use as `cursor` with `next=true` |

When `facets=true` is requested on the first page, `result.tagFacets` contains
`{tag, count}` rows and `result.voucherFacets` contains
`{id, chainId, address, name, symbol, count}` rows. Each facet retains the
search and the other facet dimension while omitting its own selected reducer,
so the counts describe options the user can add. Facet keys are absent from
cursor pages.

**Note:** Prefer this endpoint for the Pools tab browse list. `GET /api/v2/users/me/pools` remains the authenticated user's own deployed pools only.

---

### 5.10 `GET /api/v2/pools/:address`

Returns a single confirmed SwapPool by contract address. Address match is case-insensitive. Any authenticated client may load any confirmed pool.

**Auth:** user JWT cookie or service JWT

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Pool contract address |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Pool at address 0xAbCd5678...",
  "result": {
    "pool": {
      "id": 51,
      "address": "0xAbCd5678...",
      "name": "My Pool",
      "symbol": "MYP",
      "description": "...",
      "tags": ["Market"],
      "imageUrl": null,
      "tokenRegistryAddress": "0x...",
      "feePolicyAddress": "0x...",
      "quoterAddress": "0x...",
      "feesDecoupled": false,
      "locationName": null,
      "createdAt": "2024-01-15T10:30:00Z",
      "updatedAt": "2024-01-15T10:30:00Z"
    }
  }
}
```

---

## 6. User-Only Endpoints (`/api/v2/users/*`)

These endpoints require a valid **user JWT** (`__clc_auth` cookie). Service JWTs are rejected with `403 Forbidden`.

`GET /api/v2/users/me/reports` and `GET /api/v2/users/me/reports/:id` are on this
group too and are documented with the rest of Posts, in [§11.8](#118-an-authors-own-posts).

---

### 6.1 `GET /api/v2/users/me`

Returns the full profile of the currently authenticated user.

**Response `200`:**

```/dev/null/me-response.json#L1-16
{
  "ok": true,
  "description": "Current user profile",
  "result": {
    "user": {
      "id":              "f47ac10b-58cc-4372-a567-0e02b2c3d479",
      "ethereumAddress": "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
      "name":            "Alice",
      "email":           "alice@example.com",
      "role":            "USER",
      "status":          "APPROVED",
      "maxPools":        10,
      "maxTokens":       20
    }
  }
}
```

| Field | Type | Description |
|---|---|---|
| `id` | `string` (UUID) | Internal user ID |
| `ethereumAddress` | `string` | Checksummed Ethereum address |
| `name` | `string` | Display name |
| `email` | `string` | Email address |
| `role` | `"USER"` \| `"SUPERADMIN"` | Authorization role |
| `status` | `"PENDING"` \| `"APPROVED"` \| `"REJECTED"` | Account approval status |
| `maxPools` | `int` | Maximum number of pools this user may deploy |
| `maxTokens` | `int` | Maximum number of tokens this user may deploy |

---

### 6.2 `GET /api/v2/users/me/tokens`

Returns **confirmed** (on-chain) token contracts deployed by the authenticated user using cursor pagination.

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, page toward newer records from `cursor` |
| `cursor` | `int` | — | Use `last` to fetch older records or `first` with `next=true` to move back toward newer records |

**Response `200`:**

```/dev/null/me-tokens-response.json#L1-18
{
  "ok": true,
  "description": "Confirmed tokens for user f47ac10b-...",
  "result": {
    "tokens": [
      {
        "id":      42,
        "address": "0xAbCd1234...",
        "symbol":  "MTK",
        "name":    "My Token",
        "admin":   "0xd8dA6BF2...",
        "createdAt": "2024-01-15T10:30:00Z"
      }
    ],
    "first": 42,
    "last": 33
  }
}
```

| Field | Type | Description |
|---|---|---|
| `tokens` | `array` | Current page of deployed tokens |
| `first` | `int` | Highest record ID in the current page |
| `last` | `int` | Lowest record ID in the current page |

---

### 6.3 `GET /api/v2/users/me/pools`

Returns **confirmed** (on-chain) SwapPool contracts deployed by the authenticated user using cursor pagination.

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, page toward older records from `cursor` (load more) |
| `cursor` | `int` | — | Use `last` with `next=true` for the next older page, or `first` without `next` to move back toward newer records |

**Response `200`:**

```/dev/null/me-pools-response.json#L1-18
{
  "ok": true,
  "description": "Confirmed pools for user f47ac10b-...",
  "result": {
    "pools": [
      {
        "id":      51,
        "address": "0xAbCd5678...",
        "symbol":  "MYP",
        "name":    "My Pool",
        "admin":   "0xd8dA6BF2...",
        "createdAt": "2024-01-15T10:30:00Z"
      }
    ],
    "first": 51,
    "last": 44
  }
}
```

| Field | Type | Description |
|---|---|---|
| `pools` | `array` | Current page of deployed pools (`[]` when empty) |
| `first` | `int` | Highest record ID in the current page |
| `last` | `int` | Lowest record ID in the current page — use as `cursor` with `next=true` |

---

### 6.4 `GET /api/v2/users/me/pools/:address`

Returns a single confirmed SwapPool by contract address. Address match is case-insensitive.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Pool contract address |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Pool at address 0xAbCd5678...",
  "result": {
    "pool": {
      "id": 51,
      "address": "0xAbCd5678...",
      "name": "My Pool",
      "symbol": "MYP",
      "description": "...",
      "tags": ["Market"],
      "imageUrl": null,
      "tokenRegistryAddress": "0x...",
      "feePolicyAddress": "0x...",
      "quoterAddress": "0x...",
      "feesDecoupled": false,
      "locationName": null,
      "createdAt": "2024-01-15T10:30:00Z",
      "updatedAt": "2024-01-15T10:30:00Z"
    }
  }
}
```

---

### 6.5 `GET /api/v2/users/me/deployments`

Returns the complete deployment history for the authenticated user, covering both tokens and pools in reverse-chronological order.

**Response `200`:**

```/dev/null/me-deployments-response.json#L1-13
{
  "ok": true,
  "description": "Deployment history for user f47ac10b-...",
  "result": {
    "deployments": [
      {
        "trackingId":    "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "resourceType":  "TOKEN",
        "createdAt":     "2024-01-15T10:30:00Z"
      }
    ]
  }
}
```

| Field | Type | Description |
|---|---|---|
| `trackingId` | `string` (UUID) | Use with [`GET /api/v2/otx/track/:trackingId`](#52-get-apiv2otxtracktrackingid) |
| `resourceType` | `"TOKEN"` \| `"POOL"` | Type of deployed resource |
| `createdAt` | `string` (ISO 8601) | When the deploy request was submitted |

---

### 6.6 `GET /api/v2/users/me/shares/eligibility/:address`

Checks whether an exact wallet address belongs to a different, approved Cosmo
user and can therefore receive a private send attachment.

```json
{
  "ok": true,
  "result": { "eligible": true }
}
```

This endpoint only exposes a boolean and never returns the recipient's profile.

### 6.7 `POST /api/v2/users/me/shares/:txHash`

Creates a private attachment after the token send is confirmed. This endpoint
works for transactions paid by the user's wallet and for transactions submitted
through the Cosmo relayer.

```json
{
  "permission": 5,
  "note": "Thanks for the supplies"
}
```

```json
{
  "ok": true,
  "result": {
    "shareId": 42,
    "txHash": "0xabc..."
  }
}
```

The backend rechecks the receipt even if the client already waited for it. The
receipt must be successful, belong to the authenticated wallet either as the
outer signer or smart-account target, and contain exactly one positive ERC-20
`Transfer` from that wallet. The event recipient must be a different, approved
Cosmo user.

`permission` is a bitmask: email is `1`, phone is `2`, and location (label plus
coordinates) is `4`. Any authenticated sender whose transaction is verified may
add a trimmed note with `permission: 0`; no profile PII is required in that
case. PII is opt-in and only the sender chooses the bits. Repeating an identical
request is idempotent and returns the same `shareId`; trying to replace an
existing attachment for the transaction returns `409` with `E16`. An
unconfirmed, failed, ambiguous, or unrelated transaction returns `422` with
`E19`.

### 6.8 `GET /api/v2/users/me/shares/:txHash`

Returns the private attachment for a confirmed ERC-20 transfer. Only its sender
and designated recipient may read it. Missing and unauthorized records both
return `404`.

```json
{
  "ok": true,
  "result": {
    "id": 42,
    "txHash": "0xabc...",
    "permission": 5,
    "note": "Thanks for the supplies",
    "sharedBy": {
      "name": "Alice",
      "email": "alice@example.com",
      "location": {
        "locationName": "Nairobi, Kenya",
        "lat": -1.286389,
        "lng": 36.817223
      }
    },
    "createdAt": "2026-08-04T12:00:00Z"
  }
}
```

The note is always returned to an authorized participant. PII is fetched from
the sender's current profile only when its permission bit is set; the SQL query
and response serializer both enforce the bitset. The note and all
contact/location fields remain off-chain.

---

## 7. Superadmin Endpoints (`/api/v2/admin/*`)

These endpoints require a caller with role `SUPERADMIN`. Any other caller (including service JWTs) receives `403 Forbidden` with error code `E10`.

The baseline migration seeds an initial approved superadmin account. Additional superadmins must be granted directly in the `users` table by an operator with write access; public signup and SIWE verify create normal `USER` accounts only.

`GET /api/v2/admin/reports` and `GET /api/v2/admin/reports/:id` are on this group
too and are documented with the rest of Posts, in [§11.9](#119-global-moderation-reads).

---

### 7.1 `GET /api/v2/admin/users`

Returns one page of the account directory, newest first, using cursor pagination that matches the catalog (`perPage`, `cursor`, `next`). Optionally narrowed by approval status and by a search term.

> **Breaking change.** This endpoint previously ignored pagination and answered with every row. `users` is now at most `perPage` accounts and the response carries `first` / `last`. A client that pages or filters the directory locally must move both jobs to the server, or it will silently present a truncated directory as the whole one.

**Query parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `q` | `string` | — | Search over name, email, phone and address. 2–64 characters; a shorter or longer term is rejected. Country and role are not searched |
| `status` | `string` | — | Filter by status: `PENDING`, `APPROVED`, or `REJECTED` (case-insensitive) |
| `perPage` | `int` | `100` | Results per page (max `100`) |
| `next` | `bool` | `false` | When `true`, page toward newer records from `cursor` |
| `cursor` | `int` | — | Use `last` to fetch older records or `first` with `next=true` to move back toward newer records |

`q` matches as a prefix at two characters and as a substring above that, and the same rules and error messages apply as on the public catalog search.

**Response `200`:**

```/dev/null/admin-users-response.json#L1-20
{
  "ok": true,
  "description": "Users list",
  "result": {
    "users": [
      {
        "id":              4210,
        "ethereumAddress": "0xd8dA6BF2...",
        "name":            "Ada Lovelace",
        "email":           "ada@example.com",
        "phone":           "+254700000000",
        "country":         "KE",
        "role":            "USER",
        "status":          "PENDING"
      }
    ],
    "first": 4210,
    "last": 4187
  }
}
```

| Field | Type | Description |
|---|---|---|
| `users` | `array` | Current page of accounts |
| `first` | `int` | Highest account ID in the current page |
| `last` | `int` | Lowest account ID in the current page |

**Errors:**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | `q` is shorter than 2 or longer than 64 characters, or `status` is not one of the three approval states |

---

### 7.2 `PUT /api/v2/admin/users/:address/approve`

Approves a user account, setting its status to `APPROVED`. The user can then sign in.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Ethereum address of the account to approve |

**Request body:** none

**Response `200`:**

```/dev/null/admin-approve-response.json#L1-7
{
  "ok": true,
  "description": "User status updated to APPROVED",
  "result": {
    "user": { "..." : "..." }
  }
}
```

---

### 7.3 `PUT /api/v2/admin/users/:address/reject`

Rejects a user account, setting its status to `REJECTED`. The user will be blocked from signing in.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Ethereum address of the account to reject |

**Request body:** none

**Response `200`:**

```/dev/null/admin-reject-response.json#L1-7
{
  "ok": true,
  "description": "User status updated to REJECTED",
  "result": {
    "user": { "..." : "..." }
  }
}
```

---

### 7.4 `PUT /api/v2/admin/users/:address/limits`

Updates the deployment quotas for a specific user.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Ethereum address of the target user |

**Request body:**

```/dev/null/admin-limits-request.json#L1-4
{
  "maxPools":  20,
  "maxTokens": 50
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `maxPools` | `int` | Yes | New maximum pool deployment count |
| `maxTokens` | `int` | Yes | New maximum token deployment count |

**Response `200`:**

```/dev/null/admin-limits-response.json#L1-7
{
  "ok": true,
  "description": "User limits updated",
  "result": {
    "user": { "..." : "..." }
  }
}
```

---

### 7.5 `PUT /api/v2/admin/users/:address/profile`

Corrects another account's identity, contact details, and profile preferences. `PUT /auth/profile` edits only the signed-in account, so this is the operator equivalent; it takes the same body and applies the same validation.

It cannot change status, role or limits — those have their own endpoints.

**Path parameters:**

| Parameter | Type | Description |
|---|---|---|
| `address` | `string` | Ethereum address of the target user |

**Request body:**

```/dev/null/admin-profile-request.json#L1-8
{
  "name":         "Ada Lovelace",
  "email":        "ada@example.com",
  "phone":        "+254700000000",
  "country":      "KE",
  "preferredCurrency": "KES",
  "bio":          "Community organiser",
  "website":      "https://ada.example",
  "locationName": "Nairobi",
  "geoLat":       -1.2921,
  "geoLng":       36.8219
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | No | Max 80 characters |
| `email` | `string` | No | Must be a valid address, unique across accounts |
| `phone` | `string` | No | Unique across accounts |
| `country` | `string` | No | ISO 3166-1 alpha-2 code |
| `preferredCurrency` | `string` | No | Three-letter uppercase ISO 4217 code. Omit to preserve a legacy value. |
| `bio` | `string` | No | Max 500 characters. Send `""` to clear. |
| `website` | `string` | No | HTTP(S) URL. Send `""` to clear. |
| `locationName` | `string` | No | Max 200 characters |
| `geoLat` | `number` | No | −90 to 90. Must be sent with `geoLng` |
| `geoLng` | `number` | No | −180 to 180. Must be sent with `geoLat` |

Omitted location fields preserve the stored values; they are not cleared.

**Response `200`:**

```/dev/null/admin-profile-response.json#L1-7
{
  "ok": true,
  "description": "User profile updated",
  "result": {
    "user": { "..." : "..." }
  }
}
```

**Errors:**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | Validation failed, including `geoLat` sent without `geoLng` or vice versa |
| `404` | `E03` | No account at that address |
| `409` | `E17` / `E18` | `email` or `phone` already belongs to another account |

---

### 7.6 `GET /api/v2/admin/users/:address/pools`

The pools an account belongs to, cursor paginated.

Membership is **derived**, not stored: nothing records a user joining a pool, but the catalog records which vouchers a pool allows, so an account belongs to the pools that allow a voucher it owns. Pool ownership and deployment history are deliberately not part of this.

**Query parameters:** `perPage`, `cursor`, `next`, as elsewhere.

**Response `200`:** `{ "pools": [ … ], "first": 42, "last": 17 }` — pool rows in the same shape as [`GET /api/v2/pools`](#10-pool-offerings-get-apiv2poolsaddressofferings).

---

### 7.7 `GET /api/v2/admin/users/:address/stats`

The figures behind one account.

**Response `200`:**

```/dev/null/admin-account-stats.json#L1-12
{
  "ok": true,
  "description": "Account statistics",
  "result": {
    "reports":         3,
    "vouchers":        2,
    "poolMemberships": 1,
    "transactions":    118,
    "swaps":           4,
    "partners":        9
  }
}
```

| Field | Source | Description |
|---|---|---|
| `reports` | catalog | Approved reports authored by the account |
| `vouchers` | catalog | Vouchers the account owns that the public catalog can return (active, geolocated) |
| `poolMemberships` | catalog | Distinct pools allowing a voucher the account owns |
| `transactions` | federation | Successful transfers sent or received |
| `swaps` | federation | Successful swaps the account initiated |
| `partners` | federation | Distinct counterparties across successful transfers, excluding the account itself |

The three catalog figures use the same visibility rules as the lists they sit beside, so a count and its list never disagree.

**The three federation figures are `null` when the federation database is not configured or a read fails.** Null rather than zero, deliberately: a caller must be able to tell "none" from "no answer", and a zero reads as the first. The catalog half is still returned in that case.

---

### 7.8 `GET /api/v2/admin/tokens`

Lists all confirmed token contracts deployed across all users.

**Response `200`:**

```/dev/null/admin-tokens-response.json#L1-7
{
  "ok": true,
  "description": "All deployed tokens",
  "result": {
    "tokens": [ { "address": "0x...", "symbol": "MTK", "name": "My Token" } ]
  }
}
```

---

### 7.9 `GET /api/v2/admin/pools`

Lists all confirmed SwapPool contracts deployed across all users.

**Response `200`:**

```/dev/null/admin-pools-response.json#L1-7
{
  "ok": true,
  "description": "All deployed pools",
  "result": {
    "pools": [ { "address": "0x...", "symbol": "MYP", "name": "My Pool" } ]
  }
}
```

---

### 7.10 `GET /api/v2/admin/deployments`

Lists all deployment history entries across all users.

**Response `200`:**

```/dev/null/admin-deployments-response.json#L1-7
{
  "ok": true,
  "description": "All deployment history",
  "result": {
    "deployments": [ { "trackingId": "...", "resourceType": "TOKEN", "createdAt": "..." } ]
  }
}
```

---

### 7.11 `GET /api/v2/admin/dashboard/stats`

Dashboard aggregates. Mounted on the `/admin/dashboard` group, not mixed with user/token mutations.

**Query parameters:**

| Parameter | Type | Description |
|---|---|---|
| `range` | `string` | `5m`, `15m`, `30m`, `1h`, `6h`, `1d` (default), or `custom` |
| `since` | `string` | RFC3339 start, required when `range=custom` |
| `until` | `string` | RFC3339 end, required when `range=custom` |

`pools`, `vouchers`, and `accounts` are lifetime catalog stock. `reports` and `transactions` are counts inside the requested window. Two series feed the charts: catalog creations, and `chain_data_v2.tx` envelopes. `transactions` is `null` when federation is down.

### 7.12 `GET /api/v2/admin/dashboard/transactions`

Latest indexed envelopes from `chain_data_v2.tx`, classified from live event tables. Cursor pagination matches the catalog (`perPage`, `cursor`, `next`).

| Parameter | Type | Description |
|---|---|---|
| `perPage` | `number` | Page size, default/max 100 |
| `cursor` | `number` | Event or envelope id bound |
| `next` | `boolean` | `true` pages forward (lower ids), `false` pages back |
| `type` | `string` | `transfer`, `swap`, `mint`, `burn`, `deposit`, `faucet` |
| `voucher` | `string` | Token or pool `0x` address (`contract_address`) |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Admin dashboard totals",
  "result": {
    "range": { "preset": "1d", "since": "2026-08-12T00:00:00Z", "until": "2026-08-13T00:00:00Z", "bucket": "1h" },
    "pools": 42,
    "vouchers": 2192,
    "accounts": 310,
    "reports": 12,
    "transactions": 1843,
    "catalogSeries": [
      { "t": "2026-08-12T00:00:00Z", "reports": 1, "vouchers": 2, "pools": 0 }
    ],
    "transactionSeries": [
      { "t": "2026-08-12T00:00:00Z", "transactions": 16 }
    ]
  }
}
```

---

### 7.13 `GET /api/v2/admin/vouchers`

The console's voucher catalog. Same `q`, `tags`, `tagMatch` and `owner`
parameters as the public list in [section 9](#9-public-market-catalog-get-apiv2vouchers),
and the same cursor pagination (`perPage` default/max 100, `cursor`, `next`).

It differs from the public list in exactly two ways, and both are the point:

- **No visibility gate.** The public list filters `active` vouchers with
  complete coordinates. This one filters neither, so retired and ungeolocated
  vouchers are reachable. Each row carries `active` so the console can render
  status.
- **No `bbox`.** The console lists rather than maps, and a viewport filter would
  silently exclude the very rows this endpoint exists to reach.

| Parameter | Type | Default | Description |
|---|---|---|---|
| `status` | `string` | `all` | `all`, `active`, or `retired` (case-insensitive). An unrecognised value is `400 E04`, never a silently widened page |

Responses are `Cache-Control: no-store`. The public market responses are
cacheable because they are identical for every caller; this one is scoped to a
role and must never be served to anyone else.

### 7.14 `GET /api/v2/admin/vouchers/:address`

One voucher whatever its visibility. The projection is
[9.6](#96-voucher-detail-get-apiv2vouchersaddress)'s plus `active`, and there is
no `active` predicate: `404` means the catalog does not hold the address, not
that the voucher is retired.

This route is why retiring is reversible at all. Without it, retiring a voucher
would also remove the only screen an operator could restore it from.

### 7.15 `PUT /api/v2/admin/vouchers/:address/restore`

Returns a retired voucher to the public catalog. `SUPERADMIN`-only and no
request body. Success is `200 OK` with description `Voucher restored` and the
restored row under `result.voucher`.

Deliberately not the mirror image of
[9.8](#98-retire-a-voucher-delete-apiv2vouchersaddress): an owner may take their
own voucher out of circulation, but putting one back is an operator decision, so
there is no owner-facing route and no toggle an owner can flip back and forth.

Restoring an already-active voucher is idempotent in state and still `200`. It
is not an error — the caller asked for a state the row is already in, and a
`409` would make the console handle a case with no user-visible difference.

---

## 8. SIWE Message Construction

The following is a client-side example using the [`siwe`](https://www.npmjs.com/package/siwe) JavaScript library. This pattern applies to `/auth/signup`, `/auth/signin`, and `/auth/siwe/verify`.

```/dev/null/siwe-example.js#L1-24
import { SiweMessage } from 'siwe';

// Step 1: Fetch a one-time nonce from the server
const nonceRes = await fetch('/auth/nonce', { method: 'POST', credentials: 'include' });
const { result: { nonce } } = await nonceRes.json();

// Step 2: Build the EIP-4361 message
const message = new SiweMessage({
  domain:    window.location.host,
  address:   walletAddress,   // must be checksummed (EIP-55)
  statement: 'Sign in to CLC',
  uri:       window.location.origin,
  version:   '1',
  chainId:   42220,           // Celo Mainnet
  nonce,
}).prepareMessage();

// Step 3: Sign with the connected wallet
const signature = await signer.signMessage(message);

// Step 4: Submit to /auth/signin (or /auth/siwe/verify for unified auth)
await fetch('/auth/signin', {
  method:      'POST',
  headers:     { 'Content-Type': 'application/json' },
  body:        JSON.stringify({ message, signature }),
  credentials: 'include',   // required so the browser stores the __clc_auth cookie
});
```

> **Note on `credentials: 'include'`:** Calls to `/auth/*` and protected
> `/api/v2/*` routes must include credentials so the browser sends and stores
> the `__clc_auth` HttpOnly cookie cross-origin. The public market routes in
> [section 9](#9-public-market-catalog-get-apiv2vouchers) do not use the cookie
> and may explicitly send `credentials: 'omit'`.

For a full Reown AppKit (formerly WalletConnect) integration example, including the `siweConfig` adapter and sign-up flow, see [`docs/AUTH.md`](./AUTH.md).

---

## 9. Public Market Catalog (`GET /api/v2/vouchers`)

These are the only public routes under `/api/v2`. They back the Market
experience, which is browsable by signed-out visitors. Sending the
`__clc_auth` cookie is harmless but changes nothing: a guest and a signed-in
user receive byte-identical responses.

The list route returns active, geolocated voucher summaries with a
backend-derived marker kind. Per-voucher detail — contact details, images, and
unit of account — lives in [section 9.6](#96-voucher-detail-get-apiv2vouchersaddress);
its available offerings live in
[section 9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings).

### 9.1 Request

```http
GET /api/v2/vouchers?perPage=500
GET /api/v2/vouchers?perPage=500&cursor=2212&next=true
GET /api/v2/vouchers?perPage=500&bbox=36.6,-1.5,37.1,-1.1
```

| Parameter | Type | Default | Notes |
|---|---|---|---|
| `perPage` | integer | `500` | **Endpoint exception:** default *and* maximum are 500, not the 100 used by every other catalog route. `0` or any value above 500 resolves to 500. |
| `cursor` | integer | – | A voucher `id` from a previous response's `first` / `last`. |
| `next` | boolean | `false` | `true` pages forward (`id < cursor`); `false` with a cursor pages backward (`id > cursor`). |
| `bbox` | string | – | Optional `minLng,minLat,maxLng,maxLat` viewport filter. |
| `owner` | string | – | Only vouchers owned by this address. Compared case-insensitively; a malformed address is a `400`. |

**Pagination** uses descending `id` keyset paging. Requests without a cursor
return the newest IDs first. Responses always carry `first` and `last` as JSON
numbers; an empty page returns `"vouchers": []`, `"first": 0`, `"last": 0` —
never `null` and never omitted.

**`bbox` validation.** All four values must be finite and comma-separated, with
`-180 <= lng <= 180`, `-90 <= lat <= 90`, `minLng <= maxLng`, and
`minLat <= maxLat`. A single box therefore **cannot cross the antimeridian**; a
client whose viewport does must issue two requests (`[-180, east]` and
`[west, 180]`) and merge them by `id`. Malformed, out-of-range, or inverted
values return `400` with code `E04` (validation failed).

`bbox` composes with the cursor, but a caller **must keep one bbox fixed while
traversing its pages** — changing it mid-traversal produces an inconsistent set.

### 9.2 Response

```/dev/null/vouchers.json#L1-20
{
  "ok": true,
  "description": "Market vouchers",
  "result": {
    "vouchers": [
      {
        "id": 2710,
        "chainId": 42220,
        "address": "0x0000000000000000000000000000000000000000",
        "name": "Priscilah Malii",
        "symbol": "VOUCHER",
        "iconUrl": "https://content.sarafu.network/voucher/<hash>.jpg",
        "locationName": "C96, KEN",
        "geoLat": -1.450979,
        "geoLng": 38.235115,
        "kind": "voucher"
      }
    ],
    "first": 2710,
    "last": 2212
  }
}
```

| Field | Type | Notes |
|---|---|---|
| `id` | integer | Database ID. Doubles as the pagination cursor and stable map feature identity. |
| `chainId` | integer | Settlement chain, `42220` for Celo mainnet. |
| `address` | string | Contract address, preserved in its stored EIP-55 checksummed form. Never lowercased by the API. |
| `name` | string \| null | |
| `symbol` | string \| null | |
| `iconUrl` | string \| null | Profile image used by list rows. Clients must provide a fallback. |
| `locationName` | string \| null | Free-text place label from the import. |
| `geoLat` | number | Never null — rows without complete coordinates are excluded. |
| `geoLng` | number | Never null. |
| `kind` | string | `"goods"`, `"services"`, or `"voucher"`. |

**Included rows.** Only vouchers that are `active` **and** have complete
coordinates. There is no `internal` predicate: `internal` is an import
artifact, not a visibility or product-policy flag, and it is not exposed in
this contract. See `MIGRATION.md` §4.7.

**`kind` derivation.** Computed from the voucher's **available** offerings:

| Condition | `kind` |
|---|---|
| Dominant available `commodity_type` is `GOOD` | `goods` |
| Dominant available `commodity_type` is `SERVICE` | `services` |
| Equal `GOOD` / `SERVICE` counts | `goods` |
| No available offerings | `voucher` |

Unavailable offerings are ignored entirely. `commodity_type` is FK-constrained
to `GOOD`/`SERVICE`, so the `voucher` fallback represents "no available
offerings", never an unrecognised type.

**No distance field.** Distance is relative to optional browser geolocation and
is presentation state, not catalog state. It is never calculated server-side.

`iconUrl` is included because the catalog backs the default list view. Larger
profile fields remain on
[voucher detail](#96-voucher-detail-get-apiv2vouchersaddress).

### 9.3 Caching

```http
Cache-Control: public, max-age=60, stale-while-revalidate=300
Vary: Origin
```

Successful `200` representations never vary by authorization or cookie, so
they are publicly cacheable. Validation and server errors do not carry this
cache policy. `Origin` is **merged** into any existing `Vary` rather than
replacing it; the global CORS policy sets `AllowCredentials: true`, which makes
origin variance mandatory even though this client sends no credentials.

### 9.4 Rate limiting

Because the routes have no authentication to fall back on, they carry a
per-client-IP token bucket shared by all three public routes: **60 requests per
minute sustained, burst 40**. Exceeding it returns `429 Too Many Requests` with
a `Retry-After` header in whole seconds, calculated from the next available
token.

The burst is sized from how the Market tab actually opens: five catalog pages,
a bounded viewport bootstrap, then detail and offerings requests for selected
vouchers. Voucher-only rows skip the offerings request, so 40 still leaves
headroom for a run of selections immediately after load.

```/dev/null/rate-limited.json#L1-5
{
  "ok": false,
  "description": "rate limit exceeded, retry later",
  "code": "E11"
}
```

The client key is the **direct peer address**. `X-Forwarded-For` is honoured
only when `api.trust_proxy` is enabled, which must be set only where the
service is reachable exclusively through the Caddy proxy network; otherwise any
caller could forge the header and spend another client's budget. When trusted,
the **rightmost** entry is used, since that is the address the proxy actually
observed.

The limiter's client registry is bounded and idle entries are swept
periodically, so forged source addresses cannot grow it without bound.

> This limit assumes the current single API replica. If the service is scaled
> horizontally, move the limit to a shared edge or distributed limiter rather
> than multiplying the allowance per replica.

### 9.5 Examples

First page:

```sh
curl -s 'https://<host>/api/v2/vouchers?perPage=500'
```

Next page, using the previous response's `last`:

```sh
curl -s 'https://<host>/api/v2/vouchers?perPage=500&cursor=2212&next=true'
```

Bounded to a Nairobi-sized viewport:

```sh
curl -s 'https://<host>/api/v2/vouchers?perPage=500&bbox=36.6,-1.5,37.1,-1.1'
```

### 9.6 Voucher detail (`GET /api/v2/vouchers/:address`)

Public for the same reason as the list: a shared voucher link must open for a
signed-out visitor. It backs both the map's selection card and the voucher page.

```http
GET /api/v2/vouchers/0x0001423EC38205B125e7340aa37F4D19276e3aB7
```

`:address` must be a valid hex address; anything else returns `400` with code
`E04` before the database is touched. Lookup is **case-insensitive**, so a
lowercased pasted link resolves, but the response echoes the stored EIP-55
checksummed form.

```/dev/null/voucher.json#L1-27
{
  "ok": true,
  "description": "Market voucher",
  "result": {
    "voucher": {
      "id": 2710,
      "chainId": 42220,
      "address": "0x0001423EC38205B125e7340aa37F4D19276e3aB7",
      "name": "Priscilah Malii",
      "symbol": "MALII",
      "decimals": 6,
      "ownerAddress": "0xDeF0000000000000000000000000000000000456",
      "description": "Community voucher",
      "voucherUoa": "KSH",
      "voucherValue": "10",
      "locationName": "C96, KEN",
      "geoLat": -1.450979,
      "geoLng": 38.235115,
      "email": "issuer@example.org",
      "website": "https://example.org",
      "phone": "+254700000000",
      "iconUrl": "https://content.sarafu.network/voucher/<hash>.jpg",
      "bannerUrl": "https://content.sarafu.network/voucher/<hash>.jpg",
      "kind": "goods"
    }
  }
}
```

| Field | Type | Notes |
|---|---|---|
| `id`, `chainId`, `address`, `name`, `symbol`, `locationName`, `kind` | — | Identical semantics to the [list contract](#92-response). |
| `decimals` | integer \| null | ERC-20 decimals. |
| `ownerAddress` | string \| null | Issuer's wallet, stored casing preserved. |
| `description` | string \| null | |
| `voucherUoa` | string \| null | Unit of account, **free text entered by the issuer** — `KSH`, `Ksh`, `KES`, `usd`, and `Hour` all occur. Never normalised server-side; render verbatim. |
| `voucherValue` | string \| null | What one voucher is worth in `voucherUoa`, so `symbol` + `voucherValue` + `voucherUoa` reads "1 MALII = 10 KSH". Serialized as a **string**: the column is `NUMERIC` and a float would lose scale. |
| `geoLat`, `geoLng` | number \| null | **Nullable here, unlike the list.** Coordinates are not required — a voucher without them still has a readable page, it simply renders no map. Absent coordinates are `null`, never `0`. |
| `email`, `website`, `phone` | string \| null | Issuer contact, as imported. |
| `iconUrl` | string \| null | Profile image. Absent for roughly two thirds of the imported catalog, so clients need a real fallback. |
| `bannerUrl` | string \| null | Wide header image. Absent about as often as `iconUrl`. |

**Visibility.** Only `active` vouchers resolve. A deactivated voucher returns
`404` with a body **byte-identical** to an address that never existed, so the
response cannot be used to probe which addresses are in the database. Unlike the
list, coordinates are not required.

Caching and rate limiting are exactly as in [9.3](#93-caching) and
[9.4](#94-rate-limiting); all three routes share one per-IP bucket.

> **Swagger drift.** `docs/swagger.yaml` predates the catalog surface and does
> not describe these routes. Reconciling it is tracked as separate follow-up
> work.

### 9.8 Retire a voucher (`DELETE /api/v2/vouchers/:address`)

Requires a user JWT. The caller must be either the voucher's current
`ownerAddress` or a `SUPERADMIN`; the catalog row is locked and checked in the
same transaction as the write, so authorization cannot race a concurrent
ownership change. Another signed-in user receives `403 E10`, a service
credential `403`, and no session `401`. No request body is required.

```http
DELETE /api/v2/vouchers/0x1B4a4f08D796C72efB377f629130a15eb0B78942
Cookie: __clc_auth=<owner-or-admin-jwt>
```

Success is `200 OK` with description `Voucher retired` and **no `result`**. The
other catalog writes return the saved row so a client can seed its cache, but
the row this one writes is no longer visible to
[9.6](#96-voucher-detail-get-apiv2vouchersaddress) — reading it back would
answer `404`. A client's correct next move is to drop the voucher from its cache,
not to re-read it.

**Soft, not destructive.** The endpoint flips `vouchers.active`, the same
visibility gate every market, search and offering statement already filters on.
Nothing is deleted: the reports and offerings that reference the voucher keep
their foreign keys, and a restore returns it intact. Deleting instead was
measured and rejected — 65% of report→voucher links ride on rows a delete would
take with them.

**Visibility afterwards.** The voucher stops resolving from
[9](#9-public-market-catalog-get-apiv2vouchers), [9.6](#96-voucher-detail-get-apiv2vouchersaddress)
and [9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings), and its 404
body is byte-identical to an address that never existed. It remains visible to
the console reads in [7.13](#713-get-apiv2adminvouchers) and
[7.14](#714-get-apiv2adminvouchersaddress), and to the surfaces that deliberately
ignore `active`: pool composition, symbol occupancy, report linking and
federation balance metadata. A retired voucher's symbol stays taken, so a new
deploy cannot collide with it.

| State | Repeat `DELETE` |
|---|---|
| Active | `200`, retired |
| Already retired | `409` `voucher is already retired` |
| Unknown address | `404`, body identical to a retired-voucher read |

Activity is checked before ownership on purpose: answering `409` to the owner
and `403` to everyone else would leak that the address exists.

There is no owner-facing restore. Putting a voucher back is an operator
decision — see [7.15](#715-put-apiv2adminvouchersaddressrestore).

### 9.7 Voucher offerings (`GET /api/v2/vouchers/:address/offerings`)

Returns the available offerings for one active voucher. It is public so a
signed-out visitor can open the default Offerings tab on a shared voucher page.
The address lookup is case-insensitive and rejects malformed hex addresses with
`400` and code `E04`.

```http
GET /api/v2/vouchers/0x0001423EC38205B125e7340aa37F4D19276e3aB7/offerings?perPage=20
GET /api/v2/vouchers/0x0001423EC38205B125e7340aa37F4D19276e3aB7/offerings?perPage=20&cursor=42&next=true
```

The route uses the standard descending-`id` keyset pagination contract:
`perPage` defaults to 100 and is capped at 100, `next=true` selects IDs below
the cursor, and a cursor without `next=true` selects IDs above it. Every
response contains numeric `first` and `last` values. An empty result is
`"offerings": []`, `"first": 0`, `"last": 0`, never `null`.

Only rows satisfying both `offering.available = true` and
`voucher.active = true` are returned. This matches the market voucher `kind`
derivation: a catalog row with `kind: "voucher"` has an empty offerings result.
The imported catalog observed on 2026-07-27 contains at most 19 offerings for a
single voucher, so the client page size of 20 normally completes in one request.

Caching and rate limiting are exactly as in [9.3](#93-caching) and
[9.4](#94-rate-limiting).

---

## 10. Pool offerings (`GET /api/v2/pools/:address/offerings`)

Returns every available offering published against a voucher the pool allows.
The pool detail screen lists these in its default Offerings tab, instead of
walking the pool's vouchers one request at a time.

**Authentication required** — this route sits on the authenticated API group
with the other `/api/v2/pools/*` routes, because the pool screen itself requires
a session. It is therefore `no-store` and carries no public cache policy, unlike
the voucher-scoped route in [9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings).

```http
GET /api/v2/pools/0x3d11B609689FefDD887045933AED75e2B014a0cc/offerings?perPage=20
GET /api/v2/pools/0x3d11B609689FefDD887045933AED75e2B014a0cc/offerings?perPage=20&cursor=1354&next=true
```

The address lookup is case-insensitive and rejects malformed hex addresses with
`400` and code `E04`. Pagination is the standard descending-`id` keyset
contract: `perPage` defaults to 100 and is capped at 100, `next=true` selects
IDs below the cursor, and a cursor without `next=true` selects IDs above it.
Every response contains numeric `first` and `last`. An empty result is
`"offerings": []`, `"first": 0`, `"last": 0`, never `null`.

Rows are filtered on three predicates:

| Predicate               | Why                                                              |
| ----------------------- | ---------------------------------------------------------------- |
| `pool_tokens.is_allowed` | Matches `GET /api/v2/pools/:address/vouchers`, so the two tabs agree |
| `offering.available`     | Matches [9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings) |
| `voucher.active`         | Matches [9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings) |

The last two are deliberately identical to the voucher-scoped route: an offering
must read the same whether the user reached it through its voucher or through a
pool that accepts it.

Each row carries `voucherName`, `voucherSymbol`, and `voucherAddress`, because a
pool's offerings come from many vouchers and the UI has to say which voucher each
one belongs to. The response body is otherwise byte-identical in shape to
[9.7](#97-voucher-offerings-get-apiv2vouchersaddressofferings).

---

## 11. Reports / Posts (`/api/v2/reports`)

The Posts feed. **Authentication required** — Posts is a private home tab, so
these routes sit on the authenticated API group.

| Operation | User JWT | Service JWT | `SUPERADMIN` user JWT |
|---|---|---|---|
| List and detail | Yes | Yes | Yes |
| Create | Yes, as `APPROVED` | No | Yes, as `APPROVED` |
| Update | Original author only, stays `APPROVED` | No | Any post, status preserved |
| Approve | No | No | Any post |
| Delete | No | No | Any post |
| Own posts in any status | Yes, own only | No | Yes, own only |
| Any post in any status | No | No | Yes |

Only `APPROVED` reports are returned by the two read routes under
`/api/v2/reports`. `DRAFT`, `SUBMITTED`, and `REJECTED` are invisible there, and
an unapproved id is a `404` rather than a `403`: whether a draft exists is not
public information.

Those two routes are **not** widened for privileged callers. Every status is
reachable through the separate management reads in [§11.8](#118-an-authors-own-posts)
and [§11.9](#119-global-moderation-reads) instead, so one URL never returns
different rows to different callers.

### 11.1 Feed (`GET /api/v2/reports`)

```http
GET /api/v2/reports?perPage=20
GET /api/v2/reports?perPage=20&cursorUpdatedAt=2026-07-19T08:14:02.311Z&cursorId=3182
```

`perPage` defaults to 20 and is capped at 100.

The feed also accepts the catalog filter parameters below:

| Parameter | Type | Default | Description |
|---|---|---|---|
| `q` | `string` | — | Report title/description search; normalized length must be `2`–`64` characters |
| `tags` | `string` or repeated | — | Comma-separated report tags; at most `12` distinct values |
| `tagMatch` | `any` \| `all` | `any` | Whether a report must carry any or all selected tags |
| `vouchers` | `string` or repeated | — | Comma-separated stable voucher references (`chainId:0xAddress`); at most `12` |
| `voucherMatch` | `any` \| `all` | `any` | Whether a report must be linked to any or all selected vouchers |
| `facets` | `bool` | `false` | With `true` on the first page, include tag and voucher facet counts |

Voucher references are case-insensitive addresses scoped by chain ID. Invalid
or unknown references reject the whole request with `400`. `tagFacets` and
`voucherFacets` use the same disjunctive counting contract as the pool catalog
and are absent from cursor pages.

**This route does not use the numeric `cursor`/`next` keyset the catalog routes
use.** The feed is ordered by `updated_at DESC, id DESC`, and on the imported
catalog that order is unrelated to id order — measured on 3,730 approved rows,
the two agree on 4 of them and drift by up to 2,606 positions. An id cursor
would therefore skip and repeat rows. The cursor is the `(updatedAt, id)` pair
instead:

- `cursorUpdatedAt` is RFC3339 with nanoseconds; `cursorId` is a positive
  integer. Sending one without the other is a `400`, because accepting one alone
  would silently return the first page again.
- The response carries `next` — the pair to send for the following page — or
  `null` on the last page. A short page ends the feed, so `next` is `null`
  rather than the client having to guess from emptiness.
- `(updated_at, id)` row comparison is index-backed by
  `reports_status_updated_idx` (migration 009). Measured: index-only scan,
  0 heap fetches, ~0.03 ms for a cursor page.

Each row is a summary: `id`, `title`, `description`, `imageUrl`, `geoLat`,
`geoLng`, `periodFrom`, `periodTo`, `authorAddress`, `createdAt`, `updatedAt`,
`tags`, and `vouchers` (each `{id, chainId, address, name, symbol}`).

`body` is not returned on the feed — a row is a summary and `description`
already carries what it renders. It is available on the detail route.

`locationName` was removed in migration 010: the column was `NULL` on all 4,632
imported rows, because the source stored geography as a `"lat, lng"` string and
never a place name. Use `geoLat` / `geoLng`.

### 11.2 Detail (`GET /api/v2/reports/:id`)

```http
GET /api/v2/reports/3182
```

Returns one report under `result.report` with every feed field plus `status`
(`APPROVED` on this read route), `body`, `images`, `attachments`, `hostName`,
`participants`, and `timeTaken`.

Since migration 010 **none of this is parsed at request time.** The editor
document used to live in `reports.body` as JSONB and this route re-derived the
image list and the form from it on every call, with two independent
`jsonb_array_elements` passes. That work now happens once, at write time:

| Field                                     | Storage                        |
| ----------------------------------------- | ------------------------------ |
| `body`                                    | `reports.body`, markdown TEXT  |
| `images`                                  | `report_images`, by `ordinal`  |
| `attachments`                             | `report_attachments`           |
| `hostName`, `participants`, `timeTaken`   | columns on `reports`           |

`body` is **markdown**, not the editor's JSON and not plain text: heading
levels, lists, tables, emphasis and links are preserved, and 41 hyperlinks
across 8 reports have their URL only in the document (the visible text is a bare
commit hash), so plain-text flattening would have lost them. Only 310 of 4,632
imported reports have any prose at all — the rest keep their content in
`description` — and the whole corpus is 129 KB, down from 17 MB of JSONB.

`hostName`, `participants`, and `timeTaken` are issuer free text and are never
normalized — `timeTaken` alone contains `2`, `1 hour`, `2 hours`, `2hrs`, and
`1 hr`. Seven imported reports carry two form nodes and on two of them the
second differs, so the stored value is the first form in document order that has
any value at all; the previous unordered `LIMIT 1` picked arbitrarily.

`images` entries are
`{url, width, height, maxWidth, maxHeight, byteSize, caption}`. `width` and
`height` are the editor's display box; `maxWidth` and `maxHeight` are the
intrinsic size, and the two are **not** a scaled pair (a 267×1000 display box
against a 747×1600 intrinsic occurs). Any of them may be `null`. `caption` is
new — it was previously discarded. Reports carry 0 to 37 images; 73% have
exactly one and 381 have none.

`attachments` entries are `{kind, url, filename, byteSize}`, where `kind` is
`FILE` for an uploaded document or video and `EMBED` for an external player URL.
`filename` is set only for uploads. These were previously not served at all.

**Only `http(s)` URLs are stored** in either table — a CHECK constraint enforces
it, so the exclusion is a storage invariant rather than a filter each request
re-applies. Legacy `img` nodes embed base64 `data:` URIs up to ~900 KB and 48
`cloud_image` nodes hold unresolved `#nanoid` upload placeholders; none of them
reach a response.

One gap remains at the column level: six approved reports store a base64
`data:` URI directly in `reports.image_url`, up to 876 KB. Both routes return
`null` for those rather than the URI. They are not nulled in storage because
four of the six have no other image, so the fix is a CDN re-upload, not a
delete.

### 11.3 Write contract

`POST` and `PUT` use the same JSON document. `title` is the only required
content field; every other scalar is optional. Collections may be omitted or
sent as empty arrays.

```json
{
  "title": "August community harvest",
  "body": "## Harvest\n\nTwenty members took part.",
  "description": "A summary shown in the Posts feed.",
  "imageUrl": "https://content.example/posts/harvest-cover.jpg",
  "geoLat": -1.2864,
  "geoLng": 36.8172,
  "periodFrom": "2026-08-01T08:00:00Z",
  "periodTo": "2026-08-01T12:00:00Z",
  "hostName": "Wendo Group",
  "participants": "20",
  "timeTaken": "4 hours",
  "tags": ["Agriculture", "Youth"],
  "vouchers": [
    {
      "chainId": 42220,
      "address": "0x0000000000000000000000000000000000000001"
    }
  ],
  "images": [
    {
      "url": "https://content.example/posts/harvest-1.jpg",
      "width": 720,
      "height": 1080,
      "maxWidth": 1440,
      "maxHeight": 2160,
      "byteSize": 245761,
      "caption": "Sorting the harvest"
    }
  ],
  "attachments": [
    {
      "kind": "FILE",
      "url": "https://content.example/posts/harvest-notes.pdf",
      "filename": "harvest-notes.pdf",
      "byteSize": 81920
    }
  ]
}
```

| Field | Validation and storage rule |
|---|---|
| `title` | Required, non-blank, maximum 200 characters; surrounding whitespace is removed |
| `body` | Markdown text, maximum 500,000 characters; never editor JSON |
| `description` | Feed summary, maximum 10,000 characters |
| `imageUrl` | Optional `http(s)` URL; `null` or omission clears it on `PUT` |
| `geoLat`, `geoLng` | Must be sent together; latitude `-90..90`, longitude `-180..180` |
| `periodFrom`, `periodTo` | RFC3339 timestamps; if both are set, `periodFrom <= periodTo` |
| `hostName` | Optional issuer free text, maximum 200 characters |
| `participants`, `timeTaken` | Optional issuer free text, maximum 100 characters each |
| `tags` | At most 12 values; a value the vocabulary does not have yet is created with the post (see [section 12](#12-shared-vocabulary-token_tag-offering_unit)) |
| `vouchers` | At most 12 stable `{chainId,address}` references; every referenced voucher must exist |
| `images` | At most 50, in document order; URLs use `http(s)` |
| `attachments` | At most 20, in document order; `kind` is `FILE` or `EMBED`, and URLs use `http(s)` |

**All four collections reject duplicates with `400 E04`** after trimming and
canonicalisation — a repeated tag value, a repeated voucher `(chainId, lowercase
address)` identity, a repeated image URL, or a repeated attachment
`(kind, URL)` pair. Tags and vouchers previously deduplicated silently while
images failed and attachments were unchecked, so the same client mistake got
three different answers. An attachment URL may appear twice if the two entries
have different `kind` values: a `FILE` download and an `EMBED` player reference
are two rows.

`PUT` is **assign-all**, not patch semantics. It replaces every editable scalar
and all four relation sets (`tags`, `vouchers`, `images`, `attachments`) in one
transaction. Omitting an optional scalar clears it; omitting a collection
replaces that collection with an empty set. A validation or database failure
leaves the existing post unchanged.

The client cannot send `authorAddress`, `modifierAddress`, `status`, `sourceId`,
or timestamps. The server derives the immutable original author from the user
JWT, and derives the resulting status from the caller's role. An edit records
the caller as the modifier but never transfers authorship.

### 11.4 Create (`POST /api/v2/reports`)

Requires a user JWT; service JWTs receive `403 E10`. Posts are auto-approved:
every new post is stored as `APPROVED` and appears in the feed and detail
routes at once, whoever created it.

`vouchers` is restricted by role. A `SUPERADMIN` may link any catalog voucher;
any other caller may only link vouchers whose `owner_address` is their own
address, and a write naming someone else's voucher is a `403 E10`. A voucher
with no recorded owner is owned by nobody for this purpose.

```http
POST /api/v2/reports
Content-Type: application/json
Cookie: __clc_auth=<user-jwt>
```

Success is `201 Created` and returns the complete created report:

```json
{
  "ok": true,
  "description": "Report created",
  "result": {
    "report": {
      "id": 4633,
      "title": "August community harvest",
      "authorAddress": "0x0000000000000000000000000000000000000011",
      "status": "APPROVED",
      "tags": ["Agriculture", "Youth"],
      "vouchers": [],
      "images": [],
      "attachments": [],
      "createdAt": "2026-08-20T10:00:00Z",
      "updatedAt": "2026-08-20T10:00:00Z"
    }
  }
}
```

### 11.5 Update (`PUT /api/v2/reports/:id`)

Requires a user JWT. The caller must be either the report's immutable original
author or a `SUPERADMIN`. The ownership row is locked and checked in the same
transaction as the update, so authorization cannot race a concurrent delete or
edit. Another user receives `403 E10`.

```http
PUT /api/v2/reports/4633
Content-Type: application/json
Cookie: __clc_auth=<author-or-admin-jwt>
```

The request body is the complete [write document](#113-write-contract).
Success is `200 OK` with description `Report updated` and the complete report
under `result.report`.

The resulting workflow status depends on who edited:

| Caller | Status after the edit |
|---|---|
| Original author, any status | `APPROVED` |
| `SUPERADMIN` | Unchanged; a rejected post stays rejected |

Posts are auto-approved, so an author's edit publishes the result the same way
their create did. That also lifts a legacy `SUBMITTED` post out of a queue that
is no longer worked. A superadmin edit preserves status so that a moderator's
correction of a rejected post does not republish it by accident; rejecting and
deleting are the moderation levers, and approval
([§11.6](#116-approve-put-apiv2reportsidapprove)) remains for legacy rows.

The mutation response includes `status` and returns the row to its authorized
caller even when the normal detail route hides it. An author whose post is no
longer approved reads it back through
[§11.8](#118-an-authors-own-posts).

### 11.6 Approve (`PUT /api/v2/reports/:id/approve`)

Approval is `SUPERADMIN`-only. Normal users and service credentials receive
`403 E10`. No request body is required.

```http
PUT /api/v2/reports/4633/approve
Cookie: __clc_auth=<superadmin-jwt>
```

The endpoint changes only `status` to `APPROVED` and records the superadmin as
the latest modifier. It does not rewrite the post body or any relation. Calling
it for an already-approved post is idempotent in state and returns `200 OK` with
description `Report approved` and the complete report under `result.report`.

Once approved, the post appears in the feed and detail routes. Both an admin
edit and an author edit keep it `APPROVED`
([§11.5](#115-update-put-apiv2reportsid)); with auto-approval this endpoint is
needed only for rows that predate it.

There is no unapprove transition. A console must therefore confirm before
calling this.

### 11.7 Delete (`DELETE /api/v2/reports/:id`)

Deletion is `SUPERADMIN`-only. Original authors cannot delete their own posts;
normal user and service credentials receive `403 E10`. Deleting the report also
deletes its tags, voucher links, images, attachments, and verifiers through the
existing foreign-key cascades.

```http
DELETE /api/v2/reports/4633
Cookie: __clc_auth=<superadmin-jwt>
```

Success is `200 OK`:

```json
{
  "ok": true,
  "description": "Report deleted",
  "result": { "id": 4633 }
}
```

The mutation routes return `400 E02` for malformed JSON, `400 E04` for
invalid fields or unknown tag/voucher references, `401 E06` without valid
authentication, `403 E10` when the credential lacks permission or a non-superadmin
links a voucher they do not own, and `404 E03` when `:id` does not exist.

### 11.8 An author's own posts

```http
GET /api/v2/users/me/reports?perPage=20&status=SUBMITTED
GET /api/v2/users/me/reports/4633
```

Requires a user JWT; service credentials receive `403 E10`. Both routes return
every status **authored by the caller**, whose address comes from the JWT and is
not a parameter. `GET /users/me/reports/:id` naming another author's post is a
`404`, not a `403` — a `403` would confirm the id exists.

The list accepts the same `q`, `tags`, `tagMatch`, `vouchers`, and `voucherMatch`
filters as the feed, plus:

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | `uint` | `20` | Capped at `100`, as on the feed |
| `status` | `DRAFT` \| `SUBMITTED` \| `APPROVED` \| `REJECTED` | — | Case-insensitive; an unknown value is a `400`, never a silently broadened queue |
| `cursorUpdatedAt`, `cursorId` | keyset pair | — | Both or neither; one alone is a `400` |
| `next` | `bool` | `true` with a cursor | Direction. Sending it without a cursor is a `400` |

Rows are the feed's summary plus a required `status`; `body` stays off list rows
as it does on the feed. Ordering is `updated_at DESC, id DESC`, so an approved
post whose author has just edited it sorts to the top rather than back at its
original creation position.

The response carries **both** edge cursors rather than the feed's single `next`,
because this list pages in both directions:

```json
{
  "ok": true,
  "description": "Reports",
  "result": {
    "reports": [{ "id": 4633, "status": "SUBMITTED", "...": "..." }],
    "first": { "updatedAt": "2026-08-20T10:00:00Z", "id": 4633 },
    "last":  { "updatedAt": "2026-08-02T09:12:41Z", "id": 4102 }
  }
}
```

Send `last` with `next=true` for the following page and `first` with
`next=false` for the preceding one. Both are `null` on an empty page. The
comparison is on the `(updatedAt, id)` pair, so paging stays duplicate-free
across the tied timestamps the import produced in bulk.

`Cache-Control: no-store` on both routes.

### 11.9 Global moderation reads

```http
GET /api/v2/admin/reports?status=SUBMITTED&perPage=20
GET /api/v2/admin/reports/4633
```

`SUPERADMIN`-only; every other credential receives `403 E10`. Same parameters,
same page and cursor shape, and the same `no-store` as §11.8, over every author
rather than one. One extra parameter:

| Parameter | Type | Description |
|---|---|---|
| `author` | address | Exact, case-insensitive `author_address`; unknown or malformed is a `400` |

`author` is what lets a console's per-account panel and its global queue read one
endpoint. `GET /admin/reports/:id` returns the complete post in any status,
including `images`, `attachments`, and the field-report fields, which is what an
assign-all editor needs to resubmit a legacy post without erasing anything.

`status` is the moderation queue: a console defaults to `SUBMITTED` and offers
the rest. There is no `ALL` value on the wire — omit the parameter.

---

## 12. Shared vocabulary (`token_tag`, `offering_unit`)

Two global lookup tables back every tag picker and the offering unit field.
Both are open: a value the catalog does not have yet is **created by the write
that names it**, not by a separate call. There is no "create a tag" endpoint,
and there never should be — a vocabulary value with nothing referencing it is
litter.

### 12.1 Reads

```http
GET /api/v2/token-tags
GET /api/v2/offering-units
```

Both are authenticated (`/api/v2/*`), both return built-in and custom values in
one list, and neither distinguishes the two: a picker has no reason to care
which values shipped with a migration.

```json
{ "ok": true, "description": "Available offering units", "result": { "units": ["bag", "kg", "Produce Crate"] } }
```

### 12.2 Creation is a side effect of the owning write

| Write | Creates |
|---|---|
| `POST /api/v2/vouchers/:address/offerings`, `PUT .../offerings/:id` | the offering's `unit` |
| `PUT /api/v2/vouchers/:address/profile`, `PUT /api/v2/admin/vouchers/:address/profile` | each `tags` value |
| `PUT /api/v2/pools/:address/profile`, `PUT /api/v2/admin/pools/:address/profile` | each `tags` value |
| `POST /api/v2/reports`, `PUT /api/v2/reports/:id` | each `tags` value |
| `POST /api/v2/contracts/token`, `POST /api/v2/contracts/pool` | each `tags` value |

The value is created inside the same transaction as the write that named it, so
a request that fails validation, authorization or the quota check leaves nothing
behind. A cancelled form creates nothing because it never sends a request.

**An existing spelling always wins.** The lookup is case-insensitive
(`lower(value)`, uniquely indexed), so `KG` resolves to the built-in `kg` rather
than creating a second row. Only a value nothing matches is created, and it is
created in Title Case after whitespace is trimmed and collapsed:
`"  urban   FARMING "` becomes `Urban Farming`. The response echoes the
canonical spelling, which is what the client stores.

Validation, applied before anything is created:

| Rule | Tags | Units |
|---|---|---|
| Blank after trimming | `400 E04` | `400 E04` |
| Maximum length | 100 characters | 32 characters |
| Contains a comma | `400 E04` | allowed |
| Duplicate within one request | `400 E04`, compared case-insensitively | n/a |

Commas are refused in tags because the tag filter on every catalog read is a
comma-separated list; a tag containing one could never be selected back.

Concurrency is settled by the unique index rather than by the handler: two
requests submitting the same new value at the same time produce one row, and
both writes succeed.

### 12.3 Tag filters are still closed

Creation applies to **writes only**. The `tags` filter on the catalog reads
(`GET /api/v2/vouchers`, `/api/v2/pools`, `/api/v2/reports`,
`/api/v2/admin/reports`) still rejects a value outside the vocabulary with
`400 E04`. A filter naming a tag nothing carries is a client bug, and answering
it with an empty page hides that.

### 12.4 `GET /api/v2/admin/vocabulary`

`SUPERADMIN`-only. Lists **every value**, built-in and custom, each flagged by
`isCustom`. Only custom values can be deleted (§12.5); built-in tags can be
renamed (§12.6), so the console lists them too.

```json
{
  "ok": true,
  "description": "Vocabulary",
  "result": {
    "tags": [
      {
        "value": "Urban Farming", "isCustom": true,
        "deployedTokens": 0, "deployedPools": 0,
        "vouchers": 2, "pools": 0, "posts": 1, "offerings": 0
      }
    ],
    "units": [{ "value": "Produce Crate", "isCustom": true, "offerings": 4 }]
  }
}
```

The per-resource counts exist so the console can describe what a deletion or a
rename will reach before the operator commits to it.

### 12.5 `DELETE /api/v2/admin/vocabulary/tags` and `.../units`

`SUPERADMIN`-only. The value travels in a JSON body, not the path, because a
tag may contain spaces and any character but a comma:

```http
DELETE /api/v2/admin/vocabulary/tags
{ "value": "Urban Farming" }
```

The row is located case-insensitively and locked `FOR UPDATE`, then:

| Response | When |
|---|---|
| `404 E03` | no such value |
| `409 E16` | the value is a built-in |
| `200` | deleted, with the counts of what it removed |

```json
{
  "ok": true,
  "description": "Custom vocabulary value deleted",
  "result": {
    "value": "Urban Farming",
    "usage": { "deployedTokens": 0, "deployedPools": 0, "vouchers": 2, "pools": 0, "posts": 1, "offerings": 0 },
    "total": 3
  }
}
```

The two deletions differ in what they do to the content that referenced the
value, and the difference is deliberate:

- **A tag** is removed from every relation that carried it — `voucher_tags`,
  `pool_tags`, `report_tags`, `offering_tags`, `deployed_token_tags`,
  `deployed_pool_tags` — and the vouchers, pools, posts and offerings
  themselves are untouched.
- **A unit** is cleared to `NULL` on every offering that used it. The offerings
  survive; an operator tidying a vocabulary must not delete a seller's listing.

Both run in one transaction with the delete, so a value never disappears while
a row still points at it.

Deletion does not blacklist: the next catalog write that names the value creates
it again.

### 12.6 `PUT /api/v2/admin/vocabulary/tags` and `.../units`

`SUPERADMIN`-only. Renames a value everywhere it is used:

```http
PUT /api/v2/admin/vocabulary/tags
{ "value": "Urban Farming", "replacement": "Community Gardens" }
```

`value` is located case-insensitively and locked `FOR UPDATE`. `replacement` is
trimmed and its inner whitespace collapsed, but its casing is kept as sent, so a
rename can fix casing (`kwh` → `kWh`). It follows the creation rules otherwise:
at most 100 characters and no commas for a tag, at most 32 for a unit.

| Response | When |
|---|---|
| `400 E02` / `E04` | malformed body, blank value, or an invalid replacement |
| `404 E03` | no such value |
| `409 E16` | the replacement already exists (case-insensitively) |
| `409 E16` | the value is a built-in unit |
| `409 E16` | a queued or running voucher or pool deployment names the tag |
| `200` | renamed; `result.value` is the stored spelling |

Every reference follows in the same transaction: the tag and unit foreign keys
are `ON UPDATE CASCADE` (migration 040), so vouchers, pools, posts, offerings and
deployments keep their association under the new name.

- **Built-in tags** can be renamed and stay built-in, so they remain undeletable.
  This is how a deployment makes the shipped defaults its own.
- **Built-in units** cannot: the apps translate them by their stored spelling,
  and a rename would strip every locale's label from that unit.
- **A deployment job** saves its tags as names in the job's arguments, outside
  any foreign key, and writes them when it runs. A tag named by a job that can
  still run is refused until the job finishes; `completed`, `cancelled` and
  `discarded` jobs never run again and do not block.

Like deletion, a rename does not blacklist the old spelling: the next catalog
write that names it creates it as a new custom value.

## Recipient search

Authenticated `GET /api/v2/recipients?q=…` returns up to 20 recipient identities
in `result.recipients`. Text searches current usernames and effective pool names.
Phone-shaped input searches approved users with usernames by their stored phone
prefix, after at least three digits. An optional leading `+`, spaces, parentheses
and hyphens are ignored when comparing; every digit, including leading zeros
and `00`, is preserved. `071` is not converted to an international number.
Exact phone matches rank first, followed by username/address order. The existing
optional `kind=user|pool` parameter still applies; pool contact phones are excluded.
Phone numbers are not included in the response.

Sending to a complete wallet address uses direct address entry, with optional
`GET /api/v2/recipients/reverse/:address` for its current identity label. Unknown
addresses remain valid destinations; partial addresses do not trigger discovery.
