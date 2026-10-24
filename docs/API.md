# CLC Core API Reference

> **Version:** 2.0
> **License:** [AGPL-3.0](../LICENSE)

This document describes the HTTP API of the CLC Core service.
It lists each route with its access level, parameters, responses and errors.
The code is the source of truth. The generated Swagger files in this folder are a secondary reference.

## Table of contents

1. [Overview](#1-overview)
2. [Authentication](#2-authentication)
3. [Responses and errors](#3-responses-and-errors)
4. [Auth endpoints](#4-auth-endpoints)
5. [Relayer](#5-relayer)
6. [Contracts, tracking and system](#6-contracts-tracking-and-system)
7. [Vouchers and offerings](#7-vouchers-and-offerings)
8. [Pools](#8-pools)
9. [Curated catalogs](#9-curated-catalogs)
10. [Posts](#10-posts)
11. [Lookups and shared vocabulary](#11-lookups-and-shared-vocabulary)
12. [Account data](#12-account-data)
13. [Pretium](#13-pretium)
14. [Current user](#14-current-user)
15. [Admin](#15-admin)
16. [Route index](#16-route-index)

---

## 1. Overview

| Property | Value |
|---|---|
| Base URL | `https://example.com` (use the host of your deployment) |
| Auth routes | `/auth/*` |
| API routes | `/api/v2/*` |
| Content type | `application/json` |
| Maximum request body | 1 MB. A larger body returns `E08`. |
| Request time limit | 15 seconds. A slower request returns `E09`. |
| Sign-in method | Sign-In With Ethereum (SIWE, EIP-4361) or WebAuthn |

### 1.1 Terms

This document uses each term below for one meaning only.

| Term | Meaning |
|---|---|
| account | An Ethereum address. |
| user | A person with a registered account and a role. |
| caller | The party that sends a request. A caller is a user, a service or an anonymous client. |
| voucher | A token that an account issues. |
| pool | A swap pool contract. |
| relay | To send a transaction through the relayer. |

### 1.2 Access levels

Each route in this document has one access level.

| Level | Meaning |
|---|---|
| Public | No credential is required. |
| User session | The caller must be a user. A service token is rejected with `E10`. |
| User or service token | The caller must send a valid user or service credential. |
| Superadmin | The caller must be a user with the role `SUPERADMIN`. |

Some routes add checks, for example "the caller must own the voucher".
The route section lists these checks.

### 1.3 CORS

The service reads the list of allowed origins from its configuration.
For an allowed origin, the service sets `Access-Control-Allow-Credentials: true`.
The allowed request headers are `Origin`, `Content-Type`, `Accept` and `Authorization`.
Browsers can cache a preflight response for 86400 seconds.

---

## 2. Authentication

### 2.1 Credentials

The service accepts two kinds of credential on protected routes: a user JWT and a service JWT.
Both are EdDSA tokens. The service verifies them with one public key.

#### User JWT

The service issues a user JWT after a successful sign-in for an approved user.
The service sends the token in an `HttpOnly` cookie named `__clc_auth`.
A client can also send the same token in the header `Authorization: Bearer <token>`.

| Property | Value |
|---|---|
| Algorithm | EdDSA |
| Lifetime | 30 days |
| Cookie name | `__clc_auth` |
| Cookie path | `/` |
| Cookie `Secure` flag | Set in production mode only |
| Cookie `SameSite` | `None` in production mode, `Lax` otherwise |

The token has these claims:

| Claim | Type | Meaning |
|---|---|---|
| `userId` | number | Internal user identifier. |
| `ethereumAddress` | string | Address of the user. |
| `role` | string | `USER` or `SUPERADMIN`. |
| `iat`, `exp` | number | Issue and expiry time. |

The service checks the cookie first. If the cookie is missing or invalid, the service checks the `Authorization` header.

#### Service JWT

A service JWT is for server-to-server calls. An operator creates it with the `gen-service-token` command.

| Property | Value |
|---|---|
| Algorithm | EdDSA |
| Default lifetime | 365 days |
| Transport | Header `Authorization: Bearer <token>` or the legacy cookie `__ge_cust_auth` |

The token has these claims:

| Claim | Type | Meaning |
|---|---|---|
| `service` | boolean | Must be `true`. |
| `publicKey` | string | Identity of the service. |
| `sub`, `iss`, `iat`, `exp` | | Standard claims. |

To create a service token, run this command:

```bash
go run ./cmd/gen-service-token -config config.toml -service my-service
```

To use the token, send it in each request:

```http
Authorization: Bearer <token>
```

### 2.2 Caller roles

| Role | Credential | Notes |
|---|---|---|
| `USER` | User JWT | Can use every user route. |
| `SUPERADMIN` | User JWT | Can use every route, including `/api/v2/admin/*`. |
| `SERVICE` | Service JWT | Cannot use user-only routes. Gets `E10` there. |

A route with the level "User session" rejects service tokens.
A route with the level "Superadmin" rejects every caller whose role is not `SUPERADMIN`.
A missing or invalid credential on a protected route returns status 401 with code `E06`.

### 2.3 Sign-in flow

1. Send `POST /auth/nonce` to get a one-time nonce. The nonce is valid for a short time.
2. Build an EIP-4361 message that contains the nonce. Sign the message with the wallet.
3. Send the signed message to one of these routes:
   - `POST /auth/signup` creates an account and signs it in.
   - `POST /auth/signin` signs in an existing approved account.
   - `POST /auth/siwe/verify` creates an account for an unknown address and signs in.
4. On success, the service sets the `__clc_auth` cookie. The browser sends it with each later request.
5. Send `POST /auth/logout` to clear the cookie.

Section 4 describes each route and the message format.

### 2.4 Rate limits

Some public routes have a per-client limit.
Section 7.1 describes the market limit. Section 8.1 describes the pool limit.
A client that exceeds a limit gets status 429, code `E11` and a `Retry-After` header.

---

## 3. Responses and errors

Every response is a JSON object.
Use the `ok` field to decide if a request succeeded.

### 3.1 Success

```json
{
  "ok": true,
  "description": "Human-readable status message",
  "result": {}
}
```

The shape of `result` depends on the route.
Some list routes add paging data inside `result`.

### 3.2 Error

```json
{
  "ok": false,
  "description": "Human-readable error message",
  "code": "E04"
}
```

A few errors add a `result` object with data that the client can use.
For example, a response can carry the tracking id of a request that is already pending.

The `description` text is for people. Do not parse it.
Use the HTTP status and the `code` field in client logic.

### 3.3 Error codes

| Code | Meaning |
|---|---|
| `E01` | Internal server error. |
| `E02` | The request body is not valid JSON or has the wrong shape. |
| `E03` | The route does not exist. |
| `E04` | Validation failed on one or more fields. |
| `E05` | The method is not allowed. |
| `E06` | Authentication failed. |
| `E07` | No record found. |
| `E08` | The request body is too large. |
| `E09` | The request timed out. |
| `E10` | The caller is not allowed to do this. |
| `E11` | A rate limit or quota is exceeded. |
| `E12` | The relayer is disabled. |
| `E13` | An EIP-7702 authorization is required. |
| `E14` | The relayer executor does not match. |
| `E15` | WebAuthn verification failed. |
| `E16` | The request conflicts with existing data, for example a name that is in use. |
| `E17` | The email is linked to another account. |
| `E18` | The phone number is linked to another account. |
| `E19` | A private share or a confirmed send is not available. |

### 3.4 Common statuses

| Status | When it occurs |
|---|---|
| 200 | The request succeeded. |
| 201 | The request created a record, on routes that say so. |
| 202 | The service accepted the request and queued work. |
| 400 | The request is malformed or fails validation (`E02`, `E04`). |
| 401 | Authentication is missing or invalid (`E06`). |
| 403 | The caller lacks the right role or ownership (`E10`). |
| 404 | The route or the record does not exist (`E03`, `E07`). |
| 409 | The request conflicts with existing data (`E16`, `E17`, `E18`). |
| 413 | The body is larger than 1 MB (`E08`). |
| 429 | A rate limit is exceeded (`E11`). |
| 500 | An internal error occurred (`E01`). |

Each route section lists the statuses that are specific to that route.

---

## 4. Auth endpoints

The auth endpoints use Sign-In with Ethereum (SIWE, EIP-4361) and WebAuthn passkeys. A successful sign-in sets a session cookie. See section 2 for the cookie and token rules.

The routes in this section are not under `/api/v2`. They use the `/auth` prefix. Auth routes have no rate limit. The global request body limit is 1 MB and the request timeout is 15 seconds.

#### Session cookie set by sign-in

`POST /auth/signup`, `POST /auth/signin`, `POST /auth/siwe/verify` and `POST /auth/webauthn/verify` set the same cookie.

| Attribute | Value |
|---|---|
| Name | `__clc_auth` |
| Value | EdDSA JWT with claims `userId`, `ethereumAddress`, `role`, `exp`, `iat` |
| Lifetime | 30 days |
| `HttpOnly` | Always |
| `Path` | `/` |
| `Secure` | Set in production mode only |
| `SameSite` | `None` in production mode, `Lax` otherwise |

`POST /auth/logout` clears the cookie with the same attributes.

#### Account status

A user has the status `PENDING`, `APPROVED` or `REJECTED`. A sign-in for a `PENDING` or `REJECTED` user returns `403` with `E10`. The sign-up and unified verify routes create new users as `APPROVED`. A superadmin can change the status later (see section 15, Admin).

### SIWE message construction

The client builds the message and signs it with `personal_sign` (EIP-191). Follow these steps.

1. Send `POST /auth/nonce` or `POST /auth/siwe/nonce`. Keep the `nonce` value.
2. Build the message text. Use `\n` as the line separator.
3. Sign the exact message text with the wallet.
4. Send the message and the signature in the same request.

Use this layout.

```text
example.com wants you to sign in with your Ethereum account:
0x1111111111111111111111111111111111111111

Sign in to the platform

URI: https://example.com
Version: 1
Chain ID: 42220
Nonce: a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4
Issued At: 2026-01-01T00:00:00.000Z
```

The server checks only these parts of the message.

| Part | Rule |
|---|---|
| Line 2 | Must be the account address, with no other text. The server compares it with the address that the signature recovers. |
| Line `Nonce: <nonce>` | Must exist. The text after `Nonce: ` must be a nonce that the server issued. |
| Signature | Must be a `0x`-prefixed hex string of 65 bytes (`r`, `s`, `v`). The `v` byte can be `0`, `1`, `27` or `28`. |

The server does not check the domain, the URI, the chain ID, the statement or the time fields. The nonce is the replay protection. Use a clear domain and statement for the user.

Each nonce has these properties.

- The server makes the nonce from 16 random bytes. It has 32 hex characters.
- The server keeps nonces in memory. A nonce expires after 5 minutes.
- The server removes a nonce on first use. This includes a failed request. Request a new nonce for each attempt.
- A restart of the service removes all nonces.

### 4.1 `POST /auth/nonce` and `POST /auth/siwe/nonce`

**Access:** Public.

Neither route reads a request body.

Response `200`:

```json
{
  "ok": true,
  "description": "Nonce generated for SIWE challenge",
  "result": { "nonce": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4" }
}
```

### 4.2 `POST /auth/signup`

**Access:** Public.

Creates a user with the status `APPROVED` and the role `USER`. The response sets the session cookie. The caller does not need to call `POST /auth/signin` after sign-up.

Request body:

| Field | Type | Required | Rule |
|---|---|---|---|
| `message` | string | Yes | SIWE message. |
| `signature` | string | Yes | SIWE signature. |
| `name` | string | Yes | Display name. |
| `email` | string | Yes | Must be a valid email. The server stores it in lower case. |

The server runs these steps in order.

1. Recover the signer from the signature.
2. Compare the signer with the address on line 2 of the message.
3. Consume the nonce.
4. Check that the address and the email are not in use.
5. Create the user.

Response `200`:

```json
{
  "ok": true,
  "description": "Registration successful",
  "result": { "userId": 123, "status": "APPROVED", "role": "USER" }
}
```

Errors:

| Status | Code | Condition |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field fails validation. |
| `400` | `E04` | The signature is not valid, the signer does not match line 2, or the nonce is not valid or is expired. |
| `409` | `E16` | The address is already linked to a user. Use `POST /auth/signin` instead. |
| `409` | `E17` | The email is already linked to another user. The address is not registered. |
| `409` | `E16` or `E17` | Another request registered the same address or email at the same time. The code follows the constraint that failed. |

### 4.3 `POST /auth/signin`

**Access:** Public.

Signs in a registered user. The response sets the session cookie.

Request body:

| Field | Type | Required | Rule |
|---|---|---|---|
| `message` | string | Yes | SIWE message. |
| `signature` | string | Yes | SIWE signature. |

Response `200`:

```json
{
  "ok": true,
  "description": "Signed in successfully",
  "result": { "userId": 123, "role": "USER" }
}
```

Errors:

| Status | Code | Condition |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field is missing, the signature is not valid, the signer does not match line 2, or the nonce is not valid or is expired. |
| `404` | `E03` | The address is not registered. Call `POST /auth/signup` first. |
| `403` | `E10` | The user status is `PENDING` or `REJECTED`. |

### 4.4 `POST /auth/siwe/verify`

**Access:** Public.

Signs in a user and registers the user if the address is new. Use this route for wallet SDKs. The request has the same fields as `POST /auth/signin`.

A new address creates a user with the status `APPROVED`, the role `USER` and an empty name. The email is a placeholder of the form `pending+<address without 0x>@clc.local`. The caller can set real values with `POST /auth/profile`.

If the user status is `PENDING`, the server can approve the user later from the legacy federation data. The response is still `403`.

This route is the only sign-in route that returns the token in the body. The response also sets the session cookie.

Response `200`:

```json
{
  "ok": true,
  "description": "Authenticated successfully",
  "result": {
    "token": "<token>",
    "address": "0x1111111111111111111111111111111111111111",
    "expiresAt": "2026-02-01T00:00:00Z"
  }
}
```

`address` is the address as stored for the user. `expiresAt` is 30 days after the response, in RFC 3339 format.

Errors:

| Status | Code | Condition |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field is missing. |
| `400` | `E04` | The nonce is not valid or is expired. |
| `401` | `E04` | The signature is not valid, or the signer does not match line 2. |
| `403` | `E10` | The user status is `PENDING` or `REJECTED`. |

### 4.5 `POST /auth/logout`

**Access:** Public.

Clears the session cookie. The route does not read a body. The route succeeds when the caller has no session. The server keeps no token blocklist. A copy of the token stays valid until it expires.

Response `200`:

```json
{ "ok": true, "description": "Signed out successfully", "result": {} }
```

### 4.6 `POST /auth/webauthn/verify`

**Access:** Public.

Signs in with a passkey. The request does not need a SIWE message. The assertion is the proof. The response sets the session cookie.

Request body. All fields are required strings.

| Field | Description |
|---|---|
| `credentialId` | The credential ID that the wallet registered. |
| `authenticatorData` | Base64url. Padded and unpadded forms work. At least 37 bytes after decoding. |
| `clientDataJSON` | Base64url. The type must be `webauthn.get`. The origin must be on the allowed origin list. |
| `signature` | Base64url, DER-encoded ECDSA P-256 signature. |

Get the challenge from `GET /api/v2/relayer/webauthn/challenge` (see section 5). The challenge is in `clientDataJSON`. The server consumes the challenge before it checks the signature. The assertion must have the user-present and user-verified flags set.

The server finds an active credential for `credentialId`, verifies the assertion, and finds the user for the credential address. It then checks the user status.

Response `200`:

```json
{
  "ok": true,
  "description": "Signed in successfully",
  "result": {
    "userId": 123,
    "role": "USER",
    "address": "0x1111111111111111111111111111111111111111",
    "keyHash": "0x0000000000000000000000000000000000000000000000000000000000000001",
    "publicKeyX": "1",
    "publicKeyY": "2"
  }
}
```

`publicKeyX` and `publicKeyY` are decimal strings.

Errors:

| Status | Code | Condition |
|---|---|---|
| `400` | `E02` or `E04` | The body is not valid JSON, or a field is missing. |
| `404` | `E07` | No active credential has this `credentialId`. |
| `401` | `E15` | The assertion is not valid. The challenge can be unknown, expired or used. The origin can be wrong. The signature can be wrong. |
| `404` | `E03` | The credential address has no user. |
| `403` | `E10` | The user status is `PENDING` or `REJECTED`. |

### 4.7 `GET /auth/session`

**Access:** Public. The route reads the `__clc_auth` cookie. It ignores the `Authorization` header.

Returns the session of the caller. This response does not use the standard envelope.

Response `200`:

```json
{
  "address": "0x1111111111111111111111111111111111111111",
  "chainId": 42220,
  "role": "USER"
}
```

`address` is in EIP-55 checksum form. `role` is `USER` or `SUPERADMIN`.

Response `204` with no body: the cookie is missing or is not valid.

### 4.8 `GET /auth/webauthn/credentials`

**Access:** User session. The route reads the `__clc_auth` cookie. A service token does not work.

Lists the passkeys that are mapped to the account of the session. The server reads only stored data. It does not read the chain. The order is newest first.

Response `200`:

```json
{
  "ok": true,
  "description": "WebAuthn credentials",
  "result": {
    "address": "0x1111111111111111111111111111111111111111",
    "credentials": [
      {
        "credentialId": "abc",
        "state": "pending",
        "createdAt": "2026-01-01T00:00:00Z",
        "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
      }
    ]
  }
}
```

| Field | Type | Description |
|---|---|---|
| `credentialId` | string | The credential ID. |
| `state` | string | `pending`, `active`, `failed`, `legacy` or `unverified`. |
| `createdAt` | string | RFC 3339 time. |
| `trackingId` | string or null | Set only while `state` is `pending`. Use it with `GET /api/v2/relayer/track/:trackingId`. |

A credential is `pending` until the registration transaction is confirmed. It becomes `active` on success and `failed` on a revert. Only `active` credentials work with `POST /auth/webauthn/verify` and `POST /api/v2/relayer/webauthn/recover`.

Errors:

| Status | Code | Condition |
|---|---|---|
| `401` | `E06` | The cookie is missing, or is not valid, or has no valid address. |

### 4.9 `GET /auth/profile` and `POST /auth/profile`

**Access:** User session. A service token returns `403` with `E10`. A caller without a valid credential gets `401` with `E06`.

These two routes read and write the profile of the signed-in user. The address comes from the session.

#### `GET /auth/profile`

Response `200`:

```json
{
  "ok": true,
  "description": "User profile",
  "result": {
    "address": "0x1111111111111111111111111111111111111111",
    "name": "Alice",
    "username": "alice",
    "email": "alice@example.com",
    "phone": "+254700000000",
    "country": "KE",
    "preferredCurrency": "USD",
    "bio": "Farmer",
    "website": "https://example.com",
    "locationName": "Nairobi, Kenya",
    "geoLat": -1.286389,
    "geoLng": 36.817223,
    "avatarUrl": "https://example.com/avatar.webp",
    "createdAt": "2026-01-01T00:00:00Z"
  }
}
```

`username`, `preferredCurrency`, `bio`, `website`, `locationName`, `geoLat`, `geoLng` and `avatarUrl` can be `null`. The server stores no value for an empty `email` or `phone` and returns an empty string.

Error: `404` with `E03` if the user does not exist.

#### `POST /auth/profile`

Saves the profile and returns the saved values. The response has the same fields as `GET /auth/profile` except `createdAt`.

Request body:

| Field | Type | Required | Rule |
|---|---|---|---|
| `name` | string | No | Maximum 80 characters. Omitted sets an empty name. |
| `email` | string | No | Must be a valid email. Omitted or empty clears the email. |
| `phone` | string | No | Omitted or empty clears the phone. |
| `country` | string | No | Exactly 2 characters if sent. Omitted sets an empty value. |
| `username` | string | No | 1 to 80 characters after trimming. The server applies Unicode NFC. Omitted keeps the current value. |
| `preferredCurrency` | string | No | Maximum 32 characters. Omitted keeps the value. See the note below. |
| `bio` | string | No | Maximum 500 characters. Omitted keeps the value. An empty string clears it. |
| `website` | string | No | Must be an `http` or `https` URL, or an empty string. Omitted keeps the value. An empty string clears it. |
| `locationName` | string | No | Maximum 200 characters. Must not be blank. Omitted keeps the value. |
| `geoLat` | number | No | -90 to 90. Send with `geoLng`. |
| `geoLng` | number | No | -180 to 180. Send with `geoLat`. |
| `clearLocation` | boolean | No | If `true`, the server clears `locationName`, `geoLat` and `geoLng`. |
| `avatarUrl` | string | No | Must be an `http` or `https` URL. Omitted keeps the value. |
| `clearAvatar` | boolean | No | If `true`, the server clears `avatarUrl`. |

Rules:

- `name`, `email`, `phone` and `country` replace the stored values on every call. Send the full current value to keep it.
- If the user has no username and `name` is not blank, the server picks a username from `name`. If no free name exists, the profile still saves.
- The server trims `bio`, `website` and `locationName`. It collapses repeated spaces in `preferredCurrency`.
- `preferredCurrency` must be a unit of account.
  If the unit does not exist, the server creates it as a custom unit.
  A new unit gets the canonical spelling (each word capitalized).
  An existing unit matches without regard to case.
  The unit can have at most 32 characters.
- `clearLocation` cannot be sent with `locationName`, `geoLat` or `geoLng`.
- `clearAvatar` cannot be sent with `avatarUrl`.
- `geoLat` and `geoLng` must be sent together.

Errors:

| Status | Code | Condition |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field fails a rule above. The description names the problem. |
| `404` | `E03` | The user does not exist. |
| `409` | `E17` | The email is linked to another user. |
| `409` | `E18` | The phone is linked to another user. |
| `409` | `E16` | The username is already in use. |

### 4.10 `POST /auth/storage-token`

**Access:** User session. A service token returns `403` with `E10`.

Returns the session token of the caller so that the caller can send it to the image storage service. The storage service reads only an `Authorization` header. A browser script cannot read the `HttpOnly` cookie.

The route does not read a body. The route returns the same token that authenticated the request. It does not make a new token. Two calls with the same session return the same `token` and `expiresAt`.

The response has the header `Cache-Control: no-store`.

Response `200`:

```json
{
  "ok": true,
  "description": "Storage token issued",
  "result": { "token": "<token>", "expiresAt": "2026-02-01T00:00:00Z" }
}
```

The token is the full session token, not a limited one. A leaked copy gives API access as the user until it expires. The client must keep the token in memory only. The client must not write it to `localStorage`, `sessionStorage` or any cache.

Errors:

| Status | Code | Condition |
|---|---|---|
| `401` | `E06` | The caller has no valid credential. |
| `403` | `E10` | The caller uses a service token. |
| `401` | `E06` | The credential is not a user token that the route can read. |

## 5. Relayer

The relayer sends transactions for a user who has no gas. The user signs. The relayer pays the gas and sends the transaction. The wallet is an EIP-7702 account that delegates to the Calibur contract.

All relayer routes have the prefix `/api/v2/relayer`. They are public. They need no cookie and no token. The proof of authority is the signature in the request. All relayer routes use the standard envelope (see section 3).

### 5.1 Flow

1. Build a list of 1 to 16 calls.
2. Call `GET /api/v2/relayer/health` to get the `relayerAddress`. Use it as the `executor`.
3. Call `POST /api/v2/relayer/relay/preview` to get the `nonce` and the `digest`.
4. Sign the `digest` with the root key or a passkey.
5. Call `POST /api/v2/relayer/relay` with the signed batch.
6. Poll `GET /api/v2/relayer/track/:trackingId` until the transaction lands.

For the first relay of a wallet, add an `authorization`. See `POST /api/v2/relayer/relay`.

#### Data types

All numbers in requests and responses are decimal strings. Addresses in requests must use the EIP-55 checksum form. An address that has no letters, such as `0x1111111111111111111111111111111111111111`, always passes.

A call:

| Field | Type | Rule |
|---|---|---|
| `to` | string | Required. Checksummed address. |
| `value` | string | Required. Decimal number of wei. |
| `data` | string | Required. Must start with `0x`. |

A batched call:

| Field | Type | Rule |
|---|---|---|
| `calls` | array | Required. 1 to 16 calls. The relayer runs them in order. |
| `revertOnFailure` | boolean | If `true`, the whole batch reverts when one call fails. |

A signed batch:

| Field | Type | Rule |
|---|---|---|
| `batchedCall` | object | Required. A batched call. |
| `nonce` | string | Required. Decimal number. Use the `nonce` from `POST /api/v2/relayer/relay/preview`. |
| `keyHash` | string | Required. `0x` and 64 hex characters. All zeros for the root key. |
| `executor` | string | Required. Checksummed address. Must be the `relayerAddress` or the zero address. |
| `deadline` | string | Required. Decimal number. Unix seconds. `"0"` means no deadline. |

An authorization:

| Field | Type | Rule |
|---|---|---|
| `chainId` | string | Required. Decimal number. Must equal the chain of the service. |
| `contractAddress` | string | Required. Checksummed address. Must be the Calibur address. |
| `nonce` | string | Required. Decimal number. The EOA nonce from `GET /api/v2/relayer/nonce/:address`. |
| `yParity` | number | `0` or `1`. |
| `r` | string | Required. `0x` and 64 hex characters. |
| `s` | string | Required. `0x` and 64 hex characters. |

### 5.2 `GET /api/v2/relayer/health`

**Access:** Public.

Returns the state of the relayer.

Response `200`:

```json
{
  "ok": true,
  "description": "Relayer status",
  "result": {
    "status": "healthy",
    "build": "<build>",
    "chainId": 42220,
    "relayerAddress": "0x1111111111111111111111111111111111111111",
    "idempotentRelay": true
  }
}
```

`relayerAddress` is the address of the signer that sends the transactions. Use it as `executor`. `idempotentRelay` is always `true`. It shows that `POST /api/v2/relayer/relay` accepts `operationId`.

### 5.3 `GET /api/v2/relayer/nonce/:address`

**Access:** Public.

Returns the transaction nonce of an account on the chain. Use it as the `nonce` of an EIP-7702 authorization.

| Path parameter | Rule |
|---|---|
| `address` | Required. Checksummed address. |

Response `200`:

```json
{
  "ok": true,
  "description": "EOA nonce",
  "result": { "address": "0x1111111111111111111111111111111111111111", "nonce": "0" }
}
```

Errors: `400` with `E04` if `address` is not a valid checksummed address. A failed chain read returns `500` with `E01`.

### 5.4 `GET /api/v2/relayer/calibur-nonce/:address` and `GET /api/v2/relayer/calibur-nonce/:address/:keyHash`

**Access:** Public.

Returns the Calibur sequence number for the account and key. Without `keyHash`, the route uses the root key (all zeros).

| Path parameter | Rule |
|---|---|
| `address` | Required. Checksummed address. |
| `keyHash` | Optional. `0x` and 64 hex characters. |

Response `200`:

```json
{
  "ok": true,
  "description": "Calibur sequence",
  "result": {
    "address": "0x1111111111111111111111111111111111111111",
    "keyHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
    "nonce": "0"
  }
}
```

`nonce` is the sequence only.
For a non-zero `keyHash`, the full nonce in a signed batch is different.
It is the first 24 bytes of the key hash, shifted left by 64 bits, combined with the sequence.
For the root key, the full nonce equals the sequence.
Use `POST /api/v2/relayer/relay/preview` to get the full nonce.

Errors: `400` with `E04` if a path parameter is not valid. A failed chain read returns `500` with `E01`.

### 5.5 `POST /api/v2/relayer/relay/preview`

**Access:** Public.

Returns the full nonce and the EIP-712 digest for a batch.
The call is read-only. It queues nothing and needs no signature.
A signature over this digest matches the batch that the relayer submits.
The client can also compute the digest itself.

Request body:

| Field | Type | Required | Rule |
|---|---|---|---|
| `userAddress` | string | Yes | Checksummed address of the wallet. |
| `batchedCall` | object | Yes | A batched call with 1 to 16 calls. |
| `keyHash` | string | No | `0x` and 64 hex characters. Omitted or empty means the root key. |
| `executor` | string | Yes | Checksummed address. |
| `deadline` | string | Yes | Decimal number. |

Response `200`:

```json
{
  "ok": true,
  "description": "Relay batch preview",
  "result": {
    "nonce": "0",
    "digest": "0x<64 hex characters>"
  }
}
```

Put `nonce` in `signedBatch.nonce`. Sign `digest`. Do not change any field after the preview. A change gives a different digest.

Errors: `400` with `E02` for a body that is not valid JSON. `400` with `E04` for a field that fails a rule. A failed chain read returns `500` with `E01`.

### 5.6 `POST /api/v2/relayer/relay`

**Access:** Public.

Queues a signed batch for the relayer. The route returns after the job is queued. The relayer builds, signs and sends the transaction later. The batch can have 1 to 16 calls. There is no separate batch route. One signature covers all calls.

Request body:

| Field | Type | Required | Rule |
|---|---|---|---|
| `operationId` | string | No | UUID. Makes the request idempotent. See below. |
| `userAddress` | string | Yes | Checksummed address of the wallet. |
| `signedBatch` | object | Yes | A signed batch. |
| `signature` | string | Yes | Must start with `0x`. ABI encoding of `(bytes signature, bytes hookData)`. |
| `signatureType` | string | Yes | `secp256k1` or `webauthn`. |
| `authorization` | object | No | An authorization. Required for the first relay of a wallet. |
| `credentialId` | string | No | Registers a passkey. See below. |
| `publicKeyX` | string | No | Decimal number. P-256 x coordinate. |
| `publicKeyY` | string | No | Decimal number. P-256 y coordinate. |

Response `200`:

```json
{
  "ok": true,
  "description": "Relay request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "valuationSnapshots": [],
    "operationId": "6f1c0a52-8d4e-4b87-9f3a-2d1b7c5e9a10",
    "replayed": false
  }
}
```

`operationId` and `replayed` are present only if the request has an `operationId`. `valuationSnapshots` is a list of voucher claim records that the server stores before it sends the transaction. The server fills the list only for these two batch shapes.

- One zero-value ERC-20 `transfer` call.
- A pool swap route. Each hop has three calls: `approve` to zero, `approve` for the amount, and the pool `withdraw`.

For other batches the list is empty.

#### Rules that the server checks before it queues the job

The server does not check the signature of a normal batch. The chain checks it.
The server does not check the delegation state, the nonce, the deadline or the executor.
A fault in these values appears when the relayer builds the transaction. The failed job has no transaction.
In this case `GET /api/v2/relayer/track/:trackingId` keeps returning `404`.
To avoid such faults, use `POST /api/v2/relayer/relay/preview` and the `executor` value from `GET /api/v2/relayer/health`.

The relayer rejects a batch when the `executor` is not the zero address and is not the `relayerAddress`.

#### First relay for a wallet

If the wallet is not delegated, the request needs an `authorization`.
The relayer then sends one transaction that delegates the wallet and runs the batch.

- `authorization.contractAddress` must be the Calibur address.
- `authorization.chainId` must equal the chain of the service.
- `authorization.nonce` must be the EOA nonce from `GET /api/v2/relayer/nonce/:address`. It is not the Calibur sequence.

Later relays omit the `authorization`.
If the wallet is not delegated and the request has no `authorization`, the job fails. The track route then returns `404`.

#### Passkey registration

A relay can register a passkey for the wallet. Send `credentialId`, `publicKeyX` and `publicKeyY` together. A request with only some of the three fields returns `400` with `E04`.

The route is public. Therefore the server checks that the root key of the wallet allowed the registration. The server rejects the request with `400` and `E04` unless all of these are true.

- `signatureType` is `secp256k1` and `signedBatch.keyHash` is all zeros.
- The signature recovers to `userAddress` over the batch digest.
- `signedBatch.batchedCall.revertOnFailure` is `true`.
- The batch has exactly one call. The call goes to `userAddress`, has value `0`, and has the data of `register` for a WebAuthn P-256 key with the supplied coordinates.
- The coordinates are positive decimal numbers on the P-256 curve.

The server computes the key hash itself. The credential state is `pending`. It becomes `active` when the transaction succeeds and `failed` when it reverts. Use `GET /auth/webauthn/credentials` to read the state.

If the `credentialId` already exists, the server returns `409` with `E16`.
This happens when the credential maps to a different wallet or key, or is already `pending` or `active`.
A credential in state `failed` or `unverified` for the same wallet and key can register again.
For a `pending` credential of the same mapping, the error body has `result.trackingId`.
Follow that tracking ID. Do not relay again.

```json
{
  "ok": false,
  "description": "webauthn registration is already pending",
  "code": "E16",
  "result": { "state": "pending", "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479" }
}
```

#### Idempotent submission with `operationId`

A response can get lost. Send an `operationId` (a UUID that you make once for each payment). Keep the signed request. If the response does not arrive, send the identical request again.

| Request | Status | Result |
|---|---|---|
| New `operationId` | `200` | The job is queued. `replayed` is `false`. |
| Same `operationId`, identical request | `200` | The response has the first `trackingId` and `replayed: true`. The description is `Relay request already accepted`. The server queues nothing. |
| Same `operationId`, any other request | `409` with `E16` | The ID is bound to its first request. |
| `operationId` is not a UUID | `400` with `E04` | None. |

The server binds the ID to the chain, `userAddress` and every other field of the request.
The comparison is exact, including the case of hex strings. Send the same bytes.
There is no lookup by `operationId` alone.
A request without an `operationId` always queues a job.
Two requests with the same `operationId` at the same time are safe. One queues the job. The other returns its tracking ID.

The server checks the `operationId` before the passkey registration conflict. A replay of a registration returns its first tracking ID.

#### Gas budget

The server cannot estimate gas for the first relay because the wallet is not yet delegated. The relayer sets the gas limit from a budget.

```text
limit = (base_exec_gas
         + per_call_gas * number_of_calls
         + delegation_overhead_gas   (only if the wallet is not delegated)
         + webauthn_verify_gas       (only if signatureType is webauthn)) * 1.10
```

The limit has an upper bound of `max_relay_gas`. The settings are in the `[relayer]` section of the service configuration. A setting that is `0` or missing uses the default.

| Setting | Default | Meaning |
|---|---|---|
| `base_exec_gas` | 50,000 | Fixed cost of one relayed batch. |
| `per_call_gas` | 200,000 | Cost for each call. It covers the heaviest single action, a first token mint. |
| `delegation_overhead_gas` | 150,000 | Added when the relay also delegates the wallet. |
| `webauthn_verify_gas` | 400,000 | Added for a passkey signature. P-256 verification is costly. |
| `max_relay_gas` | 4,000,000 | Upper bound. A batch of 16 calls fits in the default bound. |
| `delegate_gas_limit` | none | Limit for a bare delegation. The service requires this setting. The code default of 200,000 applies only if the value is 0. |

The user pays no gas. The relayer pays the gas. The gas price comes from the gas oracle of the service.

#### Errors

| Status | Code | Condition |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field fails a rule, for example more than 16 calls, an address that is not checksummed, or `signatureType` that is not `secp256k1` or `webauthn`. |
| `400` | `E04` | The passkey registration is incomplete or is not valid. |
| `409` | `E16` | The `operationId` was used for a different request. |
| `409` | `E16` | The `credentialId` is already mapped. |

### 5.7 `POST /api/v2/relayer/delegate`

**Access:** Public.

Delegates a wallet to Calibur without any call. Use it when the wallet must be a smart account before the first relay.

Request body:

| Field | Type | Required | Rule |
|---|---|---|---|
| `ephemeralAddress` | string | Yes | Checksummed address of the wallet. |
| `authorization` | object | Yes | An authorization. |

The server queues a delegation job. It also queues a job that registers the address on the account index, unless the delegation job is a duplicate.
A delegation job is a duplicate when the same `ephemeralAddress` already has a job. The server then returns the first `trackingId`.

Response `200`:

```json
{
  "ok": true,
  "description": "Delegate request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "registerTrackingId": "0d3f5c1e-2b4a-4c6d-8e9f-1a2b3c4d5e6f",
    "duplicate": false
  }
}
```

If `duplicate` is `true`, `registerTrackingId` is an empty string.
The registration job does not send a transaction if the service has no account index address.
`registerTrackingId` is for the account registration transaction. `GET /api/v2/relayer/track/:trackingId` returns `403` for that ID.
Use the `trackingId` for the delegation.

Errors: `400` with `E02` or `E04` for a bad body. An internal fault returns `500` with `E01`.

### 5.8 `GET /api/v2/relayer/track/:trackingId`

**Access:** Public.

Returns the transactions for a tracking ID. A relay can have more than one transaction when the service replaces a transaction. The order is oldest first.

| Path parameter | Rule |
|---|---|
| `trackingId` | Required. UUID. |

Response `200`:

```json
{
  "ok": true,
  "description": "Relayer OTX chain",
  "result": {
    "otx": [
      {
        "id": 1,
        "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "otxType": "RELAY_EXEC",
        "signerAccount": "0x1111111111111111111111111111111111111111",
        "rawTx": "0x<raw transaction>",
        "txHash": "0x<transaction hash>",
        "nonce": 7,
        "replaced": false,
        "createdAt": "2026-01-01T00:00:00Z",
        "updatedAt": "2026-01-01T00:00:05Z",
        "status": "SUCCESS"
      }
    ]
  }
}
```

| Field | Description |
|---|---|
| `otxType` | `RELAY_DELEGATE` (bare delegation), `RELAY_EXEC` (batch for a delegated wallet) or `RELAY_DELEGATE_EXEC` (delegation and batch). |
| `signerAccount` | The relayer account that signed the transaction. |
| `nonce` | The nonce of the relayer account. |
| `replaced` | `true` if a later transaction replaced this one. |
| `status` | The dispatch status. See the list below. |

Dispatch status values: `PENDING`, `IN_NETWORK`, `SUCCESS`, `REVERTED`, `LOW_NONCE`, `NO_GAS`, `LOW_GAS_PRICE`, `NETWORK_ERROR`, `REPLACEMENT_UNDERPRICED`, `UNKNOWN_RPC_ERROR`, `POOL_FULL`, `POOL_CONFLICT`, `NONCE_GAP`, `INVALID_TX`. `SUCCESS` means the chain confirmed the transaction. `REVERTED` means the transaction ran and failed.

The route returns only relay transaction types. A tracking ID of another type returns `403` with `E10`.

Errors:

| Status | Code | Condition |
|---|---|---|
| `400` | `E04` | `trackingId` is not a UUID. |
| `404` | `E03` | No transaction exists for the tracking ID. This is normal for a short time after `POST /api/v2/relayer/relay`, because the relayer writes the record after the job runs. Poll again. |
| `403` | `E10` | The tracking ID belongs to a transaction that is not a relay type. |

### 5.9 `GET /api/v2/relayer/webauthn/challenge`

**Access:** Public.

Returns a challenge for a passkey assertion. The challenge has 32 random bytes in base64url form without padding (43 characters).

Response `200`:

```json
{
  "ok": true,
  "description": "WebAuthn challenge",
  "result": { "challenge": "<43 characters>" }
}
```

The server keeps the challenge in memory for 5 minutes. The challenge works once. The server removes it on the first use, also when the verification fails. A restart removes all challenges. The challenge store is for a single instance of the service.

### 5.10 `POST /api/v2/relayer/webauthn/recover`

**Access:** Public.

Returns the wallet that belongs to a passkey. The caller proves that it holds the passkey. The route does not set a session cookie. Use `POST /auth/webauthn/verify` to sign in.

The request body and the assertion rules are the same as for `POST /auth/webauthn/verify`. All four fields are required: `credentialId`, `authenticatorData`, `clientDataJSON` and `signature`.

Response `200`:

```json
{
  "ok": true,
  "description": "Wallet recovered",
  "result": {
    "address": "0x1111111111111111111111111111111111111111",
    "keyHash": "0x0000000000000000000000000000000000000000000000000000000000000001",
    "publicKeyX": "1",
    "publicKeyY": "2"
  }
}
```

Errors:

| Status | Code | Condition |
|---|---|---|
| `400` | `E02` or `E04` | The body is not valid JSON, or a field is missing. |
| `404` | `E07` | No active credential has this `credentialId`. |
| `401` | `E15` | The assertion is not valid. |

---

## 6. Contracts, tracking and system

These routes deploy and upgrade contracts, track origin transactions (OTX), and report system state.
All routes in this section are under the `/api/v2` prefix and need authentication (see section 2, Authentication).
A missing or invalid credential returns `401` with code `E06`.

Deploy and upgrade requests are asynchronous.
The server queues a job and returns a `trackingId` with status `200`.
Use `GET /api/v2/otx/track/:trackingId` to follow the transactions.
Use `GET /api/v2/otx/track/:trackingId/contract` to get the deployed contract.

Address fields in request bodies and path parameters must be valid EIP-55 checksummed addresses.
An address with wrong letter case fails validation with code `E04`.

### 6.1 `POST /api/v2/contracts/token`

**Access:** User or service token. A user caller counts against the voucher limit. A superadmin or service token has no limit.

Queues a job that deploys a voucher (ERC-20 proxy).

**Request body (JSON):**

| Field | Type | Required | Rules |
|---|---|---|---|
| `name` | string | Yes | Trimmed, inner spaces collapsed. Must not be blank. Maximum 120 characters. |
| `symbol` | string | Yes | Must be unique (see Name rules below). |
| `decimals` | string | Yes | Send an integer from 0 to 255 as a string. The API does not check the format (see Late checks). |
| `owner` | string | Yes | Checksummed address. |
| `expiryTimestamp` | string | No | Unix time in seconds, as a string. The API does not check the format (see Late checks). |
| `description` | string | No | No limit in the API. |
| `website` | string | No | Must be a URL. |
| `email` | string | No | Must be an email address. |
| `phone` | string | No | No check. |
| `terms` | string | No | Maximum 5000 characters. A link or Markdown text. |
| `voucherUoa` | string | No | Unit of account. Maximum 32 characters. See Unit of account below. |
| `voucherValue` | number | No | Value of one voucher unit in the unit of account. Must be 0 or more. |
| `locationName` | string | No | No limit in the API. |
| `geoLat` | number | No | From -90 to 90. |
| `geoLng` | number | No | From -180 to 180. |
| `iconUrl` | string | No | Must be an `http` or `https` URL. |
| `bannerUrl` | string | No | Must be an `http` or `https` URL. |
| `markerIcon` | string | No | One of `basket`, `hand-coin`, `coupon`, `food`, `farm`, `tools`, `health`, `education`. |
| `markerColor` | string | No | One of `blue`, `purple`, `orange`, `pink`, `cyan`, `brown`. |
| `tags` | array of string | No | See Tags below. |

**Name rules.**
The API trims `name` and collapses inner white space before it checks anything.
The deployed contract receives the cleaned name.
The name must not match another voucher name, including a deploy in progress.
The comparison ignores case and extra white space.
The `symbol` is reserved in a shared claim table, together with pool symbols.
A deploy that fails releases its name and symbol.

**Tags.**
Each tag has inner white space collapsed.
A tag must not be blank, must not contain a comma, and must have 100 characters or fewer.
A tag must not repeat inside the list (the comparison ignores case).
A tag that does not exist is created as a custom tag.
The API stores the tags sorted.

**Unit of account.**
If `voucherUoa` is not blank, the API finds an existing unit with the same name (ignoring case) or creates a custom one.
Custom values are written in title case.

**Late checks.**
The API does not check `decimals` and `expiryTimestamp`.
The worker reads them after the response.
A bad value makes the deploy job fail and the job is not retried.
The voucher never appears in that case.

**Limit.**
For a user caller who is not a superadmin, the API checks the `maxTokens` limit before it queues the job.
A new user starts with a limit of 1 voucher.
A superadmin can change the limit.
A voucher counts until its deploy fails on chain or the voucher is retired.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Token deploy request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
  }
}
```

For a user caller, the API records the tracking ID for the user.
The user can then track it, and it shows in `GET /api/v2/users/me/deployments`.

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | A field fails validation. Examples: bad address, `name` blank or longer than 120, bad `markerIcon`, bad tag, bad unit of account. |
| 401 | `E06` | No valid credential. |
| 404 | `E07` | No platform contract row exists for the configured chain. |
| 409 | `E16` | The voucher name is in use, or the `name` or `symbol` is already claimed. |
| 429 | `E11` | The user reached the voucher limit. The `result` object has `resource` (`vouchers`), `used` and `limit`. |

### 6.2 `POST /api/v2/contracts/pool`

**Access:** User or service token. A user caller counts against the pool limit. A superadmin or service token has no limit.

Queues a job that deploys a pool.

**Request body (JSON):**

| Field | Type | Required | Rules |
|---|---|---|---|
| `name` | string | Yes | Trimmed, inner spaces collapsed. Must not be blank. Maximum 80 characters. |
| `symbol` | string | Yes | Must be unique (see Name rules below). |
| `decimals` | string | Yes | Send an integer from 0 to 255 as a string. The API does not check the format. |
| `owner` | string | Yes | Checksummed address. |
| `quoterType` | string | Yes | One of `decimal`, `oracle`, `relative`. |
| `feesDecoupled` | boolean | No | Default `false`. |
| `feePolicyDefault` | string | Yes | Default fee as a percentage. A number from 0 to 100, for example `"0.5"`. |
| `feeAddress` | string | No | Checksummed address that receives the fees. Needs a `feePolicyDefault` above 0. |
| `baseCurrency` | string | If `quoterType` is `oracle` | Checksummed address. Must not be the zero address. |
| `unitOfAccount` | string | Yes | Maximum 32 characters. See Unit of account below. |
| `description` | string | No | No limit in the API. |
| `website` | string | No | Must be a URL. |
| `email` | string | No | Must be an email address. |
| `phone` | string | No | No check. |
| `terms` | string | No | Maximum 5000 characters. |
| `locationName` | string | No | No limit in the API. |
| `geoLat` | number | No | From -90 to 90. |
| `geoLng` | number | No | From -180 to 180. |
| `imageUrl` | string | No | Must be an `http` or `https` URL. |
| `bannerUrl` | string | No | Must be an `http` or `https` URL. |
| `tags` | array of string | No | Same rules as for `POST /api/v2/contracts/token`. |

**Quoter types.**

| Value | Meaning |
|---|---|
| `decimal` | Prices tokens one to one. It adjusts only for token decimals. |
| `relative` | Each voucher has a rate relative to the others. |
| `oracle` | Prices come from an oracle. `baseCurrency` is required. |

`quoterType` has no default. The API rejects a request without it.

**Name rules.**
The API trims `name` and collapses inner white space before it checks anything.
The pool name must not match another pool name, a user name, or a name of a deploy in progress.
The comparison ignores case and extra white space.
The `symbol` is reserved in the same claim table as voucher symbols.
A deploy that fails releases its name and symbol.

**Unit of account.**
The API finds an existing unit with the same name (ignoring case) or creates a custom one.
A custom unit cannot be used with `quoterType` `oracle`.

**Limit.**
For a user caller who is not a superadmin, the API checks the `maxPools` limit before it queues the job.
A new user starts with a limit of 10 pools.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Pool deploy request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479"
  }
}
```

A pool deploy sends more than one transaction under one tracking ID.

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | A field fails validation. Examples: `name` blank or longer than 80, `quoterType` missing or unknown, `feePolicyDefault` not a number from 0 to 100, `feeAddress` set with a fee of 0, `baseCurrency` missing, invalid or zero for `oracle`, custom unit with `oracle`. |
| 401 | `E06` | No valid credential. |
| 404 | `E07` | A platform contract row is missing for the configured chain. |
| 409 | `E16` | The pool name or symbol is already in use. |
| 429 | `E11` | The user reached the pool limit. The `result` object has `resource` (`pools`), `used` and `limit`. |

### 6.3 `POST /api/v2/contracts/version`

**Access:** Service token only. A user caller gets `403`.

Sets the implementation address and version string for a platform contract on the configured chain.

**Request body (JSON):**

| Field | Type | Required | Rules |
|---|---|---|---|
| `version` | string | Yes | Version label. |
| `contractType` | string | Yes | `ERC20`, `ERC20_DEMURRAGE`, `SWAPPOOL`, `TOKEN_UNIQUE_SYMBOL_INDEX`, `LIMITER`, `FEE_POLICY`, `ORACLE_QUOTER` or `RELATIVE_QUOTER`. |
| `implementationAddress` | string | Yes | Checksummed address. Must not be the zero address. |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Contract version updated",
  "result": {
    "contractType": "ERC20",
    "implementationAddress": "0x1111111111111111111111111111111111111111",
    "version": "v1.0.0"
  }
}
```

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | A field fails validation, or the address is not a valid non-zero address. |
| 401 | `E06` | No valid credential. |
| 403 | `E04` | The caller is not a service token. The description is `Insufficient permissions: service token required`. |
| 404 | `E07` | No platform contract row matches. |

### 6.4 `POST /api/v2/contracts/upgrade/token`

**Access:** Service token only. A user caller gets `403`.

Queues a job that upgrades one voucher proxy to the current ERC-20 implementation in the platform contract table.

**Request body (JSON):**

| Field | Type | Required | Rules |
|---|---|---|---|
| `address` | string | Yes | Checksummed address of a confirmed deployed voucher. The match is exact. |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Token upgrade request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "address": "0x1111111111111111111111111111111111111111"
  }
}
```

The API always queues the job. It does not check whether the proxy already uses the current implementation.

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E02`, `E04` | The body is not valid JSON, or `address` fails validation. |
| 401 | `E06` | No valid credential. |
| 403 | `E04` | The caller is not a service token. |
| 404 | `E07` | No confirmed deployed voucher has this address, or no ERC-20 platform contract exists. |

### 6.5 `POST /api/v2/contracts/upgrade/pool`

**Access:** Service token only. A user caller gets `403`.

Queues upgrade jobs for a pool proxy and its sub-contracts.
All jobs share one tracking ID.

**Request body (JSON):**

| Field | Type | Required | Rules |
|---|---|---|---|
| `address` | string | Yes | Checksummed address of a confirmed deployed pool. The comparison ignores case. |

**Contracts in the batch.**
The API builds one job for each of these contracts, if the platform contract table has an entry and the pool record has the address:

| Type | Proxy |
|---|---|
| `SWAPPOOL` | The pool itself. |
| `TOKEN_UNIQUE_SYMBOL_INDEX` | The token registry of the pool. |
| `LIMITER` | The limiter of the pool. |
| `FEE_POLICY` | The fee policy of the pool. |
| `ORACLE_QUOTER` or `RELATIVE_QUOTER` | The quoter of the pool. The recorded `quoterType` of the pool selects the type. |

The shared decimal quoter is not a proxy. The API does not upgrade it.
The API reads the implementation slot of each proxy on chain.
It removes a proxy that already runs the target implementation.
If the API cannot read the slot, it keeps the proxy in the batch.

**Response `200` when jobs are queued:**

```json
{
  "ok": true,
  "description": "Pool upgrade request accepted",
  "result": {
    "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "upgrades": [
      { "type": "SWAPPOOL", "address": "0x1111111111111111111111111111111111111111" }
    ],
    "skipped": [
      {
        "type": "LIMITER",
        "address": "0x0000000000000000000000000000000000000001",
        "reason": "already on the current implementation"
      }
    ]
  }
}
```

`skipped` is always an array. Each item has `type`, `address` and `reason`.
The `reason` is `already on the current implementation` or `pool has no recorded quoterType; upgrade it explicitly`.

**Response `200` when no job is needed.**
The `description` is `No upgrade tasks found for this pool`.
The `result` has `upgrades` as an empty array and `skipped`.
The result has no `trackingId`.

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E02`, `E04` | The body is not valid JSON, or `address` fails validation. |
| 401 | `E06` | No valid credential. |
| 403 | `E04` | The caller is not a service token. |
| 404 | `E07` | No confirmed deployed pool has this address. |

### 6.6 `GET /api/v2/otx/track/:trackingId`

**Access:** User or service token. A user caller who is not a superadmin can track only IDs that the API recorded for that user at deploy time.

Returns the OTX chain for a tracking ID.

**Path parameter:**

| Name | Type | Rules |
|---|---|---|
| `trackingId` | string | Required. Must be a UUID. |

**Response `200`:**

```json
{
  "ok": true,
  "description": "OTX chain for tracking ID f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "result": {
    "otx": [
      {
        "id": 1,
        "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "otxType": "TOKEN_PROXY_DEPLOY",
        "signerAccount": "0x0000000000000000000000000000000000000001",
        "rawTx": "0x02f8...",
        "txHash": "0x1111111111111111111111111111111111111111111111111111111111111111",
        "nonce": 5,
        "replaced": false,
        "createdAt": "2026-01-15T10:30:00Z",
        "updatedAt": "2026-01-15T10:30:05Z",
        "status": "SUCCESS"
      }
    ]
  }
}
```

The route orders the list by `id`, lowest first.
If no OTX exists for the ID, `otx` is empty. A service caller can get `null` instead of `[]`.

**OTX fields:**

| Field | Type | Meaning |
|---|---|---|
| `id` | number | OTX record ID. |
| `trackingId` | string | Tracking UUID. |
| `otxType` | string | Operation type. |
| `signerAccount` | string | Address of the account that signed the transaction. |
| `rawTx` | string | Signed transaction as hex. |
| `txHash` | string | Transaction hash. |
| `nonce` | number | Nonce of the signer account. |
| `replaced` | boolean | `true` if a later transaction replaced this one. |
| `createdAt`, `updatedAt` | string | RFC 3339 times. |
| `status` | string | Dispatch status. |

**`otxType` values:** `TOKEN_PROXY_DEPLOY`, `POOL_TOKEN_INDEX_DEPLOY`, `POOL_LIMITER_DEPLOY`, `POOL_FEE_POLICY_DEPLOY`, `POOL_ORACLE_QUOTER_DEPLOY`, `POOL_RELATIVE_QUOTER_DEPLOY`, `POOL_PROXY_DEPLOY`, `PROXY_UPGRADE`, `RELAY_DELEGATE`, `RELAY_EXEC`, `RELAY_DELEGATE_EXEC`, `ACCOUNT_REGISTER`.

**`status` values:** `PENDING`, `IN_NETWORK`, `SUCCESS`, `REVERTED`, `LOW_NONCE`, `NO_GAS`, `LOW_GAS_PRICE`, `REPLACEMENT_UNDERPRICED`, `NETWORK_ERROR`, `UNKNOWN_RPC_ERROR`, `POOL_FULL`, `POOL_CONFLICT`, `NONCE_GAP`, `INVALID_TX`.
`SUCCESS`, `REVERTED`, `LOW_NONCE` and `INVALID_TX` are final.

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E04` | `trackingId` is not a UUID. |
| 401 | `E06` | No valid credential. |
| 404 | `E07` | A user caller (not a superadmin) does not own the tracking ID. |

### 6.7 `GET /api/v2/otx/track/:trackingId/contract`

**Access:** User or service token. A user caller who is not a superadmin can read only IDs that the API recorded for that user at deploy time.

Returns the contract that a deploy created.
The route looks for a voucher first, then for a pool.

**Path parameter:**

| Name | Type | Rules |
|---|---|---|
| `trackingId` | string | A UUID. The route does not check the format. |

**Response `200`** when a confirmed contract exists:

```json
{
  "ok": true,
  "description": "Deployed token for tracking ID f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "result": {
    "type": "TOKEN",
    "contract": { "id": 42, "status": "CONFIRMED", "address": "0x1111111111111111111111111111111111111111" }
  }
}
```

`type` is `TOKEN` or `POOL`.
For `TOKEN`, `contract` has the fields in section 14, `GET /api/v2/users/me/tokens/:address`.
For `POOL`, `contract` has the fields in section 14, `GET /api/v2/users/me/pools/:address`.
The example shows only some fields.

**Response `204`** with an empty body when no confirmed voucher or pool exists yet.
Poll this route until the status is `200`.
A deploy that fails stays `204`. Check `GET /api/v2/otx/track/:trackingId` for the failure.

**Errors:**

| Status | Code | When |
|---|---|---|
| 401 | `E06` | No valid credential. |
| 404 | `E07` | A user caller (not a superadmin) does not own the tracking ID. |
| 500 | `E01` | A user caller sends a value that is not a UUID. |

### 6.8 `GET /api/v2/account/otx/:address`

**Access:** User or service token. `address` must be the caller's own address, unless the caller is a service token or a superadmin.

Returns the OTX that a custodial signer account signed, newest first.

**Path parameter:**

| Name | Type | Rules |
|---|---|---|
| `address` | string | Required. Checksummed address. |

**Query parameters:**

| Name | Type | Default | Rules |
|---|---|---|---|
| `perPage` | integer | 100 | A value of 0, or above 100, becomes 100. |
| `cursor` | integer | none | An OTX `id`. |
| `next` | boolean | `false` | Sets the direction. See below. |

**Pagination.**
The route sorts the page by `id`, highest first.
The first request has no `cursor` and no `next`.
To get older rows, send `cursor` set to `last` and leave `next` out.
To get newer rows, send `next=true` and `cursor` set to `first`.
Send `cursor` whenever you send `next`.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Successfully fetched OTX for 0x0000000000000000000000000000000000000001",
  "result": {
    "otx": [
      {
        "id": 50,
        "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "otxType": "RELAY_EXEC",
        "signerAccount": "0x0000000000000000000000000000000000000001",
        "rawTx": "0x02f8...",
        "txHash": "0x1111111111111111111111111111111111111111111111111111111111111111",
        "nonce": 4,
        "replaced": false,
        "createdAt": "2026-01-15T10:30:00Z",
        "updatedAt": "2026-01-15T10:30:05Z",
        "status": "SUCCESS"
      }
    ],
    "first": 50,
    "last": 50
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `otx` | array | The page. See section 6.6 for the OTX fields. Can be `null` when empty. |
| `first` | number | `id` of the first item (the highest). 0 when the page is empty. |
| `last` | number | `id` of the last item (the lowest). 0 when the page is empty. |

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E02` | `perPage`, `cursor` or `next` has a wrong type. |
| 400 | `E04` | `address` is not a valid checksummed address. |
| 401 | `E06` | No valid credential. |
| 403 | `E10` | The caller is a user and `address` is not the caller's own address. |

### 6.9 `GET /api/v2/account/status/:address`

**Access:** User or service token.

Returns the chain and internal state of a custodial account.

**Path parameter:**

| Name | Type | Rules |
|---|---|---|
| `address` | string | Required. Checksummed address. |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Custodial account status",
  "result": {
    "gasBalance": "1000000000000000000",
    "networkNonce": 4,
    "internalNonce": 5,
    "active": true
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `gasBalance` | string | Native balance in wei, as a decimal string. |
| `networkNonce` | number | The chain transaction count minus 1. It is 0 when the count is 0. |
| `internalNonce` | number | The next nonce that the service holds for the account. |
| `active` | boolean | The `active` flag of the key pair of the account. |

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E04` | `address` is not a valid checksummed address. |
| 401 | `E06` | No valid credential. |
| 404 | `E07` | The service has no key pair or nonce record for the address. |
| 500 | `E01` | The chain node call fails. |

### 6.10 `GET /api/v2/system`

**Access:** User or service token.

Returns the public address of the master signer and the build string.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Current system information",
  "result": {
    "systemSigner": "0x0000000000000000000000000000000000000001",
    "build": "v1.0.0"
  }
}
```

### 6.11 `GET /api/v2/federation/system`

**Access:** User or service token.

The route exists only when the service has federation configured.
Without it, the route is not registered.

Checks that federation answers a ping within 5 seconds.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Federation DB connectivity check",
  "result": {
    "status": "UP"
  }
}
```

**Response `503`** when the ping fails.
The body has `ok` set to `false`, the description `Federation DB unreachable`, and an empty `code`.

---

## 7. Vouchers and offerings

A voucher is a token issued by an account. An offering is a product or service that the voucher owner publishes against a voucher. This section covers the public market reads, the review routes, and the owner writes for vouchers and offerings.

All paths in this section start with `/api/v2`. The error envelope and the codes `E01` to `E19` are defined in section 3.

### 7.1 Shared behavior of the market reads

These routes share the behavior in this part:

- `GET /api/v2/vouchers`
- `GET /api/v2/vouchers/:address`
- `GET /api/v2/vouchers/:address/offerings`
- `GET /api/v2/vouchers/:address/offerings/:id`
- `GET /api/v2/vouchers/:address/reviews`
- `GET /api/v2/offerings`
- `GET /api/v2/marketplace`

#### Access

These routes are public. A caller does not need a session. The routes `GET /api/v2/vouchers`, `GET /api/v2/offerings` and `GET /api/v2/marketplace` need a user session when the request has `nearby=true`.

#### Rate limit

Each client IP address has a token bucket.
The bucket gives 60 requests each minute (one token each second) with a burst of 60.
A request that has no token gets status `429`, code `E11`, description `rate limit exceeded, retry later`, and a `Retry-After` header in whole seconds.

- The limit applies to every caller, also to a caller with a valid session.
- The limit runs before the session check. A rejected `nearby=true` request from a signed-out caller still uses one token.
- The public catalog, report and recipient routes in other sections use the same bucket.
- The client key is the direct peer address. When the deployment trusts its proxy, the key is the last entry of the `X-Forwarded-For` header.
- The server keeps at most 10000 client entries and removes an entry after 10 minutes without requests. When the table is full of active clients, a new client gets `429` with `Retry-After: 1`.

```json
{
  "ok": false,
  "description": "rate limit exceeded, retry later",
  "code": "E11"
}
```

#### Cache headers

A successful `200` response from these routes has one of two cache policies.

| Condition | `Cache-Control` | `Vary` |
|---|---|---|
| `nearby` is not `true` | `public, max-age=60, stale-while-revalidate=300` | `Origin` is added to any existing value |
| `nearby=true` | `private, no-store` | not changed |

The routes that use the public policy are `GET /api/v2/vouchers`, `GET /api/v2/vouchers/:address`, `GET /api/v2/vouchers/:address/offerings`, `GET /api/v2/vouchers/:address/offerings/:id` and `GET /api/v2/offerings`. A guest and a signed-in user get the same representation, so a shared cache can store it.

These routes use `no-store` for every response: `GET /api/v2/marketplace` and the review routes. A request with `nearby=true` also gets `private, no-store` on its error responses. Error responses from the other routes do not carry the public policy.

#### Vouchers tag facet cache

`GET /api/v2/vouchers` can return a tag facet (see section 7.2).
If the request has none of `bbox`, `q`, `tags`, `pools`, `owner` and `nearby`, the server caches the facet for 60 seconds.
A new tag can take up to 60 seconds to show in the facet.
Any other request returns a current facet.

#### Search and filter parameters

These query parameters are common to the list routes. A route can accept a subset. The table in each route section lists what the route accepts.

| Parameter | Type | Default | Rules |
|---|---|---|---|
| `q` | string | none | The server trims the value and collapses inner spaces. The length must be 2 to 64 characters. An empty value means no search. A value of 2 characters matches a prefix. A longer value matches a substring. The match is case-insensitive. The characters `%`, `_` and `\` match themselves. |
| `tags` | string | none | Tag values. Repeat the parameter or use commas. At most 12 distinct values. The server trims, deduplicates and sorts the values. Tag values are case-sensitive. |
| `tagMatch` | string | `any` | `any` or `all`. Other values return `400` `E04`. |
| `facets` | boolean | `false` | Value for Go `ParseBool`, for example `true` or `false`. The server computes tag facets on the first page only. |
| `pools` | string | none | Pool references in the form `chainId:address`. Repeat the parameter or use commas. At most 100 distinct references. The chain ID must be a positive integer. A voucher matches when at least one selected pool allows it. |
| `nearby` | string | none | `true` or `false`. Other values return `400` `E04`. |

Validation rules for these parameters:

- A value outside the rules returns `400` with code `E04`. The description names the parameter.
- A `tags` value outside the tag vocabulary returns `400` `E04`. To read the valid values, retry without `tags` and with `facets=true`.
- A `pools` reference that does not exist returns `400` `E04`. The server never drops it.

#### Nearby search

The request `nearby=true` limits the result to rows within 1000 kilometers of the caller. The server takes the center from the `geoLat` and `geoLng` values of the caller profile. The caller cannot set the center or the radius.

- The caller must have a user session. A request without a session returns `401` `E06`. A service token returns `403` `E10`.
- A caller without a saved location gets `400` `E04` with the description `a valid saved profile location is required`.
- On `GET /api/v2/offerings` and `GET /api/v2/marketplace`, each row has `distanceMeters` and the order is nearest first. Without `nearby`, `distanceMeters` is `null`.
- On `GET /api/v2/vouchers` and on `GET /api/v2/pools`, `nearby` only filters rows. The rows have no distance field and the order does not change.

#### Pagination styles

The routes use two styles.

| Style | Routes | Request | Response |
|---|---|---|---|
| Numeric keyset | `GET /api/v2/vouchers`, `GET /api/v2/vouchers/:address/offerings` | `perPage`, `next`, `cursor` | `first`, `last` |
| Opaque cursor | `GET /api/v2/offerings`, `GET /api/v2/marketplace` | `perPage`, `cursor` | `nextCursor` |

For the numeric keyset style:

- Rows are in descending `id` order. A request without `cursor` returns the newest rows.
- `next=true` with `cursor` returns rows with an `id` below the cursor. Use the `last` value from the previous response.
- `cursor` without `next=true` returns rows with an `id` above the cursor. Use the `first` value. The rows are still in descending order.
- `first` is the `id` of the first row. `last` is the `id` of the last row. An empty page has `first` and `last` equal to `0` and an empty array, never `null`.
- A value that is not a number in `perPage` or `cursor` returns `400` `E02`.

For the opaque cursor style:

- `perPage` is an integer from 1 to 100. The default is 20. Other values return `400` `E04`.
- `nextCursor` is a string, or `null` on the last page. Send it unchanged as `cursor`.
- A cursor is valid only for the same search, the same location and the same caller. A cursor from another search returns `400` `E04` on `GET /api/v2/offerings`. Keep the search parameters fixed while you read pages.
- A cursor longer than 1024 characters (offerings) is invalid.

#### Offering object

The offering routes return this object. The fields that only `GET /api/v2/offerings` fills are `null` on the other routes.

| Field | Type | Notes |
|---|---|---|
| `id` | integer | Offering ID. |
| `sourceId` | integer or null | ID in the import source. |
| `voucherId` | integer | Voucher ID. |
| `voucherAddress` | string | Voucher address. |
| `voucherName` | string or null | Display name of the voucher. |
| `voucherSymbol` | string or null | |
| `voucherUoa` | string or null | Unit of account of the voucher. |
| `priceInUnitOfAccount` | string or null | `price` multiplied by the voucher value, as decimal text. The value is `null` unless the price, the unit, the voucher value and the unit of account are all set and positive. |
| `voucherDecimals` | integer or null | |
| `voucherImageUrl` | string or null | Only `GET /api/v2/offerings`. |
| `voucherLocationName` | string or null | Only `GET /api/v2/offerings`. |
| `voucherGeoLat`, `voucherGeoLng` | number or null | Only `GET /api/v2/offerings`. |
| `sellerAddress` | string | The seller. |
| `commodityName` | string | Name of the offering. |
| `description` | string | |
| `quantity` | string | Decimal text. New offerings have `"1"`. |
| `unit` | string or null | |
| `frequency` | string or null | |
| `price` | string or null | Decimal text. |
| `available` | boolean | |
| `locationName` | string or null | |
| `geoLat`, `geoLng` | number or null | |
| `imageUrl` | string or null | |
| `effectiveLocationName` | string or null | Only `GET /api/v2/offerings`. The offering value, or the voucher value when the offering has none. |
| `effectiveGeoLat`, `effectiveGeoLng` | number or null | Only `GET /api/v2/offerings`. Same fallback. |
| `distanceMeters` | number or null | Only `GET /api/v2/offerings` with `nearby=true`. |
| `tags` | array of string | Sorted. Empty array when none. |
| `createdAt`, `updatedAt` | string | RFC 3339 time. |

```json
{
  "id": 42,
  "sourceId": null,
  "voucherId": 7,
  "voucherAddress": "0x0000000000000000000000000000000000000001",
  "voucherName": "Example Voucher",
  "voucherSymbol": "EXV",
  "voucherUoa": "KES",
  "priceInUnitOfAccount": "50",
  "voucherDecimals": 6,
  "voucherImageUrl": null,
  "voucherLocationName": null,
  "voucherGeoLat": null,
  "voucherGeoLng": null,
  "sellerAddress": "0x1111111111111111111111111111111111111111",
  "commodityName": "Fresh maize",
  "description": "Maize from the village farm",
  "quantity": "1",
  "unit": "kg",
  "frequency": null,
  "price": "5",
  "available": true,
  "locationName": null,
  "geoLat": null,
  "geoLng": null,
  "imageUrl": null,
  "effectiveLocationName": null,
  "effectiveGeoLat": null,
  "effectiveGeoLng": null,
  "distanceMeters": null,
  "tags": ["Food"],
  "createdAt": "2025-01-15T10:30:00Z",
  "updatedAt": "2025-01-15T10:30:00Z"
}
```

### 7.2 `GET /api/v2/vouchers`

**Access:** Public. A user session is needed when `nearby=true`. See section 7.1 for the rate limit and the cache headers.

Returns active vouchers that have complete coordinates. The route supports a map viewport, search, tag filters and an owner filter.

#### Query parameters

| Parameter | Type | Default | Rules |
|---|---|---|---|
| `perPage` | integer | `500` | Maximum is `500`. `0` or a larger value becomes `500`. |
| `next` | boolean | `false` | See pagination in section 7.1. |
| `cursor` | integer | none | A voucher `id`. |
| `bbox` | string | none | `minLng,minLat,maxLng,maxLat`. Four finite numbers. Longitude from -180 to 180, latitude from -90 to 90, and each minimum not above its maximum. A box cannot cross the antimeridian. Split the viewport into two requests in that case. An invalid value returns `400` `E04`. |
| `owner` | string | none | A hex address. The server compares it with the voucher owner and ignores case. An invalid address returns `400` `E04`. |
| `q`, `tags`, `tagMatch`, `facets`, `pools`, `nearby` | | | See section 7.1. `q` matches the voucher name and the symbol. The name is the display name when one is set. |

Keep `bbox` and the filters fixed while you read pages.

#### Response

Status `200`, description `Market vouchers`. The `result` has these fields:

| Field | Type | Notes |
|---|---|---|
| `vouchers` | array | Voucher summaries. Empty array when none. |
| `first` | integer | See section 7.1. |
| `last` | integer | See section 7.1. |
| `tagFacets` | array | Only with `facets=true` on the first page. Rows of `{"tag": string, "count": integer}`. |

The facet lists every value of the tag vocabulary, also with count `0`.
The counts ignore the selected `tags`, so a selected tag does not set the counts of other tags to zero.
A voucher counts for a tag when the tag is on the voucher or on one of its available offerings.

Voucher summary fields:

| Field | Type | Notes |
|---|---|---|
| `id` | integer | |
| `chainId` | integer | |
| `address` | string | |
| `name` | string or null | The display name, or the contract name when no display name is set. |
| `symbol` | string or null | |
| `iconUrl` | string or null | |
| `locationName` | string or null | |
| `geoLat`, `geoLng` | number | Never `null` on this route. |
| `kind` | string | Always `voucher`. |
| `markerIcon`, `markerColor` | string or null | The marker the owner chose. `null` means automatic. |

```json
{
  "ok": true,
  "description": "Market vouchers",
  "result": {
    "vouchers": [
      {
        "id": 7,
        "chainId": 42220,
        "address": "0x0000000000000000000000000000000000000001",
        "name": "Example Voucher",
        "symbol": "EXV",
        "iconUrl": null,
        "locationName": "Nairobi",
        "geoLat": -1.286389,
        "geoLng": 36.817223,
        "kind": "voucher",
        "markerIcon": null,
        "markerColor": null
      }
    ],
    "first": 7,
    "last": 7
  }
}
```

#### Errors

| Status | Code | Condition |
|---|---|---|
| 400 | `E02` | `perPage`, `next` or `cursor` is not valid. |
| 400 | `E04` | `bbox`, `owner`, a search parameter or a `pools` reference is not valid. |
| 400 | `E04` | `nearby=true` and the caller has no saved location. |
| 401 | `E06` | `nearby=true` and the request has no valid session. |
| 403 | `E10` | `nearby=true` and the caller uses a service token. |
| 429 | `E11` | The rate limit is exceeded. |

### 7.3 `GET /api/v2/vouchers/:address`

**Access:** Public. See section 7.1 for the rate limit and the cache headers.

Returns the detail of one active voucher.

| Path parameter | Type | Rules |
|---|---|---|
| `address` | string | A hex address. The lookup ignores case. |

Status `200`, description `Market voucher`. The `result` is `{"voucher": {...}}`. The voucher fields are:

| Field | Type | Notes |
|---|---|---|
| `id`, `chainId`, `address` | | As in the list. |
| `name` | string or null | The display name, or the contract name. |
| `contractName` | string or null | The name stored on the contract. |
| `symbol` | string or null | |
| `decimals` | integer or null | |
| `ownerAddress` | string or null | |
| `description` | string or null | |
| `voucherUoa` | string or null | Unit of account. |
| `voucherValue` | string or null | The value of one voucher in `voucherUoa`, as decimal text. |
| `locationName` | string or null | |
| `geoLat`, `geoLng` | number or null | Can be `null` on this route. |
| `email`, `website`, `phone`, `terms` | string or null | |
| `iconUrl`, `bannerUrl` | string or null | |
| `tags` | array of string | Never `null`. |
| `kind` | string | Always `voucher`. |
| `markerIcon`, `markerColor` | string or null | |

```json
{
  "ok": true,
  "description": "Market voucher",
  "result": {
    "voucher": {
      "id": 7,
      "chainId": 42220,
      "address": "0x0000000000000000000000000000000000000001",
      "name": "Example Voucher",
      "contractName": "Example Voucher",
      "symbol": "EXV",
      "decimals": 6,
      "ownerAddress": "0x1111111111111111111111111111111111111111",
      "description": "A community voucher",
      "voucherUoa": "KES",
      "voucherValue": "10",
      "locationName": "Nairobi",
      "geoLat": -1.286389,
      "geoLng": 36.817223,
      "email": "issuer@example.com",
      "website": "https://example.com",
      "phone": null,
      "terms": null,
      "iconUrl": null,
      "bannerUrl": null,
      "tags": ["Food"],
      "kind": "voucher",
      "markerIcon": null,
      "markerColor": null
    }
  }
}
```

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `address` is not a valid hex address. |
| 404 | `E07` | The voucher does not exist or is retired. The two cases give the same response. |
| 429 | `E11` | The rate limit is exceeded. |

### 7.4 `GET /api/v2/vouchers/:address/offerings`

**Access:** Public. See section 7.1 for the rate limit and the cache headers.

Returns the available offerings of one active voucher. The order is descending `id`. The route uses the numeric keyset pagination of section 7.1.

| Parameter | Type | Default | Rules |
|---|---|---|---|
| `address` (path) | string | required | A hex address. The lookup ignores case. |
| `perPage` | integer | `100` | Maximum is `100`. `0` or a larger value becomes `100`. |
| `next`, `cursor` | | | See section 7.1. |

Status `200`, description `Offerings for voucher <address>`. The `result` has `offerings` (array of offering objects, see section 7.1), `first` and `last`. An unknown or retired voucher returns an empty list, not an error.

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `address` is not a valid hex address. |
| 400 | `E02` | `perPage`, `next` or `cursor` is not valid. |
| 429 | `E11` | The rate limit is exceeded. |

### 7.5 `GET /api/v2/vouchers/:address/offerings/:id`

**Access:** Public. See section 7.1 for the rate limit and the cache headers.

Returns one available offering of an active voucher.

| Path parameter | Type | Rules |
|---|---|---|
| `address` | string | A hex address. |
| `id` | integer | A positive integer. |

Status `200`, description `Offering`. The `result` is `{"offering": {...}}` (see section 7.1).

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `address` is not a hex address, or `id` is not a positive integer. |
| 404 | `E03` | The offering does not exist, is unlisted, belongs to another voucher, or its voucher is retired. |
| 429 | `E11` | The rate limit is exceeded. |

### 7.6 `GET /api/v2/offerings`

**Access:** Public. A user session is needed when `nearby=true`. See section 7.1 for the rate limit and the cache headers.

Returns available offerings of active vouchers. An offering must have a location. The location is the offering location, or the voucher location when the offering has none. The order is newest first, or nearest first with `nearby=true`.

| Parameter | Type | Default | Rules |
|---|---|---|---|
| `perPage` | integer | `20` | From 1 to 100. |
| `cursor` | string | none | The `nextCursor` of the previous response. |
| `q` | string | none | Matches the offering name, the offering description, the voucher name, the voucher symbol and the offering tags. |
| `tags`, `tagMatch`, `facets`, `pools`, `nearby` | | | See section 7.1. The tags are the offering tags. |

Status `200`, description `Offerings`. The `result` has these fields:

| Field | Type | Notes |
|---|---|---|
| `offerings` | array | Offering objects (see section 7.1). |
| `nextCursor` | string or null | |
| `tagFacets` | array | Only with `facets=true` and no `cursor`. Rows of `{"tag": string, "count": integer}`. |

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `perPage`, `cursor`, a search parameter or a `pools` reference is not valid. |
| 400 | `E04` | `nearby=true` and the caller has no saved location. |
| 401 | `E06` | `nearby=true` and the request has no valid session. |
| 403 | `E10` | `nearby=true` and the caller uses a service token. |
| 429 | `E11` | The rate limit is exceeded. |

### 7.7 `GET /api/v2/marketplace`

**Access:** Public. A user session is needed when `nearby=true`. See section 7.1 for the rate limit.

Returns one mixed list of offerings, vouchers, pools and curated catalogs. The server does the ranking and the paging in one query. Every response has `Cache-Control: no-store`.

| Parameter | Type | Default | Rules |
|---|---|---|---|
| `perPage` | integer | `20` | From 1 to 100. |
| `cursor` | string | none | The `nextCursor` of the previous response. |
| `kinds` | string | all kinds | Values `catalog`, `offering`, `pool` and `voucher`. Repeat the parameter or use commas. The server ignores case and empty items. Another value returns `400` `E04`. |
| `q` | string | none | Matches the name, symbol, description, tags and related names of a row. |
| `tags`, `tagMatch`, `facets`, `pools`, `nearby` | | | See section 7.1. The tags are the tags of each row. |

The server includes only rows that have coordinates. Offerings and vouchers must be available and active. The order is:

1. Match rank, from the best match to the worst. The rank is used only with `q`. An exact name or symbol match is best, then a name prefix, then a tag match, then a name substring, then any other match.
2. Distance, nearest first, with `nearby=true`.
3. `updatedAt`, newest first.
4. `kind`, ascending.
5. `id`, descending.

Status `200`. The response has no description. The `result` has these fields:

| Field | Type | Notes |
|---|---|---|
| `results` | array | Rows. Empty array when none. |
| `nextCursor` | string or null | |
| `tagFacets` | array | Only with `facets=true` and no `cursor`. Rows of `{"tag": string, "count": integer}`. The counts cover the rows that match the current filter, including the selected tags. |

Row fields:

| Field | Type | Notes |
|---|---|---|
| `kind` | string | `offering`, `voucher`, `pool` or `catalog`. |
| `id` | integer | ID in the table of the kind. |
| `chainId` | integer or null | `null` for a catalog. |
| `address` | string or null | The voucher address for an offering. `null` for a catalog. |
| `name` | string | |
| `description` | string or null | |
| `symbol` | string or null | |
| `imageUrl` | string or null | |
| `markerIcon`, `markerColor`, `voucherKind` | string or null | For a voucher row only. |
| `locationName` | string or null | |
| `geoLat`, `geoLng` | number | |
| `distanceMeters` | number or null | Set with `nearby=true`. |
| `tags` | array of string | |
| `voucherAddress`, `voucherName`, `voucherSymbol`, `voucherUoa` | string or null | For an offering row only. |
| `priceInUnitOfAccount`, `unit` | string or null | For an offering row only. |
| `poolCount` | integer or null | For a catalog row only. |
| `voucherCount` | integer or null | For a pool row and a catalog row. |
| `createdAt`, `updatedAt` | string | RFC 3339 time. |

```json
{
  "ok": true,
  "description": "",
  "result": {
    "results": [
      {
        "kind": "voucher",
        "id": 7,
        "chainId": 42220,
        "address": "0x0000000000000000000000000000000000000001",
        "name": "Example Voucher",
        "description": null,
        "symbol": "EXV",
        "imageUrl": null,
        "markerIcon": null,
        "markerColor": null,
        "voucherKind": "voucher",
        "locationName": "Nairobi",
        "geoLat": -1.286389,
        "geoLng": 36.817223,
        "distanceMeters": null,
        "tags": ["Food"],
        "voucherAddress": null,
        "voucherName": null,
        "voucherSymbol": null,
        "voucherUoa": null,
        "priceInUnitOfAccount": null,
        "unit": null,
        "poolCount": null,
        "voucherCount": null,
        "createdAt": "2025-01-15T10:30:00Z",
        "updatedAt": "2025-01-15T10:30:00Z"
      }
    ],
    "nextCursor": null
  }
}
```

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `perPage`, `kinds`, a search parameter or a `pools` reference is not valid. |
| 400 | `E04` | `nearby=true` and the caller has no saved location. |
| 401 | `E06` | `nearby=true` and the request has no valid session. |
| 403 | `E10` | `nearby=true` and the caller uses a service token. |
| 429 | `E11` | The rate limit is exceeded. |
| 500 | `E01` | The `cursor` is not valid for this search. The cursor check does not return a validation error. |

### 7.8 Voucher reviews

A review has 1 to 5 stars and an optional comment.
A reviewer can review a voucher after the reviewer redeems it.
Redeeming means a successful transfer of voucher tokens from the reviewer to the voucher owner.
The reviewer can write or change the review for 7 days after the latest redemption.
A voucher owner cannot review the own voucher.
One reviewer has one review for each voucher.

Review responses differ from the other routes:

- A success response has `ok` and `result`. It has no `description`.
- The review routes return `Cache-Control: no-store`.
- Most error responses have `"code": ""`. The routes return a code only where the table in the route lists it.
- The review routes accept retired vouchers.

Review object:

| Field | Type | Notes |
|---|---|---|
| `id` | integer | |
| `stars` | integer | 1 to 5. |
| `comment` | string | |
| `reply` | string or null | The reply of the voucher owner. |

#### 7.8.1 `GET /api/v2/vouchers/:address/reviews`

**Access:** Public. See section 7.1 for the rate limit.

Returns the visible reviews of a voucher, newest first. Each page has at most 20 reviews.

| Parameter | Type | Default | Rules |
|---|---|---|---|
| `address` (path) | string | required | A hex address. |
| `stars` | integer | none | From 1 to 5. The filter applies to the list only. `counts`, `total` and `average` cover all visible reviews. |
| `before` | integer | none | A review `id`. Returns reviews with a lower `id`. Use the `next` value of the previous response. |

Status `200`. The `result` has these fields:

| Field | Type | Notes |
|---|---|---|
| `reviews` | array | Review objects. |
| `counts` | object | The number of reviews for each star value. The keys are `"1"` to `"5"`. |
| `total` | integer | |
| `average` | number | `0` when there are no reviews. |
| `next` | integer or null | The `before` value for the next page. |

```json
{
  "ok": true,
  "result": {
    "reviews": [{"id": 12, "stars": 5, "comment": "Good service", "reply": null}],
    "counts": {"1": 0, "2": 0, "3": 0, "4": 0, "5": 1},
    "total": 1,
    "average": 5,
    "next": null
  }
}
```

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `address` is not a valid hex address. |
| 400 | none | `stars` is not an integer from 1 to 5, or `before` is not a valid number. |
| 404 | none | The voucher does not exist. |
| 429 | `E11` | The rate limit is exceeded. |

#### 7.8.2 `GET /api/v2/vouchers/:address/reviews/me`

**Access:** User session.

Returns what the caller can do with the reviews of this voucher.

Status `200`. The `result` has these fields:

| Field | Type | Notes |
|---|---|---|
| `isOwner` | boolean | `true` when the caller owns the voucher. |
| `editableUntil` | string or null | End of the time the caller can write a review. `null` when the caller cannot write one. |
| `review` | object or null | The review of the caller. |
| `hidden` | boolean | `true` when a moderator hid the review of the caller. |
| `editableReplyIds` | array of integer | Review IDs that the caller (as owner) already replied to. Empty for a reviewer. |
| `flaggedReviewIds` | array of integer | Review IDs that the caller (as owner) flagged. Empty for a reviewer. |

For a caller who is not the owner, the server reads the latest redemption from the transfer index. It sets `editableUntil` to 7 days after that redemption. When the redemption is older than 7 days, `editableUntil` is in the past.

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `address` is not a valid hex address. |
| 401 | `E06` | No valid session. |
| 403 | `E10` | The caller uses a service token. |
| 404 | none | The voucher does not exist. |
| 503 | none | The voucher is not on the configured chain, or the transfer index is not available. |

#### 7.8.3 `PUT /api/v2/vouchers/:address/reviews/me`

**Access:** User session. The caller must be a reviewer with a redemption in the last 7 days.

Creates or replaces the review of the caller.

| Body field | Type | Required | Rules |
|---|---|---|---|
| `stars` | integer | yes | From 1 to 5. |
| `comment` | string | no | The server trims the value. At most 2000 characters. |

Status `200`. The `result` is `{"saved": true}`.

| Status | Code | Condition |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | `address` is not a valid hex address. |
| 400 | none | `stars` or `comment` is outside the rules. |
| 401 | `E06` | No valid session. |
| 403 | `E10` | The caller uses a service token, owns the voucher, has a hidden review, has no redemption in the last 7 days, or the edit window closed. |
| 404 | none | The voucher does not exist. |
| 503 | none | The voucher is not on the configured chain, or the transfer index is not available. |

#### 7.8.4 `PUT /api/v2/vouchers/:address/reviews/:id/reply`

**Access:** User session. The caller must be the current voucher owner.

Sets the reply of the owner on a review. A new reply replaces the old reply of the same owner.

| Parameter | Type | Rules |
|---|---|---|
| `address` (path) | string | A hex address. |
| `id` (path) | integer | A positive review ID. |
| `reply` (body) | string | Required. The server trims it. From 1 to 2000 characters. |

Status `200`. The `result` is `{"saved": true}`.

| Status | Code | Condition |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | `address` is not a valid hex address. |
| 400 | none | `id` is not valid, or `reply` is empty or too long. |
| 401 | `E06` | No valid session. |
| 403 | `E10` | The caller is not the current voucher owner, or uses a service token. |
| 404 | none | The voucher or the review does not exist, the review is hidden, or another account wrote the reply. |

#### 7.8.5 `POST /api/v2/vouchers/:address/reviews/:id/flag`

**Access:** User session. The caller must be the current voucher owner.

Reports a review to the moderators. A second flag from the same owner on the same review has no effect and still returns success.

| Parameter | Type | Rules |
|---|---|---|
| `address` (path) | string | A hex address. |
| `id` (path) | integer | A positive review ID. |
| `reason` (body) | string | Required. The server trims it. From 1 to 500 characters. |

Status `200`. The `result` is `{"saved": true}`. The errors are the same as in section 7.8.4, with `reason` in place of `reply`.

### 7.9 `POST /api/v2/vouchers/:address/offerings`

**Access:** User session. The caller must be the voucher owner or a superadmin.

Creates an offering for a voucher. The server sets the seller. The seller is the voucher owner. When the voucher has no owner, only a superadmin can create the offering, and the seller is that superadmin.

| Body field | Type | Required | Rules |
|---|---|---|---|
| `commodityName` | string | yes | The server trims it. At most 120 characters. |
| `description` | string | yes | The server trims it. At most 2000 characters. |
| `unit` | string | yes | The server trims it. At most 32 characters. A value that is not in the unit list becomes a new custom unit. |
| `price` | string | yes | Plain decimal text, for example `5` or `12.50`. No sign, no exponent and no separator. The value must be above zero. At most 64 characters. |
| `imageUrl` | string or null | no | An `http` or `https` URL. An empty string or `null` means no image. |

Status `201`, description `Offering created`. The `result` is `{"offering": {...}}` (see section 7.1). The response has `Cache-Control: no-store`.

```json
{
  "commodityName": "Fresh maize",
  "description": "Maize from the village farm",
  "unit": "kg",
  "price": "5",
  "imageUrl": "https://example.com/maize.jpg"
}
```

| Status | Code | Condition |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | `address` is not valid, or a body field is outside the rules. |
| 401 | `E06` | No valid session. |
| 403 | `E10` | The caller uses a service token, or is not the owner or a superadmin. |
| 404 | `E03` | The voucher does not exist. |
| 409 | `E16` | The voucher is retired. |
| 429 | `E11` | The caller reached the offer limit of the account. The `result` has `resource`, `used` and `limit`. A superadmin has no limit. |

### 7.10 `PUT /api/v2/vouchers/:address/offerings/:id`

**Access:** User session. The caller must be the voucher owner or a superadmin.

Replaces the product fields of an offering. The body is the same as for `POST /api/v2/vouchers/:address/offerings`. Every field is written. A request without `imageUrl` removes the image. There is no partial update.

Status `200`, description `Offering updated`. The `result` is `{"offering": {...}}`. The response has `Cache-Control: no-store`.

| Status | Code | Condition |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | `address` or `id` is not valid, or a body field is outside the rules. |
| 401 | `E06` | No valid session. |
| 403 | `E10` | The caller uses a service token, or is not the owner or a superadmin. |
| 404 | `E03` | The offering does not exist, belongs to another voucher, or is unlisted. |
| 409 | `E16` | The voucher is retired. |

### 7.11 `DELETE /api/v2/vouchers/:address/offerings/:id`

**Access:** User session. The caller must be the voucher owner or a superadmin.

Unlists an offering. The server keeps the row and sets `available` to `false`. The public routes no longer return it. The server does not delete the stored image.

Status `200`, description `Offering deleted`. The `result` is `{"id": <offering id>}`. The response has `Cache-Control: no-store`.

The errors are the same as for `PUT /api/v2/vouchers/:address/offerings/:id`, except that the body errors do not apply. A repeated request returns `404` `E03`.

### 7.12 `PUT /api/v2/vouchers/:address/display-name`

**Access:** User session. The caller must be the voucher owner or a superadmin.

Sets the public name of a voucher. The contract name does not change.

| Body field | Type | Required | Rules |
|---|---|---|---|
| `displayName` | string | yes | The server trims it. From 1 to 120 characters. |

Status `200`, description `Voucher display name updated`. The `result` is `{"voucher": {...}}` with the detail fields of section 7.3. The response has `Cache-Control: no-store`.

| Status | Code | Condition |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | `address` is not valid, `displayName` is missing, blank or longer than 120 characters. |
| 401 | `E06` | No valid session. |
| 403 | `E10` | The caller uses a service token, or is not the owner or a superadmin. |
| 404 | `E03` | The voucher does not exist. |
| 409 | `E16` | The voucher is retired, or another voucher or pending token already uses the name. |

### 7.13 `PUT /api/v2/vouchers/:address/profile`

**Access:** User session. The caller must be the voucher owner or a superadmin.

Replaces the off-chain profile of a voucher.
The route writes every field, so a request must send the full record.
An omitted field is cleared.
The fields `displayName`, `iconUrl`, `bannerUrl`, `markerIcon` and `markerColor` are the exception: an omitted field keeps its stored value, and an empty string clears it.
The tags are always replaced.

| Body field | Type | Rules |
|---|---|---|
| `displayName` | string | At most 120 characters. The server trims it. |
| `description` | string | At most 500 characters. |
| `voucherUoa` | string | At most 32 characters. Must not be blank. A new value becomes a custom unit of account. Omit the field to clear it. |
| `voucherValue` | number | Zero or more. The value of one voucher in `voucherUoa`. |
| `email` | string | A valid email address. |
| `website` | string | A valid URL. |
| `phone` | string | No rule. |
| `terms` | string | At most 5000 characters. |
| `locationName` | string | At most 200 characters. |
| `geoLat` | number | From -90 to 90. Send with `geoLng`. |
| `geoLng` | number | From -180 to 180. Send with `geoLat`. |
| `iconUrl`, `bannerUrl` | string | An `http` or `https` URL, or an empty string. |
| `tags` | array of string | Each tag at most 100 characters, with no comma, not blank, and no duplicate. A tag that is not in the vocabulary becomes a new custom tag. |
| `markerIcon` | string | `basket`, `hand-coin`, `coupon`, `food`, `farm`, `tools`, `health`, `education`, or an empty string. |
| `markerColor` | string | `blue`, `purple`, `orange`, `pink`, `cyan`, `brown`, or an empty string. |

Status `200`, description `Voucher profile updated`. The `result` is `{"voucher": {...}}` with the detail fields of section 7.3. The response has `Cache-Control: no-store`.

| Status | Code | Condition |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | `address` is not valid, a field is outside the rules, or only one of `geoLat` and `geoLng` is present. |
| 401 | `E06` | No valid session. |
| 403 | `E10` | The caller uses a service token, or is not the owner or a superadmin. |
| 404 | `E03` | The voucher does not exist. |
| 409 | `E16` | The voucher is retired, or another voucher or pending token already uses the display name. |

### 7.14 `DELETE /api/v2/vouchers/:address`

**Access:** User session. The caller must be the voucher owner or a superadmin.

Retires a voucher.
The server sets the voucher to inactive.
The voucher then disappears from the public reads.
The server keeps the voucher row, its offerings and its reports.
The route takes no body.
A superadmin restores a retired voucher with a route in section 15, Admin.

Status `200`, description `Voucher retired`. The `result` is `null`. The response has `Cache-Control: no-store`.

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `address` is not a valid hex address. |
| 401 | `E06` | No valid session. |
| 403 | `E10` | The caller uses a service token, or is not the owner or a superadmin. |
| 404 | `E03` | The voucher does not exist. |
| 409 | `E16` | The voucher is already retired. The server checks this before the owner check. |

## 8. Pools

A pool is a swap pool contract. The pool catalog reads are public. The pool profile write needs a user session.

All paths in this section start with `/api/v2`.

### 8.1 Access and rate limit of the pool reads

The routes `GET /api/v2/pools`, `GET /api/v2/pools/:address`, `GET /api/v2/pools/:address/vouchers` and `GET /api/v2/pools/:address/offerings` use the pool catalog rate limit.

- A caller with a valid user session or a valid service token has no rate limit.
- Any other caller is a signed-out caller. This includes a caller whose session expired. The limit for a signed-out caller is 60 requests each minute for each client IP address, with a burst of 60.
- A signed-out caller that exceeds the limit gets `429`, code `E11`, and a `Retry-After` header. The response is the same as in section 7.1.
- The pool limit is separate from the market limit of section 7.1. A pool request does not use a market token.
- `GET /api/v2/pools` with `nearby=true` needs a user session. The pool limit runs first. A signed-out caller gets `401` `E06`, and a service token gets `403` `E10`.

The pool routes do not set a public cache policy. The exception is `nearby=true` on `GET /api/v2/pools`, which returns `Cache-Control: private, no-store`.

### 8.2 `GET /api/v2/pools`

**Access:** Public, with the rate limit of section 8.1. A user session is needed when `nearby=true`.

Returns the pool catalog, newest first.

| Parameter | Type | Default | Rules |
|---|---|---|---|
| `perPage` | integer | `100` | Maximum is `100`. `0` or a larger value becomes `100`. |
| `next`, `cursor` | | | Numeric keyset pagination, as in section 7.1. The `id` is the pool `id`. |
| `q` | string | none | See section 7.1. Matches the pool name or symbol, or the name or symbol of an active voucher that the pool allows. The name is the display name when one is set. |
| `tags` | string | none | See section 7.1. The tags are the tags of the pool itself. |
| `tagMatch` | string | `any` | `any` or `all`. |
| `vouchers` | string | none | Voucher references in the form `chainId:address`. Repeat the parameter or use commas. At most 12 distinct references. The pool must allow the voucher. |
| `voucherMatch` | string | `any` | `any` or `all`. Other values return `400` `E04`. |
| `owner` | string | none | A hex address. The server compares it with the pool owner and ignores case. |
| `facets` | boolean | `false` | With `true` on the first page, the response has `tagFacets` and `voucherFacets`. |
| `nearby` | string | none | `true` or `false`. Limits the result to pools within 1000 kilometers (see section 7.1). The pool rows have no distance field. |

Status `200`, description `Pools`. The `result` has these fields:

| Field | Type | Notes |
|---|---|---|
| `pools` | array | Pool objects. Empty array when none. |
| `first` | integer | `id` of the first pool. `0` when the page is empty. |
| `last` | integer | `id` of the last pool. `0` when the page is empty. |
| `tagFacets` | array | Only with `facets=true` on the first page. Rows of `{"tag": string, "count": integer}`. |
| `voucherFacets` | array | Only with `facets=true` on the first page. Rows of `{"id", "chainId", "address", "name", "symbol", "count"}`. |

Each facet ignores its own selected filter, so the counts show what a caller can add. The facet keys are not in the cursor pages.

Pool object fields:

| Field | Type | Notes |
|---|---|---|
| `id` | integer | |
| `chainId` | integer | |
| `address` | string | |
| `name` | string or null | The display name, or the contract name. |
| `contractName` | string or null | The name stored on the contract. |
| `symbol` | string or null | |
| `ownerAddress` | string or null | |
| `feePpm` | number or null | |
| `feeAddress` | string or null | |
| `tokenRegistryAddress` | string or null | |
| `limiterAddress` | string or null | |
| `quoterAddress` | string or null | |
| `quoterType` | string or null | `decimal`, `oracle` or `relative`. `null` for an imported pool. |
| `feePolicyAddress` | string or null | |
| `protocolFeeControllerAddress` | string or null | |
| `feesDecoupled` | boolean or null | |
| `decimals` | integer or null | |
| `description` | string or null | |
| `unitOfAccount` | string or null | |
| `locationName` | string or null | |
| `geoLat`, `geoLng` | number or null | |
| `email`, `website`, `phone`, `terms` | string or null | |
| `imageUrl`, `bannerUrl` | string or null | |
| `defaultVoucherId` | integer or null | |
| `platformContractId` | integer or null | |
| `voucherCount` | integer | The number of allowed, active vouchers. |
| `tags` | array of string | Sorted. Empty array when none. |
| `createdAt`, `updatedAt` | string | RFC 3339 time. |

```json
{
  "ok": true,
  "description": "Pools",
  "result": {
    "pools": [
      {
        "id": 51,
        "chainId": 42220,
        "address": "0x0000000000000000000000000000000000000002",
        "name": "Example Pool",
        "contractName": "Example Pool",
        "symbol": "EXP",
        "ownerAddress": "0x1111111111111111111111111111111111111111",
        "feePpm": 1000,
        "feeAddress": null,
        "tokenRegistryAddress": null,
        "limiterAddress": null,
        "quoterAddress": null,
        "quoterType": "decimal",
        "feePolicyAddress": null,
        "protocolFeeControllerAddress": null,
        "feesDecoupled": false,
        "decimals": 6,
        "description": null,
        "unitOfAccount": "KES",
        "locationName": null,
        "geoLat": null,
        "geoLng": null,
        "email": null,
        "website": null,
        "phone": null,
        "terms": null,
        "imageUrl": null,
        "bannerUrl": null,
        "defaultVoucherId": null,
        "platformContractId": null,
        "voucherCount": 3,
        "tags": ["Market"],
        "createdAt": "2025-01-15T10:30:00Z",
        "updatedAt": "2025-01-15T10:30:00Z"
      }
    ],
    "first": 51,
    "last": 51
  }
}
```

| Status | Code | Condition |
|---|---|---|
| 400 | `E02` | `perPage`, `next` or `cursor` is not valid. |
| 400 | `E04` | A search parameter, `voucherMatch`, `owner` or a `vouchers` reference is not valid, or a voucher reference does not exist. |
| 400 | `E04` | `nearby=true` and the caller has no saved location. |
| 401 | `E06` | `nearby=true` and the request has no valid session. |
| 403 | `E10` | `nearby=true` and the caller uses a service token. |
| 429 | `E11` | A signed-out caller exceeds the pool rate limit. |

### 8.3 `GET /api/v2/pools/:address`

**Access:** Public, with the rate limit of section 8.1.

Returns one pool. The lookup ignores case. The route does not check the address format, so a value that is not an address returns `404`.

Status `200`, description `Pool at address <address>`. The `result` is `{"pool": {...}}` with the pool object of section 8.2.

| Status | Code | Condition |
|---|---|---|
| 404 | `E07` | No pool has this address. |
| 429 | `E11` | A signed-out caller exceeds the pool rate limit. |

### 8.4 `GET /api/v2/pools/:address/vouchers`

**Access:** Public, with the rate limit of section 8.1.

Returns the active vouchers that the pool allows, newest voucher `id` first. The route uses the numeric keyset pagination of section 7.1.

| Parameter | Type | Default | Rules |
|---|---|---|---|
| `address` (path) | string | required | A hex address. |
| `perPage` | integer | `100` | Maximum is `100`. `0` or a larger value becomes `100`. |
| `next`, `cursor` | | | A voucher `id`. |

Status `200`, description `Vouchers in pool <address>`. The `result` has `vouchers`, `first` and `last`. An unknown pool returns an empty list.

Pool voucher fields:

| Field | Type | Notes |
|---|---|---|
| `id`, `chainId`, `address` | | |
| `name` | string or null | The display name, or the contract name. |
| `symbol` | string or null | |
| `decimals` | integer or null | |
| `ownerAddress` | string or null | |
| `voucherType`, `sinkAddress`, `redemptionAddress` | string or null | |
| `description` | string or null | |
| `voucherUoa`, `voucherValue` | string or null | |
| `locationName` | string or null | |
| `geoLat`, `geoLng` | number or null | |
| `email`, `website`, `phone`, `terms` | string or null | |
| `iconUrl`, `bannerUrl` | string or null | |
| `contractVersion` | string or null | |
| `active` | boolean | Always `true` on this route. |
| `internal` | boolean | |
| `isAllowed` | boolean | Always `true` on this route. |
| `tokenLimit` | string or null | The pool limit for this voucher, as decimal text. |
| `syncedAt` | string | RFC 3339 time. |
| `oracleFeedId` | integer or null | |
| `oracleName`, `oracleAddress` | string or null | |
| `offerCount` | integer | All offerings of the voucher. |
| `availableOfferCount` | integer | Available offerings of the voucher. |
| `tags` | array of string | The tags on the voucher. |
| `createdAt`, `updatedAt` | string | RFC 3339 time. |

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `address` is not a valid hex address. |
| 400 | `E02` | `perPage`, `next` or `cursor` is not valid. |
| 429 | `E11` | A signed-out caller exceeds the pool rate limit. |

### 8.5 `GET /api/v2/pools/:address/offerings`

**Access:** Public, with the rate limit of section 8.1.

Returns the available offerings of the active vouchers that the pool allows. The order is descending offering `id`. The route uses the numeric keyset pagination of section 7.1. The route sets no cache headers.

| Parameter | Type | Default | Rules |
|---|---|---|---|
| `address` (path) | string | required | A hex address. |
| `perPage` | integer | `100` | Maximum is `100`. `0` or a larger value becomes `100`. |
| `next`, `cursor` | | | An offering `id`. |

Status `200`, description `Offerings in pool <address>`. The `result` has `offerings` (offering objects, see section 7.1), `first` and `last`. An unknown pool returns an empty list. Each offering has `voucherAddress`, `voucherName` and `voucherSymbol`, because a pool lists offerings of many vouchers.

| Status | Code | Condition |
|---|---|---|
| 400 | `E04` | `address` is not a valid hex address. |
| 400 | `E02` | `perPage`, `next` or `cursor` is not valid. |
| 429 | `E11` | A signed-out caller exceeds the pool rate limit. |

### 8.6 `PUT /api/v2/pools/:address/profile`

**Access:** User session. The caller must be the pool owner or a superadmin. A pool with no owner can only be changed by a superadmin. This route has no rate limit.

Replaces the off-chain profile of a pool.
The route writes every field, so a request must send the full record.
An omitted field is cleared.
The fields `displayName`, `imageUrl` and `bannerUrl` are the exception: an omitted field keeps its stored value, and an empty string clears it.
The tags are always replaced.

| Body field | Type | Rules |
|---|---|---|
| `displayName` | string | At most 80 characters. The server trims it. |
| `description` | string | At most 500 characters. |
| `unitOfAccount` | string | At most 32 characters. Must not be blank. A new value becomes a custom unit of account. Omit the field to clear it. |
| `email` | string | A valid email address. |
| `website` | string | A valid URL. |
| `phone` | string | No rule. |
| `terms` | string | At most 5000 characters. |
| `locationName` | string | At most 200 characters. |
| `geoLat` | number | From -90 to 90. Send with `geoLng`. |
| `geoLng` | number | From -180 to 180. Send with `geoLat`. |
| `imageUrl`, `bannerUrl` | string | An `http` or `https` URL, or an empty string. |
| `tags` | array of string | Each tag at most 100 characters, with no comma, not blank, and no duplicate. A tag that is not in the vocabulary becomes a new custom tag. |

For a pool with `quoterType` equal to `oracle`, the unit of account is fixed. The server keeps the stored value when `unitOfAccount` is omitted. A different value returns `409`.

Status `200`, description `Pool profile updated`. The `result` is `{"pool": {...}}` with the pool object of section 8.2. The response has `Cache-Control: no-store`.

| Status | Code | Condition |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | `address` is not a valid address, a field is outside the rules, or only one of `geoLat` and `geoLng` is present. |
| 401 | `E06` | No valid session. |
| 403 | `E10` | The caller uses a service token, or is not the pool owner or a superadmin. |
| 404 | `E03` | The pool does not exist. |
| 409 | `E16` | Another pool uses the display name, or the request changes the unit of account of an oracle pool. |

---

## 9. Curated catalogs

A curated catalog is a list that a user makes.
The catalog links to existing pools and vouchers.
This resource is separate from the network pool and voucher search in section 7, Vouchers and offerings, and section 8, Pools.

All paths in this section start with `/api/v2`. Each user can own a limited number of catalogs. The default limit is 1.

### 9.1 Catalog object and common rules

#### Catalog object

Catalog reads and writes return this object.

| Field | Type | Description |
|---|---|---|
| `id` | number | Catalog ID. The ID is the stable public identity. Names do not need to be unique. |
| `name` | string | Display name. |
| `description` | string or null | Optional text. |
| `imageUrl` | string or null | Absolute `http` or `https` URL. |
| `locationName` | string | Place name. |
| `geoLat` | number | Latitude. |
| `geoLng` | number | Longitude. |
| `ownerAddress` | string | Account address of the owner. |
| `poolCount` | number | Number of pools in the catalog. |
| `voucherCount` | number | Number of active vouchers in the catalog. |
| `distanceMeters` | number or null | Distance from the caller. The value is not null only in `nearby=true` list results. |
| `tags` | array of string | Tags in sorted order. The array is empty when the catalog has no tags. |
| `createdAt` | string | RFC 3339 time. |
| `updatedAt` | string | RFC 3339 time. |

#### Member object

The member lists return this object for a pool or a voucher.

| Field | Type | Description |
|---|---|---|
| `id` | number | Internal ID of the pool or voucher. Use this ID in the membership routes. |
| `chainId` | number | Chain ID. |
| `address` | string | Contract address. |
| `name` | string | Display name, or the name, or the address when no name exists. |
| `imageUrl` | string or null | Pool image or voucher icon. |

#### Common rules

- Every successful catalog response uses status `200`, the envelope from section 3, and an empty `description`.
- Every successful catalog response has `Cache-Control: no-store`.
- Every catalog read and write has a time limit of 5 seconds. A request that takes longer returns `500 E01`.
- The public reads use the market limit (section 7.1).
- The `:id` path parameter must be a positive integer up to 2147483647. Other values return `400 E04` with the description `id must be a positive integer`.
- A catalog that does not exist returns `404 E07`.
- A caller that is not the owner of the catalog gets `403 E10`. A superadmin has no exception. Only the owner can change a catalog.
- A malformed JSON body returns `400 E02`.

#### Pagination

The catalog routes use an opaque cursor.

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | integer | `20` | From 1 to 100. Other values return `400 E04`. |
| `cursor` | string | none | The `nextCursor` value from the previous page. The maximum length is 1024 characters. |

- Each page response has `nextCursor`. The value is a string, or `null` on the last page.
- A cursor is valid only for the same list, the same caller, and the same filters.
  A cursor from another list or search returns `400 E04` with the description `catalog cursor does not match this list and location; restart pagination`.
  A malformed cursor returns `400 E04` with the description `invalid catalog cursor`.
- For `nearby=true`, a cursor also depends on the saved profile location. After the caller changes the location, the caller must start again without a cursor.

### 9.2 `GET /api/v2/catalogs`

**Access:** Public. With `nearby=true`, a user session is required.

Returns a page of catalogs. The default order is newest first (`id` descending). With `nearby=true`, the order is nearest first, then `id` ascending.

A request with `nearby=true` and no valid session returns `401 E06`. A request with `nearby=true` and a service token returns `403 E10`. A caller without a saved profile location gets `400 E04` with the description `a valid saved profile location is required`.

#### Query parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | integer | `20` | See section 9.1. |
| `cursor` | string | none | See section 9.1. |
| `nearby` | `true` or `false` | `false` | Use the saved profile location of the caller. Only catalogs within 1000 km are returned. The response is not cached. |
| `q` | string | none | Search text. Whitespace is trimmed and repeated spaces become one space. The length must be 2 to 64 characters. The search is not case-sensitive. It matches `name`, `locationName`, and `description`. A term of 2 characters matches the start of the text. A longer term matches any part of the text. |
| `tags` | string | none | Tag filter. Repeat the parameter or use a comma-separated list. At most 12 distinct values. Each value must exist in the tag vocabulary. The comparison is exact. |
| `tagMatch` | `any` or `all` | `any` | A catalog must have any of the tags, or all of the tags. |
| `pools` | string | none | Pool filter. Repeat the parameter or use a comma-separated list of `chainId:address` values. At most 100 distinct values. A catalog matches when it contains one of the pools, or contains a voucher that one of the pools allows. |
| `facets` | `true` or `false` | `false` | With `true` on the first page, add `tagFacets` to the response. |

A value of `tags` that is not in the vocabulary returns `400 E04`. A `pools` value that has a wrong form or does not exist returns `400 E04`. The tag vocabulary is described in section 11.

#### Response

| Field | Type | Description |
|---|---|---|
| `catalogs` | array of catalog object | The page. |
| `nextCursor` | string or null | See section 9.1. |
| `tagFacets` | array | Only when `facets=true` and the request has no `cursor`. |

Each `tagFacets` item is `{"tag": "Art", "count": 3}`. The array has every tag of the vocabulary, with count 0 for unused tags. The count uses `q`, `pools`, and `nearby`. It does not use `tags`, so the caller can add more tags.

```json
{
  "ok": true,
  "description": "",
  "result": {
    "catalogs": [
      {
        "id": 12,
        "name": "Community market",
        "description": null,
        "imageUrl": "https://example.com/catalog.png",
        "locationName": "Nairobi",
        "geoLat": -1.2864,
        "geoLng": 36.8172,
        "ownerAddress": "0x0000000000000000000000000000000000000001",
        "poolCount": 2,
        "voucherCount": 5,
        "distanceMeters": null,
        "tags": ["Art"],
        "createdAt": "2026-08-01T08:00:00Z",
        "updatedAt": "2026-08-02T08:00:00Z"
      }
    ],
    "nextCursor": null
  }
}
```

### 9.3 `GET /api/v2/catalogs/:id`

**Access:** Public.

Returns one catalog. The distance filter does not apply. Any catalog is visible by ID.

| Part | Description |
|---|---|
| Path | `id`: catalog ID. |
| Success | `200`. `result` is `{"catalog": <catalog object>}`. |
| Errors | `400 E04` for a bad ID. `404 E07` when the catalog does not exist. `429 E11` over the rate limit. |

### 9.4 `GET /api/v2/catalogs/:id/pools` and `GET /api/v2/catalogs/:id/vouchers`

**Access:** Public.

`GET /api/v2/catalogs/:id/pools` lists the pools of a catalog.
`GET /api/v2/catalogs/:id/vouchers` lists the vouchers of a catalog.

- The order is `id` ascending.
- The pool list has every pool in the catalog.
- The voucher list has only active vouchers. A retired voucher is hidden. The link remains, and the owner can remove it.
- The pool list and the voucher list are independent. A pool in a catalog does not add its vouchers.

Query parameters: `perPage` and `cursor` (see section 9.1). A cursor is valid only for the same catalog and the same list.

The success response is `{"items": [<member object>], "nextCursor": null}`. An empty list is an empty array. Errors: `400 E04`, `404 E07` for an unknown catalog, and `429 E11`.

```json
{
  "ok": true,
  "description": "",
  "result": {
    "items": [
      {
        "id": 7,
        "chainId": 42220,
        "address": "0x1111111111111111111111111111111111111111",
        "name": "Community pool",
        "imageUrl": null
      }
    ],
    "nextCursor": null
  }
}
```

### 9.5 `POST /api/v2/catalogs` and `PUT /api/v2/catalogs/:id`

**Access:** User session. For `PUT`, the caller must be the owner of the catalog.

Both routes use one body.
`POST /api/v2/catalogs` creates a catalog. The owner is the caller. A body field cannot set the owner.
`PUT /api/v2/catalogs/:id` replaces the editable state of a catalog. An omitted optional field is cleared.

#### Body

| Field | Type | Required | Rule |
|---|---|---|---|
| `name` | string | Yes | Trimmed. 1 to 120 Unicode characters. |
| `description` | string | No | Trimmed. At most 500 characters. An empty string clears the description. |
| `locationName` | string | Yes | Trimmed. 1 to 200 characters. |
| `geoLat` | number | Yes | From -90 to 90. |
| `geoLng` | number | Yes | From -180 to 180. |
| `imageUrl` | string or null | No | An absolute `http` or `https` URL without spaces. A missing, null, or blank value clears the image. The service stores the URL only. Upload the image with the storage flow first. |
| `tags` | array of string | No | At most 12 values. A missing array removes all tags. See the tag rules below. |

Tag rules: Each value is trimmed and repeated spaces become one space.
The value must not be blank, must not have a comma, and must have at most 100 characters.
The check is not case-sensitive, so two values that differ only in case are duplicates.
An existing tag keeps its stored spelling.
A new tag is created.
See section 11 for the creation rule.
The new list replaces the tags of the catalog.

```json
{
  "name": "Community market",
  "description": "Pools and vouchers of our market.",
  "locationName": "Nairobi",
  "geoLat": -1.2864,
  "geoLng": 36.8172,
  "imageUrl": "https://example.com/catalog.png",
  "tags": ["Art", "Culture"]
}
```

#### Response

Status `200` for both routes. Creation does not return `201`. `result` is `{"catalog": <catalog object>}`.

#### Errors

| Status | Code | When |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field breaks a rule. The description names the field. Also for tag errors. |
| `401` | `E06` | The request has no valid session. |
| `403` | `E10` | The caller uses a service token. Or the caller is not the owner (`PUT`). |
| `404` | `E07` | `PUT` and the catalog does not exist. |
| `429` | `E11` | `POST` and the caller used all the catalog slots. |

A quota error has this body. The `used` and `limit` values come from the account.

```json
{
  "ok": false,
  "description": "catalogs creation limit reached (1 of 1 used)",
  "code": "E11",
  "result": { "resource": "catalogs", "used": 1, "limit": 1 }
}
```

A superadmin has no catalog limit. Section 14, Current user, shows the usage of the caller. Section 15, Admin, shows how a superadmin sets the limit.

### 9.6 `GET /api/v2/users/me/catalogs`

**Access:** User session.

Returns the catalogs that the caller owns. There is no distance filter and no search. The order is `id` descending. Each page has 100 catalogs. The `perPage` parameter is not used. The `cursor` parameter works as in section 9.1.

The success response is `{"catalogs": [<catalog object>], "nextCursor": null}`. `nextCursor` is a string when more catalogs exist. A bad cursor returns `400 E04`.

### 9.7 `GET /api/v2/catalogs/:id/membership`

**Access:** User session. The caller must be the owner of the catalog.

Tells whether one pool or voucher is in the catalog.

| Query parameter | Type | Required | Description |
|---|---|---|---|
| `kind` | `pool` or `voucher` | Yes | Type of the resource. |
| `resourceId` | integer | Yes | The internal `id` of the pool or voucher. It is the `id` of the member object. It must be a positive integer. |

The success response is `{"added": true}` or `{"added": false}`. A resource that does not exist gives `added: false`.

Errors: `400 E04` for a bad `kind`, `resourceId`, or `:id`. `401 E06`. `403 E10` for a service token or a caller that is not the owner. `404 E07` when the catalog does not exist.

### 9.8 Membership writes

**Access:** User session. The caller must be the owner of the catalog.

| Route | Effect |
|---|---|
| `PUT /api/v2/catalogs/:id/pools/:poolId` | Adds the pool to the catalog. |
| `DELETE /api/v2/catalogs/:id/pools/:poolId` | Removes the pool from the catalog. |
| `PUT /api/v2/catalogs/:id/vouchers/:voucherId` | Adds the voucher to the catalog. |
| `DELETE /api/v2/catalogs/:id/vouchers/:voucherId` | Removes the voucher from the catalog. |

The membership read is in section 9.7.
The `:poolId` and `:voucherId` values are internal IDs. They must be positive integers.

- The routes are idempotent. A second `PUT` or `DELETE` gives the same final state.
- `PUT` returns `{"added": true}`. `DELETE` returns `{"added": false}`.
- The owner can add any pool. The owner can add only an active voucher. A pool or voucher that does not exist, or a voucher that is not active, returns `404 E07`.
- A voucher without map coordinates can be in a catalog.
- A catalog change does not change the pool or the voucher.
- `DELETE` of a link that does not exist succeeds.

Errors: `400 E04` for a bad ID. `401 E06`. `403 E10` for a service token or a caller that is not the owner. `404 E07` for an unknown catalog, or for a resource that cannot be added.

---

## 10. Posts

Posts are community reports. The API path uses the word `reports`. A post has text, optional images and attachments, tags, and links to vouchers and pools. Every new or edited post must link at least one voucher or pool.

All paths in this section start with `/api/v2`.
This section covers the public reads, the author writes, and the reads of an author for the author own posts.
The global admin reads (`GET /api/v2/admin/reports` and `GET /api/v2/admin/reports/:id`) and the review notifications are in section 15, Admin.

### 10.1 Access summary

| Operation | Access |
|---|---|
| `GET /api/v2/reports` and `GET /api/v2/reports/:id` | Public. Shows only `APPROVED` posts. With `nearby=true`, a user session is required. |
| `POST /api/v2/reports` | User session. |
| `PUT /api/v2/reports/:id` | User session. The caller must be the author or a superadmin. |
| `PUT /api/v2/reports/:id/approve` | Superadmin. |
| `DELETE /api/v2/reports/:id` | Superadmin. |
| `GET /api/v2/users/me/reports` and `GET /api/v2/users/me/reports/:id` | User session. Shows the posts of the caller in every status. |

### 10.2 Post object

A post list item has the fields below. The detail response has more fields.

| Field | Type | In list | In detail | Description |
|---|---|---|---|---|
| `id` | number | Yes | Yes | Post ID. |
| `title` | string | Yes | Yes | Title. |
| `description` | string | Yes | Yes | Summary. |
| `imageUrl` | string or null | Yes | Yes | Cover image. The value is null when the stored value is not an `http` or `https` URL. |
| `geoLat` | number or null | Yes | Yes | Latitude. |
| `geoLng` | number or null | Yes | Yes | Longitude. |
| `periodFrom` | string or null | Yes | Yes | RFC 3339 time. |
| `periodTo` | string or null | Yes | Yes | RFC 3339 time. |
| `authorAddress` | string | Only in managed user/admin lists | Only in managed user/admin detail | The account of the original author. Public responses never include it. |
| `status` | string | Only in managed user/admin lists | Only in managed user/admin detail | `DRAFT`, `SUBMITTED`, `APPROVED`, or `REJECTED`. Public responses never include it. |
| `createdAt` | string | Yes | Yes | RFC 3339 time. |
| `updatedAt` | string | Yes | Yes | RFC 3339 time. |
| `tags` | array of string | Yes | Yes | Tags in sorted order. |
| `vouchers` | array | Yes | Yes | Linked vouchers: `{"id", "chainId", "address", "name", "symbol"}`. `name` and `symbol` can be null. |
| `pools` | array | Yes | Yes | Linked pools: `{"id", "chainId", "address", "name", "symbol"}`. |
| `body` | string | No | Yes | Markdown text. Omitted when empty. |
| `hostName` | string | No | Yes | Free text. Omitted when not set. |
| `participants` | string | No | Yes | Free text. Omitted when not set. |
| `timeTaken` | string | No | Yes | Free text. Omitted when not set. |
| `images` | array | No | Yes | `{"url", "width", "height", "maxWidth", "maxHeight", "byteSize", "caption"}`. Each number and `caption` can be null. `width` and `height` are the display size. `maxWidth` and `maxHeight` are the original size. Omitted when empty. |
| `attachments` | array | No | Yes | `{"kind", "url", "filename", "byteSize"}`. `kind` is `FILE` or `EMBED`. `filename` is set only for uploaded files. Omitted when empty. |

The service stores only `http` and `https` URLs for images and attachments.

### 10.3 `GET /api/v2/reports`

**Access:** Public. With `nearby=true`, a user session is required.

Returns a page of `APPROVED` posts.
The order is `updatedAt` descending, then `id` descending.
The route uses the market limit (section 7.1).
A request with `nearby=true` and no valid session returns `401 E06`.
A service token returns `403 E10`.
The response has no `Cache-Control` header, except for `nearby=true`, which sets `private, no-store`.

#### Query parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | integer | `20` | `0` or no value means 20. A value above 100 becomes 100. A value that is not a non-negative integer returns `400 E04`. |
| `cursorUpdatedAt` | string | none | RFC 3339 time (nanoseconds allowed). Send with `cursorId`. |
| `cursorId` | integer | none | Positive integer. Send with `cursorUpdatedAt`. |
| `q` | string | none | Search in `title` and `description`. The length must be 2 to 64 characters after trimming. A term of 2 characters matches the start of the text. A longer term matches any part of the text. |
| `tags` | string | none | Repeat the parameter or use a comma-separated list. At most 12 distinct values. Each value must exist in the tag vocabulary. |
| `tagMatch` | `any` or `all` | `any` | A post must have any of the tags, or all of the tags. |
| `vouchers` | string | none | Repeat the parameter or use a comma-separated list of `chainId:address` values. At most 12 distinct values. Every voucher must exist. |
| `voucherMatch` | `any` or `all` | `any` | A post must link any of the vouchers, or all of the vouchers. |
| `pools` | string | none | Repeat the parameter or use a comma-separated list of `chainId:address` values. At most 12 distinct values. Every pool must exist. |
| `poolMatch` | `any` or `all` | `any` | A post must link any of the pools, or all of the pools. |
| `nearby` | `true` or `false` | `false` | Show only posts with coordinates within 1000 km of the saved profile location of the caller. The order does not change. A caller without a saved location gets `400 E04`. |
| `facets` | `true` or `false` | `false` | With `true` and no cursor, add `tagFacets`, `voucherFacets`, and `poolFacets`. |

A cursor needs both `cursorUpdatedAt` and `cursorId`. One alone returns `400 E04`. A bad time or ID returns `400 E04`. A tag that is not in the vocabulary returns `400 E04`. A voucher or pool that is not valid, or does not exist, returns `400 E04`. The obsolete public `author` parameter is rejected with `400 E04` so a caller cannot enumerate a wallet's public posts.

#### Response

| Field | Type | Description |
|---|---|---|
| `reports` | array of post | The page. The array is empty when there are no matches. |
| `next` | object or null | `{"updatedAt": "<RFC 3339 time>", "id": 4633}`. Send the two values as `cursorUpdatedAt` and `cursorId` to get the next page. The value is `null` when the page has fewer than `perPage` posts. A full last page can still give a `next` value, and the next page is then empty. |
| `tagFacets` | array | Only with `facets=true` and no cursor. Items are `{"tag", "count"}`. It lists only tags that appear on a matching post. The count ignores `tags`. |
| `voucherFacets` | array | Only with `facets=true` and no cursor. Items are `{"id", "chainId", "address", "name", "symbol", "count"}`. The count ignores `vouchers`. |
| `poolFacets` | array | Only with `facets=true` and no cursor. Items are `{"id", "chainId", "address", "name", "symbol", "count"}`. The count ignores `pools`. |

```json
{
  "ok": true,
  "description": "Reports",
  "result": {
    "reports": [
      {
        "id": 4633,
        "title": "August community harvest",
        "description": "Twenty members took part.",
        "imageUrl": null,
        "geoLat": -1.2864,
        "geoLng": 36.8172,
        "periodFrom": null,
        "periodTo": null,
        "createdAt": "2026-08-01T08:00:00Z",
        "updatedAt": "2026-08-02T08:00:00Z",
        "tags": ["Agriculture"],
        "vouchers": [
          {"id": 21, "chainId": 100, "address": "0x1111111111111111111111111111111111111111", "name": "Harvest", "symbol": "HRV"}
        ],
        "pools": []
      }
    ],
    "next": { "updatedAt": "2026-08-02T08:00:00Z", "id": 4633 }
  }
}
```

### 10.4 `GET /api/v2/reports/:id`

**Access:** Public.

Returns one post with the detail fields in section 10.2.
`result` is `{"report": <post>}` and `description` is `Report`.
The route returns only `APPROVED` posts.
A post in another status gives `404 E03`, the same as a post that does not exist.
The route uses the market limit (section 7.1).
The response has no `Cache-Control` header.

| Status | Code | When |
|---|---|---|
| `400` | `E04` | `:id` is not a positive integer. |
| `404` | `E03` | The post does not exist or is not `APPROVED`. |
| `429` | `E11` | Over the rate limit. |

### 10.5 Write body for `POST /api/v2/reports` and `PUT /api/v2/reports/:id`

Both routes use the same body.
`PUT` replaces all editable fields and all five collections.
An omitted optional field is cleared. An omitted collection becomes empty.
A failed request changes nothing.
At least one of `vouchers` or `pools` must be non-empty.

The body cannot set `authorAddress`, `status`, or a time. The service takes the author from the session.

| Field | Type | Required | Rule |
|---|---|---|---|
| `title` | string | Yes | Trimmed. Not blank. At most 200 characters. |
| `body` | string | No | Markdown. At most 500000 characters. |
| `description` | string | No | At most 10000 characters. |
| `imageUrl` | string | No | An `http` or `https` URL. |
| `geoLat` | number | No | From -90 to 90. Send with `geoLng`. |
| `geoLng` | number | No | From -180 to 180. Send with `geoLat`. |
| `periodFrom` | string | No | RFC 3339 time. Must not be after `periodTo`. |
| `periodTo` | string | No | RFC 3339 time. |
| `hostName` | string | No | Trimmed. At most 200 characters. A blank value is cleared. |
| `participants` | string | No | Trimmed. At most 100 characters. A blank value is cleared. |
| `timeTaken` | string | No | Trimmed. At most 100 characters. A blank value is cleared. |
| `tags` | array of string | No | At most 12 values. Each value must not be blank and has at most 100 characters. No duplicates. A tag that is not in the vocabulary is created. See section 11. |
| `vouchers` | array | No | At most 12 items: `{"chainId": 42220, "address": "0x..."}`. `chainId` must be positive. `address` must be a hex address. No duplicates. Every voucher must exist. |
| `pools` | array | No | At most 12 items: `{"chainId": 100, "address": "0x..."}`. `chainId` must be positive. `address` must be a hex address. No duplicates. Every pool must exist. |
| `images` | array | No | At most 50 items. No duplicate URLs. See below. |
| `attachments` | array | No | At most 20 items. No duplicate `kind` and `url` pairs. See below. |

Each item of `images` has these fields. `url` is required and must be an `http` or `https` URL. `width`, `height`, `maxWidth`, `maxHeight`, and `byteSize` are optional integers greater than 0. `caption` is optional and has at most 500 characters.

Each item of `attachments` has these fields.
`kind` is required and is `FILE` or `EMBED`.
`url` is required and must be an `http` or `https` URL.
`filename` is optional and has at most 255 characters.
`byteSize` is an optional integer greater than 0.
The same URL can appear twice when the two items have different `kind` values.

A caller that is not a superadmin can link only vouchers and pools that the caller currently owns. Another account's resource returns `403 E10`. A superadmin can link any catalog voucher or pool. Existing links remain stored after an ownership transfer, but a regular author must remove any resource no longer owned before saving another edit.

```json
{
  "title": "August community harvest",
  "body": "## Harvest\n\nTwenty members took part.",
  "description": "A summary for the feed.",
  "imageUrl": "https://example.com/posts/cover.jpg",
  "geoLat": -1.2864,
  "geoLng": 36.8172,
  "periodFrom": "2026-08-01T08:00:00Z",
  "periodTo": "2026-08-01T12:00:00Z",
  "hostName": "Example Group",
  "participants": "20",
  "timeTaken": "4 hours",
  "tags": ["Agriculture", "Youth"],
  "vouchers": [
    { "chainId": 42220, "address": "0x1111111111111111111111111111111111111111" }
  ],
  "pools": [],
  "images": [
    { "url": "https://example.com/posts/1.jpg", "width": 720, "height": 1080, "caption": "Sorting" }
  ],
  "attachments": [
    { "kind": "FILE", "url": "https://example.com/posts/notes.pdf", "filename": "notes.pdf", "byteSize": 81920 }
  ]
}
```

### 10.6 `POST /api/v2/reports`

**Access:** User session.

Creates a post. The status is `APPROVED` at once. There is no review queue. Status `201`. `description` is `Report created`. `result` is `{"report": <post>}` with the detail fields.

| Status | Code | When |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field breaks a rule in section 10.5. Also for a voucher or pool that does not exist, and for a tag error. |
| `401` | `E06` | The request has no valid session. |
| `403` | `E10` | The caller uses a service token. Or the caller links a voucher or pool that the caller does not own. |

### 10.7 `PUT /api/v2/reports/:id`

**Access:** User session. The caller must be the original author or a superadmin.

Replaces the post with the body in section 10.5. Status `200`. `description` is `Report updated`. `result` is `{"report": <post>}`.

| Caller | Status after the edit |
|---|---|
| Original author | `APPROVED` |
| Superadmin | Unchanged |

The body is validated first, then the author, then the tags, vouchers, and pools.

| Status | Code | When |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | `:id` is not a positive integer, or a field breaks a rule. |
| `401` | `E06` | The request has no valid session. |
| `403` | `E10` | The caller is not the author and not a superadmin. Or a service token. Or a voucher or pool that the caller does not own. |
| `404` | `E03` | The post does not exist. |

### 10.8 `PUT /api/v2/reports/:id/approve`

**Access:** Superadmin.

Sets the status of the post to `APPROVED`. The request has no body. The route changes nothing else. A post that is already `APPROVED` stays the same and returns `200`. Status `200`. `description` is `Report approved`. `result` is `{"report": <post>}`.

Errors: `400 E04` for a bad `:id`. `403 E10` for a caller that is not a superadmin. `404 E03` when the post does not exist.

### 10.9 `DELETE /api/v2/reports/:id`

**Access:** Superadmin.

Deletes the post with its tags, voucher links, pool links, images, and attachments. An author cannot delete the own post. Status `200`. `description` is `Report deleted`. `result` is `{"id": 4633}`.

Errors: `400 E04` for a bad `:id`. `403 E10` for a caller that is not a superadmin. `404 E03` when the post does not exist.

### 10.10 `GET /api/v2/users/me/reports`

**Access:** User session.

Returns the posts that the caller wrote, in every status. The author is the caller. The `author` parameter is not used. The response has `Cache-Control: no-store`.

#### Query parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | integer | `20` | Same rule as section 10.3. |
| `status` | string | none | `DRAFT`, `SUBMITTED`, `APPROVED`, or `REJECTED`. The case does not matter. No value shows every status. Another value returns `400 E04`. |
| `cursorUpdatedAt`, `cursorId` | string, integer | none | Send both or neither. One alone returns `400 E04`. |
| `next` | `true` or `false` | `true` when a cursor exists | `true` gets the older page. `false` gets the newer page. `next` without a cursor returns `400 E04`. |
| `q`, `tags`, `tagMatch`, `vouchers`, `voucherMatch` | | | Same rules as section 10.3. |

The parameters `nearby` and `facets` have no effect on this route.

#### Response

The order is always `updatedAt` descending, then `id` descending. Each item is a list item with `status` (see section 10.2).

| Field | Type | Description |
|---|---|---|
| `reports` | array | The page. |
| `first` | object or null | `{"updatedAt", "id"}` of the first post. Send it with `next=false` for the newer page. |
| `last` | object or null | `{"updatedAt", "id"}` of the last post. Send it with `next=true` for the older page. |

`first` and `last` are `null` when the page is empty.

Errors: `400 E04` for a bad parameter. `401 E06`. `403 E10` for a service token.

### 10.11 `GET /api/v2/users/me/reports/:id`

**Access:** User session.

Returns one post that the caller wrote, in any status, with the detail fields. `description` is `Report`. `result` is `{"report": <post>}`. The response has `Cache-Control: no-store`.

The post of another author returns `404 E03`, the same as a post that does not exist. Errors: `400 E04` for a bad `:id`. `401 E06`. `403 E10` for a service token.

---

## 11. Lookups and shared vocabulary

This section describes the read routes for lookups and the shared vocabulary. All paths start with `/api/v2`.

### 11.1 Shared vocabulary

Three global tables hold the values that clients show in pickers.

| Vocabulary | Used for | Value limit |
|---|---|---|
| `token_tag` | Tags on vouchers, pools, posts, and curated catalogs. | 100 characters. No comma. |
| `offering_unit` | The `unit` of an offering. | 32 characters. |
| `unit_of_account` | The unit of account of a voucher, a pool, or a user profile. | 32 characters. |

Each table has built-in values and custom values. A custom value has `isCustom: true` where the API shows the flag.

#### How values are created

There is no route to create a tag or an offering unit.
The write that names the value creates it.
A request that fails leaves no value behind.

| Write | Creates |
|---|---|
| `POST /api/v2/contracts/pool` | Each `tags` value and the `unitOfAccount`. |
| `POST /api/v2/contracts/token` | Each `tags` value and the `voucherUoa`, when sent. |
| `PUT /api/v2/vouchers/:address/profile` and `PUT /api/v2/admin/vouchers/:address/profile` | Each `tags` value and the `voucherUoa`. |
| `PUT /api/v2/pools/:address/profile` and `PUT /api/v2/admin/pools/:address/profile` | Each `tags` value and the `unitOfAccount`. |
| `POST /api/v2/vouchers/:address/offerings` and `PUT /api/v2/vouchers/:address/offerings/:id` | The `unit` of the offering. |
| `POST /api/v2/reports` and `PUT /api/v2/reports/:id` | Each `tags` value. |
| `POST /api/v2/catalogs` and `PUT /api/v2/catalogs/:id` | Each `tags` value. |
| `POST /auth/profile` and `PUT /api/v2/admin/users/:address/profile` | The `preferredCurrency`. |

An administrator can also create a custom unit of account with a route in section 15, Admin. That section also describes how a superadmin lists, renames, and deletes vocabulary values.

#### Rules for a value

1. The service trims the value and changes repeated spaces to one space.
2. A blank value returns `400 E04`.
3. The service looks for an existing value, and the comparison is not case-sensitive. An existing value always wins. For example, `KG` resolves to the built-in `kg`. The response and the stored data use the existing spelling.
4. If no value exists, the service creates a custom value in Title Case. Each word starts with a capital letter. The rest is lower case. For example, `  urban   FARMING ` becomes `Urban Farming`.
5. Two requests that create the same new value at the same time make one value. Both requests succeed.

| Rule | Tags | Offering units | Units of account |
|---|---|---|---|
| Maximum length | 100 characters | 32 characters | 32 characters |
| A comma | `400 E04` | Allowed | Allowed |
| A duplicate in one request (not case-sensitive) | `400 E04` | Not applicable | Not applicable |

A tag has no comma because the tag filter uses a comma-separated list. A custom unit of account cannot be used with oracle pricing when the pool is deployed. That request returns `400 E04`.

#### Tag filters are closed

The `tags` filter on a read does not create values.
A filter value that is not in the tag vocabulary returns `400 E04`.
This rule applies to `GET /api/v2/catalogs`, `GET /api/v2/reports`, `GET /api/v2/users/me/reports`, and to the voucher, offering, and pool reads in sections 7 and 8.
To get the valid values, call `GET /api/v2/token-tags`, or send `facets=true` on a request without `tags`.

### 11.2 `GET /api/v2/token-tags`

**Access:** User or service token.

Returns every tag, built-in and custom, sorted by value.

```json
{
  "ok": true,
  "description": "Available token tags",
  "result": { "tags": ["Agriculture", "Art", "Culture"] }
}
```

### 11.3 `GET /api/v2/offering-units`

**Access:** User or service token.

Returns every offering unit, built-in and custom, sorted by value.

```json
{
  "ok": true,
  "description": "Available offering units",
  "result": { "units": ["bag", "kg", "Produce Crate"] }
}
```

### 11.4 `GET /api/v2/units-of-account`

**Access:** User session. A service token returns `403 E10`.

Returns every unit of account. Built-in values come first, then custom values. Each group is sorted by value.

```json
{
  "ok": true,
  "description": "Available units of account",
  "result": {
    "unitsOfAccount": [
      { "value": "KES", "isCustom": false },
      { "value": "Community Credits", "isCustom": true }
    ]
  }
}
```

### 11.5 `GET /api/v2/symbols`

**Access:** User or service token.

Shows the symbols that are in use under one symbol stem. Use it before a voucher or pool deployment. The result is advice only. It does not reserve a symbol.

| Query parameter | Type | Required | Rule |
|---|---|---|---|
| `kind` | `voucher` or `pool` | Yes | Type of the symbol. |
| `prefix` | string | Yes | 1 to 6 letters or digits. |

The service changes `prefix` to upper case.
A symbol is taken when it equals the prefix, or when it is the prefix followed by 1 or 2 digits.
Taken symbols come from existing vouchers, pools, and open deployments.
The response has at most 128 symbols in sorted order.
It has `Cache-Control: no-store`.

```json
{
  "ok": true,
  "description": "Taken symbols",
  "result": { "kind": "voucher", "prefix": "FARM", "taken": ["FARM", "FARM1"] }
}
```

An invalid `kind` or `prefix` returns `400 E04`.

### 11.6 `GET /api/v2/names`

**Access:** User or service token.

Tells whether a voucher name or a pool name is free. Use it before a deployment or a rename. The result is advice only. The deployment makes the real claim.

| Query parameter | Type | Required | Rule |
|---|---|---|---|
| `kind` | `voucher` or `pool` | Yes | Type of the name. |
| `name` | string | Yes | The service trims it and changes repeated spaces to one space. It must not be blank. A voucher name has at most 120 characters. A pool name has at most 80 characters. |
| `exclude` | string | No | A hex address. The name of this voucher or pool does not count as taken. Use it for a rename form. |

The response has `Cache-Control: no-store`. The `name` value in the response is the normalized name that the service compared.

```json
{
  "ok": true,
  "description": "Name availability",
  "result": { "kind": "pool", "name": "Community pool", "available": true }
}
```

An invalid parameter returns `400 E04`.

### 11.7 Recipient routes

A recipient is a public identity that can receive a payment: an approved user with a username, or a pool with a name. A recipient has this shape.

| Field | Type | Description |
|---|---|---|
| `kind` | `user` or `pool` | Type of the recipient. |
| `name` | string | For a user, the username. Profile names are never returned. For a pool: the display name or the name. |
| `alias` | string | The username, or for a pool the display name or the name. |
| `address` | string | Account address. |
| `chainId` | number | Chain ID. For a user, the chain ID of the service. |

A recipient response never has a phone number or contact data. Every recipient route sets `Cache-Control: no-store`.

#### Username text rules

- A username has 1 to 80 Unicode code points after the service trims it and changes it to NFC form.
- A username must not be blank. An explicit blank username is rejected. If a client sends no username, the service keeps the automatic assignment.
- The service keeps case, internal spaces, punctuation, digits, and all scripts for display.
- A username and a pool name share one namespace.
  The comparison trims the text, changes it to NFC form, makes repeated whitespace one space, and makes it lower case.
  The lower-case rule depends on the locale of the server.
  A taken name returns `409 E16`.
- A suggested name adds a number to the end when the name is taken. The service cuts the text by code point so that the result has at most 80 characters.

#### `GET /api/v2/recipients`

**Access:** User or service token.

Searches recipients.

| Query parameter | Type | Required | Rule |
|---|---|---|---|
| `q` | string | Yes | Trimmed. 1 to 120 characters. |
| `kind` | `user` or `pool` | No | Filter by type. |

Rules:

- The route returns at most 20 recipients, in `result.recipients`. The array is empty when there are no matches.
- A user matches only when `q` is the exact username after case-insensitive normalization. Prefixes and partial usernames return no user. A pool name remains a public partial-text search.
- Only approved users with a username are searched. Pools are limited to the chain of the service.
- The order is: exact name matches first, then the lower-case alias, then `kind`, then `address`.
- Phone search: the service treats `q` as a phone number when it has digits. It can also have a leading `+`, spaces, parentheses and hyphens.
  After the service removes these characters, the complete normalized phone number must exactly match the stored number. Phone prefixes return nothing.
  The route keeps every digit, including leading zeros.
  For example, `071` is not changed to an international number.
- A phone search returns only users for the phone part. A pool phone is never searched.
- A phone number is never in the response.
- This route uses a recipient-specific throttle keyed by the caller identity, or by client IP for non-user credentials: one request per second with a burst of 30. It does not consume the Marketplace rate-limit budget.
- A bad `q` or `kind` returns `400 E04`.

```json
{
  "ok": true,
  "description": "",
  "result": {
    "recipients": [
      {
        "kind": "user",
        "name": "amina",
        "alias": "amina",
        "address": "0x0000000000000000000000000000000000000001",
        "chainId": 42220
      }
    ]
  }
}
```

#### `GET /api/v2/recipients/resolve` and `GET /api/v2/recipients/resolve/:name`

**Access:** User or service token.

Finds the one recipient with an exact name. Use the query form `GET /api/v2/recipients/resolve?name=...` for a name with punctuation or a slash. The path form `GET /api/v2/recipients/resolve/:name` works for simple names. If both are present, the `name` query parameter wins.

The name must not be blank and has at most 120 characters. The match is exact after the normalization in the username rules. The route reads the current identity claims. A name that a deployment only reserved does not resolve.

Success: `result` is `{"recipient": <recipient>}`.

| Status | Code | When |
|---|---|---|
| `400` | `E02` | The name is blank or longer than 120 characters. |
| `404` | `E07` | No recipient has this name. |
| `409` | `E16` | More than one recipient matches. |

#### `GET /api/v2/recipients/reverse/:address`

**Access:** User or service token.

Finds the recipient identity of an account address. The comparison ignores case. Use it to show a name for an address. An address with no recipient is still a valid destination.

Success: `result` is `{"recipient": <recipient>}`.

| Status | Code | When |
|---|---|---|
| `400` | `E02` | `:address` is not a hex address. |
| `404` | `E07` | No recipient has this address. |
| `409` | `E16` | More than one recipient matches. |

#### `GET /api/v2/recipients/suggest`

**Access:** Public. The route uses the market limit (section 7.1).

Suggests a free username. The route does not reserve the name. The unique rule is checked when the user saves the username.

| Query parameter | Type | Required | Rule |
|---|---|---|---|
| `hint` | string | Yes in practice | Trimmed. At most 120 characters. The service changes it to NFC form and cuts it to 80 code points. |

The service tries the hint first. If it is taken, the service adds the numbers 1 to 99 to the end. It returns the first free name. The success response is `{"username": "Amina2"}`.

| Status | Code | When |
|---|---|---|
| `400` | `E04` | `hint` is longer than 120 characters. |
| `409` | `E16` | The hint is blank, or no free name exists in the 100 candidates. |
| `429` | `E11` | Over the rate limit. |

### 11.8 `GET /api/v2/oracle-feeds`

**Access:** User or service token.

Lists the price oracle feeds that a pool can use for oracle pricing, sorted by `name`.

| Query parameter | Type | Default | Description |
|---|---|---|---|
| `chainId` | integer | The chain ID of the service | Must be a positive integer. Another value returns `400 E04`. |

Each feed has these fields: `id`, `chainId`, `name`, `address`, `createdAt`, `updatedAt`.

```json
{
  "ok": true,
  "description": "Available oracle feeds",
  "result": {
    "feeds": [
      {
        "id": 1,
        "chainId": 42220,
        "name": "KES/USD",
        "address": "0x1111111111111111111111111111111111111111",
        "createdAt": "2026-08-01T08:00:00Z",
        "updatedAt": "2026-08-01T08:00:00Z"
      }
    ]
  }
}
```

---

## 12. Account data

These routes read balances, transfers and activity for any account.
They read the indexed chain data.
The routes exist only when the service has federation configured.
Without it, the routes are not registered.

**Access** for all routes in this section: User or service token.
Any authenticated caller can read the data of any address.

**Path parameter** for all routes in this section:

| Name | Type | Rules |
|---|---|---|
| `address` | string | Required. Checksummed address. A bad value returns `400` with code `E04`. |

**Token metadata.**
The indexed chain data holds chain facts only.
The API fills `tokenSymbol`, `tokenName`, `tokenDecimals` and `iconUrl` from the local voucher catalog, matched by token address.
The name is the display name if one is set.
A token that is not in the catalog has empty `tokenSymbol` and `tokenName`, `tokenDecimals` 0, and no `iconUrl`.

**Amounts.**
Amounts and balances are decimal strings of raw integer units.
Use `tokenDecimals` to format them.
Do not use floating-point numbers to parse them.

**Cursor pagination** applies to `/tokens`, `/tokens/issued`, `/transfers` and `/activity`.

| Query | Type | Default | Rules |
|---|---|---|---|
| `perPage` | integer | 100 | A value of 0, or above 100, becomes 100. |
| `cursor` | integer | none | An `id` from a previous page. |
| `next` | boolean | `false` | Sets the direction. |

The route sorts every page by `id`, highest first.
The first request has no `cursor` and no `next`.
To get older rows, send `next=true` and `cursor` set to `last`.
To get newer rows, leave `next` out and send `cursor` set to `first`.
A wrong type in a query parameter returns `400` with code `E02`.
Every paged response has `first` (the `id` of the first item) and `last` (the `id` of the last item).
Both are 0 when the page is empty.
The list can be `null` when the page is empty.

**Common errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E02` | A query parameter has a wrong type. |
| 400 | `E04` | `address` is not valid, or a search value is not valid. |
| 401 | `E06` | No valid credential. |
| 404 | `E03` | Federation is not configured, so the route does not exist. |

### 12.1 `GET /api/v2/accounts/:address/tokens/latest`

**Access:** User or service token.

Returns up to 5 token balances of an account, most recent movement first.
The balance is the sum of the successful transfers of the account for each token.
Only balances above 0 count.
The route skips tokens that the catalog marks as retired.
It looks further back in the history to fill the 5 places.
This route has no pagination.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Latest token balances for 0x0000000000000000000000000000000000000001",
  "result": {
    "tokens": [
      {
        "id": 1043746,
        "tokenAddress": "0x1111111111111111111111111111111111111111",
        "tokenSymbol": "VCH",
        "tokenName": "Example Voucher",
        "tokenDecimals": 6,
        "balance": "100000",
        "iconUrl": "https://example.com/icon.png",
        "lastDateBlock": "2026-07-01T12:20:29Z",
        "catalogActive": true
      }
    ]
  }
}
```

**Token balance fields** (also used by `/tokens` and `/tokens/issued`):

| Field | Type | Meaning |
|---|---|---|
| `id` | number | Cursor value. For holdings it is the ID of the last transaction that moved the token. |
| `tokenAddress` | string | Token contract address. |
| `tokenSymbol` | string | From the catalog. |
| `tokenName` | string | From the catalog. |
| `tokenDecimals` | number | From the catalog. |
| `balance` | string | Raw units. |
| `iconUrl` | string | From the catalog. Not present if there is none. |
| `lastDateBlock` | string | Time of the last movement. |
| `catalogActive` | boolean | `false` if the voucher is retired. Not present if the token is not in the catalog. |

### 12.2 `GET /api/v2/accounts/:address/tokens/issued`

**Access:** User or service token.

Returns the active catalog vouchers that the address owns, each with the balance of the address.
The route uses cursor pagination (see the start of this section).
The route sorts rows by catalog voucher ID, newest first.

Unlike `/tokens`, this route keeps a zero balance.
A voucher that the owner never moved has `balance` `"0"` and a `lastDateBlock` of `0001-01-01T00:00:00Z`.
Retired vouchers are not listed.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Issued tokens for 0x0000000000000000000000000000000000000001",
  "result": {
    "tokens": [
      {
        "id": 12,
        "tokenAddress": "0x1111111111111111111111111111111111111111",
        "tokenSymbol": "VCH",
        "tokenName": "Example Voucher",
        "tokenDecimals": 6,
        "balance": "0",
        "lastDateBlock": "0001-01-01T00:00:00Z",
        "catalogActive": true
      }
    ],
    "first": 12,
    "last": 12
  }
}
```

The `id` is the catalog ID of the voucher. For the other fields, see section 12.1.

### 12.3 `GET /api/v2/accounts/:address/tokens`

**Access:** User or service token.

Returns all token balances above 0 for an account.
The route uses cursor pagination (see the start of this section).
The route sorts rows by the ID of the last transaction that moved the token, highest first.
The page can have more than `perPage` rows when two tokens share the same last transaction.
The route does not hide retired vouchers. Check `catalogActive`.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Token balances for 0x0000000000000000000000000000000000000001",
  "result": {
    "tokens": [
      {
        "id": 1043746,
        "tokenAddress": "0x1111111111111111111111111111111111111111",
        "tokenSymbol": "VCH",
        "tokenName": "Example Voucher",
        "tokenDecimals": 6,
        "balance": "100000",
        "lastDateBlock": "2026-07-01T12:20:29Z",
        "catalogActive": true
      }
    ],
    "first": 1043746,
    "last": 1043746
  }
}
```

For the fields, see section 12.1.

### 12.4 `GET /api/v2/accounts/:address/transfers`

**Access:** User or service token.

Returns the raw token transfers of an account.
The route uses cursor pagination (see the start of this section).
The route sorts rows by transfer ID, highest first.
The list includes failed transactions. Check `success`.
A transfer from an account to itself shows once, as `IN`.

**Extra query parameter:**

| Name | Type | Rules |
|---|---|---|
| `q` | string | Optional search text. White space is collapsed. From 2 to 66 characters. |

`q` matches the transaction hash, the counterparty address, the token address, and the tokens of catalog vouchers that match the text.
The filter runs before paging.
A value of 1 character returns `400` with code `E04` and the description `q must be at least 2 characters`.
A value above 66 characters returns `400` with code `E04` and the description `q must be at most 66 characters`.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Transfer history for 0x0000000000000000000000000000000000000001",
  "result": {
    "transfers": [
      {
        "id": 743839,
        "txHash": "0x1111111111111111111111111111111111111111111111111111111111111111",
        "dateBlock": "2026-07-01T12:20:29Z",
        "direction": "IN",
        "counterpartyAddress": "0x0000000000000000000000000000000000000002",
        "senderAddress": "0x0000000000000000000000000000000000000002",
        "recipientAddress": "0x0000000000000000000000000000000000000001",
        "tokenAddress": "0x1111111111111111111111111111111111111111",
        "tokenSymbol": "VCH",
        "tokenName": "Example Voucher",
        "tokenDecimals": 6,
        "amount": "100000",
        "success": true,
        "uoaValue": "125",
        "uoa": "Community Hours",
        "hasNote": false
      }
    ],
    "first": 743839,
    "last": 743839
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | number | Transfer ID. Use it as the cursor. |
| `txHash` | string | Transaction hash. |
| `dateBlock` | string | Block time. |
| `direction` | string | `IN` or `OUT`, from the view of `address`. |
| `counterpartyAddress` | string | The other party. |
| `senderAddress`, `recipientAddress` | string | The parties of the transfer. |
| `tokenAddress`, `tokenSymbol`, `tokenName`, `tokenDecimals` | string, string, string, number | The token. |
| `amount` | string | Raw units. |
| `success` | boolean | `false` if the transaction reverted. |
| `uoaValue` | string | Value of the amount in the unit of account. Not present if there is no value. |
| `uoa` | string | Name of the unit of account. Not present if there is no value. |
| `hasNote` | boolean | `true` if the caller can read a private attachment for this transaction. |

**Value of a transfer.**
The relayer stores a value claim of each voucher when it submits a transfer.
The API multiplies the raw amount by that stored claim.
`uoaValue` and `uoa` are present only if the claim exists and the voucher has decimals in the catalog.
Transfers that did not go through the relayer have no value.

**Notes.**
`hasNote` is `true` only for a caller who is the sender or the recipient of a private attachment (see section 14, `GET /api/v2/users/me/shares/:txHash`).
For a service token, `hasNote` is always `false`.

### 12.5 `GET /api/v2/accounts/:address/activity`

**Access:** User or service token.

Returns the activity feed of an account.
The feed has one row for each action that a user sees.
The user can see creation, sent, received and swapped actions.
All swap events that the account started in one transaction form one `swapped` row.
The transfers that belong to a swap are not shown again as `sent` or `received`.
Other transfers in the same transaction stay visible.
Use `GET /api/v2/accounts/:address/transfers` for the raw transfers.

The route uses cursor pagination (see the start of this section).
The route sorts rows by `id`, highest first.
The `id` is an opaque number. Use it only as a cursor.

**Extra query parameters:**

| Name | Type | Rules |
|---|---|---|
| `q` | string | Optional search text. Same rules as for `/transfers`. It also matches pool addresses. |
| `types` | string | Optional filter. Values: `creation`, `sent`, `received`, `swapped`. Send a comma-separated list, repeat the parameter, or both. The comparison ignores case. No value means all types. |

An unknown `types` value returns `400` with code `E04` and the description `types contains an unsupported activity type`.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Activity for 0x0000000000000000000000000000000000000001",
  "result": {
    "activity": [
      {
        "id": 98765000001,
        "kind": "received",
        "txHash": "0x1111111111111111111111111111111111111111111111111111111111111111",
        "dateBlock": "2026-07-01T12:20:29Z",
        "success": true,
        "counterpartyAddress": "0x0000000000000000000000000000000000000002",
        "senderAddress": "0x0000000000000000000000000000000000000002",
        "recipientAddress": "0x0000000000000000000000000000000000000001",
        "transfer": {
          "tokenAddress": "0x1111111111111111111111111111111111111111",
          "tokenName": "Example Voucher",
          "tokenSymbol": "VCH",
          "tokenDecimals": 6,
          "amount": "100000"
        },
        "hasNote": false
      }
    ],
    "first": 98765000001,
    "last": 98765000001
  }
}
```

**Activity fields:**

| Field | Type | Present | Meaning |
|---|---|---|---|
| `id` | number | Always | Cursor value. |
| `kind` | string | Always | `creation`, `sent`, `received` or `swapped`. `creation` is a transfer from the zero address. |
| `txHash` | string | Always | Transaction hash. |
| `dateBlock` | string | Always | Block time. |
| `success` | boolean | Always | `false` if the transaction reverted. |
| `counterpartyAddress`, `senderAddress`, `recipientAddress` | string | Not for `swapped` | The parties. |
| `transfer` | object | Not for `swapped` | The token leg. |
| `hasNote` | boolean | Not for `swapped` | Same meaning as in `/transfers`. |
| `poolAddresses` | array of string | `swapped` | The pools on the route, in order. |
| `sent` | object | `swapped` | The first input of the route. |
| `received` | object | `swapped` | The last output of the route. |
| `protocolFee` | object | `swapped`, if the fee is above 0 | The fee, in the output token. |

**Leg fields** (`transfer`, `sent`, `received`, `protocolFee`):

| Field | Type | Meaning |
|---|---|---|
| `tokenAddress` | string | Token contract address. |
| `tokenName`, `tokenSymbol` | string | From the catalog. |
| `tokenDecimals` | number | From the catalog. |
| `amount` | string | Raw units. |
| `uoaValue`, `uoa` | string | Same rules as in `/transfers`. Not present if there is no value. |

## 13. Pretium

These routes proxy the Pretium Ramp service for cash-in and cash-out flows.
The routes exist only when the service has the `pretium.base_url` setting.
Without it, the routes are not registered.
The base URL must include the upstream `/api/v1` prefix.

**Access** for all routes in this section: User or service token, with these limits:

- A user can act only on the user's own address. A request for another address returns `403` with code `E10`.
- The routes in sections 13.3, 13.5 and 13.7 are for a service token or a superadmin only. Any other caller gets `403` with code `E10`.
- A service token or a superadmin can act on any address.

**Proxy behavior:**

- The API checks the request, then calls the upstream service with a 10-second timeout.
- The API path-escapes `phoneNumber` and `address` before it calls upstream.
- The API returns the upstream status code for a success. Upstream must return a JSON object with the `ok`, `description` and `result` keys. The API returns only these three keys.
- The `result` value is an object. Any other shape is a decode failure and returns `500` with code `E01`.
- An upstream `4xx` response keeps its status code. The body is the standard error envelope.
- An upstream `5xx` response becomes `502` with code `E01`.
- A network failure or a decode failure returns `500` with code `E01`.
- The `description` of an error is the upstream description. If upstream sends no JSON description, it is the upstream body text.

**Upstream status to code:**

| Status | Code |
|---|---|
| 400, 422 | `E04` |
| 401 | `E06` |
| 403 | `E10` |
| 404 | `E03` |
| 405 | `E05` |
| 408 | `E09` |
| 409 | `E16` |
| 413 | `E08` |
| 502 (from upstream 5xx) | `E01` |
| Other | `E01` |

**Common errors** (in addition to the table above):

| Status | Code | When |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | The request fails local validation. |
| 401 | `E06` | No valid credential. |
| 403 | `E10` | The caller is not allowed to use this address or this route. |

### 13.1 `POST /api/v2/pretium/trigger-onramp`

**Access:** User or service token. `address` must be the caller's own address, unless the caller is a service token or a superadmin.

Starts a fiat-to-crypto onramp.

**Request body (JSON):**

| Field | Type | Required | Rules |
|---|---|---|---|
| `address` | string | Yes | Checksummed address. |
| `phoneNumber` | string | No | No check in this API. |
| `asset` | string | Yes | `USDT`, `USDC` or `cUSD`. |
| `amount` | number | Yes | From 20 to 250000. |

```json
{
  "address": "0x0000000000000000000000000000000000000001",
  "phoneNumber": "+254700000000",
  "asset": "USDT",
  "amount": 20
}
```

The response is the upstream response.

### 13.2 `POST /api/v2/pretium/link`

**Access:** User or service token. `publicKey` must be the caller's own address, unless the caller is a service token or a superadmin.

Links a public key to a phone number.

**Request body (JSON):**

| Field | Type | Required | Rules |
|---|---|---|---|
| `publicKey` | string | Yes | Checksummed address. |
| `phoneNumber` | string | Yes | Must not be empty. |

```json
{
  "publicKey": "0x0000000000000000000000000000000000000001",
  "phoneNumber": "+254700000000"
}
```

The response is the upstream response.
The link rules (for example, what happens when a key moves to a new phone number) belong to the upstream service.
This repository does not define them.

### 13.3 `GET /api/v2/pretium/link/:phoneNumber`

**Access:** Service token or superadmin.

Returns the links of a phone number.

| Path parameter | Type | Rules |
|---|---|---|
| `phoneNumber` | string | Required. |

The response is the upstream response.

### 13.4 `GET /api/v2/pretium/link-by-address/:address`

**Access:** User or service token. `address` must be the caller's own address, unless the caller is a service token or a superadmin.

Returns the link of a wallet address.

| Path parameter | Type | Rules |
|---|---|---|
| `address` | string | Required. Checksummed address. |

The response is the upstream response.

### 13.5 `GET /api/v2/pretium/transactions/:phoneNumber`

**Access:** Service token or superadmin.

Returns the transactions of a phone number.

| Path parameter | Type | Rules |
|---|---|---|
| `phoneNumber` | string | Required. |

The response is the upstream response.

### 13.6 `GET /api/v2/pretium/transactions-by-address/:address`

**Access:** User or service token. `address` must be the caller's own address, unless the caller is a service token or a superadmin.

Returns the transactions of an address.

| Path parameter | Type | Rules |
|---|---|---|
| `address` | string | Required. Checksummed address. |

The response is the upstream response.

### 13.7 `GET /api/v2/pretium/transactions-recent`

**Access:** Service token or superadmin.

Returns recent transactions. The route has no parameters.
The response is the upstream response.

### 13.8 `GET /api/v2/pretium/rates`

**Access:** User or service token.

Returns the current rates. The route has no parameters.
The response is the upstream response.

**Example of a success response (shape only):**

```json
{
  "ok": true,
  "description": "Rates",
  "result": {}
}
```

## 14. Current user

All routes in this section need a user session.
A service token returns `403` with code `E10`.
A missing or invalid credential returns `401` with code `E06`.
Both `USER` and `SUPERADMIN` callers can use these routes.
The user routes for posts and catalogs (`GET /api/v2/users/me/reports`, `GET /api/v2/users/me/catalogs`) are in other sections.

**Cursor pagination** applies to `/me/vouchers`, `/me/tokens` and `/me/pools`.

| Query | Type | Default | Rules |
|---|---|---|---|
| `perPage` | integer | 100 | A value of 0, or above 100, becomes 100. |
| `cursor` | integer | none | An `id` from a previous page. |
| `next` | boolean | `false` | Sets the direction. |

The route sorts every page by `id`, highest first.
The first request has no `cursor` and no `next`.
`first` is the `id` of the first item (the highest). `last` is the `id` of the last item (the lowest).
Both are 0 when the page is empty.
An empty page has `[]` as the list.
A wrong type in a query parameter returns `400` with code `E02`.
Send `cursor` whenever you send `next`.

The direction of `next` is different for each route:

| Route | Older rows | Newer rows |
|---|---|---|
| `/me/vouchers` | `next=true`, `cursor=last` | `cursor=first` (no `next`) |
| `/me/pools` | `next=true`, `cursor=last` | `cursor=first` (no `next`) |
| `/me/tokens` | `cursor=last` (no `next`) | `next=true`, `cursor=first` |

### 14.1 `GET /api/v2/users/me`

**Access:** User session.

Returns the profile of the caller.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Current user profile",
  "result": {
    "user": {
      "id": 7,
      "ethereumAddress": "0x0000000000000000000000000000000000000001",
      "username": "alice",
      "name": "Alice",
      "email": "alice@example.com",
      "phone": "",
      "country": "KE",
      "preferredCurrency": "KES",
      "bio": "Baker",
      "website": "https://example.com",
      "locationName": "Nairobi",
      "geoLat": -1.286389,
      "geoLng": 36.817223,
      "avatarUrl": "https://example.com/avatar.png",
      "role": "USER",
      "status": "APPROVED",
      "maxPools": 10,
      "maxTokens": 1,
      "maxCatalogs": 1,
      "maxOffers": 3,
      "createdAt": "2026-01-15T10:30:00Z",
      "updatedAt": "2026-01-15T10:30:00Z"
    }
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | number | User ID. |
| `ethereumAddress` | string | Address of the user. |
| `username` | string or `null` | Public user name. |
| `name`, `email`, `phone`, `country` | string | Contact fields. Empty string if not set. |
| `preferredCurrency`, `bio`, `website`, `locationName`, `geoLat`, `geoLng`, `avatarUrl` | mixed | Profile fields. Not present if not set. |
| `role` | string | `USER` or `SUPERADMIN`. |
| `status` | string | `PENDING`, `APPROVED` or `REJECTED`. |
| `maxPools`, `maxTokens`, `maxCatalogs` | number | Limits for pools, vouchers and catalogs. |
| `maxOffers` | number | Maximum available offerings on each voucher the user owns. |
| `createdAt`, `updatedAt` | string | RFC 3339 times. |

**Errors:** `404` with code `E07` if the user record does not exist.

### 14.2 `GET /api/v2/users/me/resource-usage`

**Access:** User session.

Returns how much of each limit the caller used.
The response has the header `Cache-Control: no-store`.

Optional query parameter:

| Parameter | Type | Meaning |
|---|---|---|
| `voucher` | address | Return `offers` usage for this voucher. The caller must own the voucher, unless the caller is a superadmin. Other resource counts remain the caller's normal aggregate counts. Without this parameter, `offers` retains the legacy account-wide aggregate for older clients. |

**Response `200`:**

```json
{
  "ok": true,
  "description": "",
  "result": {
    "usage": {
      "vouchers": { "used": 1, "limit": 1, "unlimited": false, "canCreate": false },
      "pools": { "used": 2, "limit": 10, "unlimited": false, "canCreate": true },
      "catalogs": { "used": 0, "limit": 1, "unlimited": false, "canCreate": true },
      "offers": { "used": 2, "limit": 3, "unlimited": false, "canCreate": true },
      "wants": { "used": 1, "limit": 3, "unlimited": false, "canCreate": true }
    }
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `used` | number | Current use. |
| `limit` | number | The limit of the user. |
| `unlimited` | boolean | `true` for a `SUPERADMIN` user. |
| `canCreate` | boolean | `true` if the user is unlimited or `used` is below `limit`. |

A voucher deploy counts until the deploy fails on chain or the voucher is retired.
When `voucher` is supplied, an offering counts while it is available on that voucher. Each voucher receives the user's full `maxOffers` allowance independently. Unavailable offerings release their slot. Existing offerings remain visible and editable after an ownership transfer even when their count exceeds the new owner's allowance; only additional creation is blocked.

**Errors:** `400` with code `E04` for a malformed voucher address, `403` with code `E10` when a non-superadmin does not own the voucher, and `404` with code `E07` when the voucher is unknown.

### 14.3 `GET /api/v2/users/me/vouchers`

**Access:** User session.

Returns the vouchers that the caller owns now.
The catalog owner address must equal the address of the caller.
The list includes retired vouchers and vouchers with no location.
The route uses cursor pagination (see the start of this section).
The response has the header `Cache-Control: no-store`.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Currently owned vouchers for user 7",
  "result": {
    "vouchers": [
      {
        "id": 12,
        "chainId": 42220,
        "address": "0x1111111111111111111111111111111111111111",
        "name": "Example Voucher",
        "symbol": "VCH",
        "decimals": 6,
        "iconUrl": null,
        "ownerAddress": "0x0000000000000000000000000000000000000001",
        "active": true
      }
    ],
    "first": 12,
    "last": 12
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | number | Catalog ID. Use it as the cursor. |
| `chainId` | number | Chain ID. |
| `address` | string | Voucher address. |
| `name` | string | Display name, else contract name, else address. |
| `symbol` | string | Symbol, or a short form of the address if there is none. |
| `decimals` | number or `null` | Token decimals. |
| `iconUrl` | string or `null` | Icon URL. |
| `ownerAddress` | string | Catalog owner address. |
| `active` | boolean | `false` if the voucher is retired. |

### 14.4 `GET /api/v2/users/me/tokens`

**Access:** User session.

Returns the confirmed vouchers that the caller deployed with this API.
Only deploys recorded for the caller appear.
The route uses cursor pagination (see the start of this section).
The route sorts rows by deployment ID, highest first.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Confirmed tokens for user 7",
  "result": {
    "tokens": [
      {
        "id": 42,
        "otxId": 100,
        "platformContractId": 1,
        "status": "CONFIRMED",
        "address": "0x1111111111111111111111111111111111111111",
        "symbol": "VCH",
        "name": "Example Voucher",
        "decimals": 6,
        "admin": "0x0000000000000000000000000000000000000001",
        "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "createdAt": "2026-01-15T10:30:00Z",
        "updatedAt": "2026-01-15T10:35:00Z"
      }
    ],
    "first": 42,
    "last": 42
  }
}
```

The list rows have only the fields in the example.
The other fields of the voucher record are `null`, and `tags` is `null`.
Use `GET /api/v2/users/me/tokens/:address` for the full record.

### 14.5 `GET /api/v2/users/me/tokens/:address`

**Access:** User session. The caller must have started the deployment, or be a superadmin.

Returns one confirmed deployed voucher.

| Path parameter | Type | Rules |
|---|---|---|
| `address` | string | Voucher address. The match is exact. No format check. |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Token at address 0x1111111111111111111111111111111111111111",
  "result": {
    "token": {
      "id": 42,
      "otxId": 100,
      "platformContractId": 1,
      "status": "CONFIRMED",
      "address": "0x1111111111111111111111111111111111111111",
      "symbol": "VCH",
      "name": "Example Voucher",
      "decimals": 6,
      "admin": "0x0000000000000000000000000000000000000001",
      "description": "A voucher for example goods",
      "website": null,
      "email": null,
      "phone": null,
      "terms": null,
      "voucherUoa": "Hours",
      "voucherValue": 1.5,
      "locationName": "Nairobi",
      "geoLat": -1.286389,
      "geoLng": 36.817223,
      "iconUrl": null,
      "bannerUrl": null,
      "markerIcon": "basket",
      "markerColor": "blue",
      "tags": ["Food"],
      "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
      "createdAt": "2026-01-15T10:30:00Z",
      "updatedAt": "2026-01-15T10:35:00Z"
    }
  }
}
```

The `name` is the catalog display name if one is set.
**Errors:** `404` with code `E07` if no confirmed deployed voucher has this address, or if another user started it.

### 14.6 `GET /api/v2/users/me/pools`

**Access:** User session.

Returns the confirmed pools that the caller deployed with this API.
The route uses cursor pagination (see the start of this section).
The route sorts rows by deployment ID, highest first.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Confirmed pools for user 7",
  "result": {
    "pools": [
      {
        "id": 51,
        "otxId": 120,
        "platformContractId": 2,
        "status": "CONFIRMED",
        "address": "0x2222222222222222222222222222222222222222",
        "name": "Example Pool",
        "symbol": "EXP",
        "decimals": 6,
        "tokenRegistryAddress": "0x3333333333333333333333333333333333333333",
        "limiterAddress": "0x4444444444444444444444444444444444444444",
        "feePolicyAddress": "0x5555555555555555555555555555555555555555",
        "quoterAddress": "0x6666666666666666666666666666666666666666",
        "quoterType": "relative",
        "protocolFeeControllerAddress": "0x7777777777777777777777777777777777777777",
        "feesDecoupled": false,
        "feePpm": null,
        "feeAddress": null,
        "admin": "0x0000000000000000000000000000000000000001",
        "description": null,
        "unitOfAccount": null,
        "website": null,
        "email": null,
        "phone": null,
        "terms": null,
        "locationName": null,
        "geoLat": null,
        "geoLng": null,
        "imageUrl": null,
        "bannerUrl": null,
        "tags": [],
        "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "createdAt": "2026-01-15T10:30:00Z",
        "updatedAt": "2026-01-15T10:35:00Z"
      }
    ],
    "first": 51,
    "last": 51
  }
}
```

In the list, `feePpm`, `feeAddress`, `unitOfAccount` and `terms` are always `null`.
Use `GET /api/v2/users/me/pools/:address` for the full record.

### 14.7 `GET /api/v2/users/me/pools/:address`

**Access:** User session. The caller must have started the deployment, or be a superadmin.

Returns one confirmed deployed pool.

| Path parameter | Type | Rules |
|---|---|---|
| `address` | string | Pool address. The comparison ignores case. No format check. |

**Response `200`:** `result.pool` has the same fields as one item of the list in section 14.6.
The description is `Pool at address <address>`.
In this route `feePpm`, `feeAddress`, `unitOfAccount` and `terms` have their stored values.
`feePpm` is the fee in parts per million.

**Errors:** `404` with code `E07` if no confirmed deployed pool has this address, or if another user started it.

### 14.8 `GET /api/v2/users/me/deployments`

**Access:** User session.

Returns all deploy requests of the caller, newest first.
The route has no pagination.
Use `trackingId` with `GET /api/v2/otx/track/:trackingId`.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Deployment history for user 7",
  "result": {
    "deployments": [
      {
        "id": 3,
        "userId": 7,
        "trackingId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
        "resourceType": "TOKEN",
        "createdAt": "2026-01-15T10:30:00Z"
      }
    ]
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | number | Record ID. |
| `userId` | number | User ID. |
| `trackingId` | string | Tracking UUID. |
| `resourceType` | string | `TOKEN` or `POOL`. |
| `createdAt` | string | RFC 3339 time of the request. |

The list can be `null` when the user has no deploy requests.

### 14.9 `GET /api/v2/users/me/shares/eligibility/:address`

**Access:** User session.

Checks if an address can receive a private send attachment.
The route returns only a boolean. It never returns profile data.

| Path parameter | Type | Rules |
|---|---|---|
| `address` | string | Required. A hex address. The route does not check the checksum. |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Private share eligibility",
  "result": {
    "eligible": true
  }
}
```

`eligible` is `true` when the address belongs to a user with status `APPROVED` who is not the caller.

**Errors:** `400` with code `E04` and the description `invalid recipient address` if `address` is not a hex address.

### 14.10 `POST /api/v2/users/me/shares/:txHash`

**Access:** User session.

Creates a private attachment for a confirmed token send.
Only the sender and the recipient of the send can read it later.
The note and the profile data stay off chain.

| Path parameter | Type | Rules |
|---|---|---|
| `txHash` | string | Required. 66 characters. Starts with `0x`. Hexadecimal. |

**Request body (JSON):**

| Field | Type | Required | Rules |
|---|---|---|---|
| `permission` | integer | No | Bit mask from 0 to 7. Default 0. `1` is email, `2` is phone, `4` is location. |
| `note` | string | No | Trimmed. Maximum 500 characters. |

The body must have a non-empty `note`, or `permission` above 0.

```json
{
  "permission": 5,
  "note": "Thanks for the supplies"
}
```

**Checks.**
The API reads the transaction and the receipt from the chain.
The receipt must show success.
The caller must own the transaction. The caller address must be the signer, or the target of the transaction (for a send that the relayer submitted).
The receipt must have exactly one ERC-20 `Transfer` event from the caller address with an amount above 0.
The recipient of that event must be a user with status `APPROVED` who is not the caller.
For each permission bit, the profile of the caller must have the data. The data is an email that does not end in `@clc.local`, a phone number, or a location with a name and coordinates.

**Response `200`:**

```json
{
  "ok": true,
  "description": "Private send attachment saved",
  "result": {
    "shareId": 42,
    "txHash": "0x1111111111111111111111111111111111111111111111111111111111111111"
  }
}
```

The request is idempotent.
If the same attachment exists (same recipient, `permission` and `note`), the response is `200` with the same `shareId`.

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E02` | The body is not valid JSON. |
| 400 | `E04` | `txHash` or `permission` is not valid, `note` is too long, an unknown permission bit is set, or both `note` and `permission` are empty. |
| 401 | `E06` | No valid credential. |
| 403 | `E10` | The caller is a service token, or the user record does not match the session address. |
| 409 | `E16` | A different attachment already exists for this transaction. |
| 422 | `E19` | The transaction is not a confirmed token send from the caller, the recipient is not available (not registered, not approved, or the caller), or the profile lacks the data for a permission bit. |
| 500 | `E01` | The chain node call fails. |

### 14.11 `GET /api/v2/users/me/shares/:txHash`

**Access:** User session. Only the sender (owner) and the recipient of the attachment can read it.

Returns the private attachment of a send.

| Path parameter | Type | Rules |
|---|---|---|
| `txHash` | string | Required. 66 characters. Starts with `0x`. Hexadecimal. The comparison ignores case. |

**Response `200`:**

```json
{
  "ok": true,
  "description": "Private send attachment",
  "result": {
    "id": 42,
    "txHash": "0x1111111111111111111111111111111111111111111111111111111111111111",
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

| Field | Type | Meaning |
|---|---|---|
| `id` | number | Attachment ID. |
| `txHash` | string | Transaction hash. |
| `permission` | number | The bit mask. |
| `note` | string | The note. Empty string if none. |
| `sharedBy.name` | string | Name of the sender. Always present. |
| `sharedBy.email` | string | Present if bit `1` is set and the sender has an email. |
| `sharedBy.phone` | string | Present if bit `2` is set and the sender has a phone number. |
| `sharedBy.location` | object | Present if bit `4` is set and the sender has a location. It has `locationName`, `lat` and `lng`. |
| `createdAt` | string | RFC 3339 time. |

The API reads the profile data from the current profile of the sender.

**Errors:**

| Status | Code | When |
|---|---|---|
| 400 | `E04` | `txHash` is not valid. |
| 401 | `E06` | No valid credential. |
| 403 | `E10` | The caller is a service token. |
| 404 | `E03` | No attachment exists, or the caller is not the sender or the recipient. |

---

## 15. Admin

This section describes all routes under `/api/v2/admin`. Only a superadmin can use them.

### 15.1 Common rules

**Access:** Superadmin.

#### Access control

- The caller must have a user session with the role `SUPERADMIN`. See section 2.
- A service token has the role `SERVICE`. A service token gets `403` with `E10` and the description `superadmin access required`.
- A caller with the role `USER` also gets `403` with `E10`.
- A request without valid credentials gets `401` with `E06`.
- This section does not repeat these three errors for each route.

#### Address path parameters

- A path parameter named `:address` must be a valid Ethereum address. The case of the letters does not matter.
- The server converts the address to the checksum form before it uses the address.
- On every route in this section except `GET /api/v2/admin/pools/:address/vouchers`, an invalid address returns `400` with `E04` and the description `invalid Ethereum address`.
- On `GET /api/v2/admin/pools/:address/vouchers`, an invalid address returns `400` with `E04` and the description `address must be a valid hex address`.

#### Cursor pagination

These routes use the same cursor pagination: `GET /api/v2/admin/users`, `GET /api/v2/admin/users/:address/pools`, `GET /api/v2/admin/pools/:address/vouchers`, `GET /api/v2/admin/vouchers`, and the three routes under `/api/v2/admin/transactions`.

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | integer | `100` | Number of rows in one page. The maximum is `100`. A value of `0` or a value above `100` becomes `100`. |
| `cursor` | integer | none | A row ID from an earlier response. Use the value of `first` or `last`. |
| `next` | boolean | `false` | Direction from `cursor`. |

- Rows are always in descending order, newest first.
- A request with no `cursor` and `next=false` returns the first page.
- `next=true` with `cursor` returns the rows that come after the cursor in this order. These rows are older. Send the `last` value of the previous page as `cursor`.
- `next=false` with a `cursor` greater than `0` returns the rows that come before the cursor. These rows are newer. Send the `first` value of the page as `cursor`. The rows in the page are still newest first.
- The result contains a list under a route-specific key, and the two fields `first` and `last`.
- `first` is the ID of the first row in the page. `last` is the ID of the last row in the page. Both are `0` when the page is empty.
- For the transaction routes, the ID is a packed number. Use it only as a cursor.
- A request with an invalid `next` or a non-numeric `cursor` or `perPage` returns `400` with `E02`.
- A list read has a time limit of 5 seconds. The two chain transaction routes (see section 15.28) have no such limit.

#### Search term `q`

- The server trims the term and replaces runs of white space with one space.
- An empty term means no search.
- The term must have 2 to 64 characters (counted as Unicode characters).
  A shorter term returns `400` with `E04` and the description `q must be at least 2 characters`.
  A longer term returns `400` with `E04` and the description `q must be at most 64 characters`.
- The match is not case-sensitive. A term of 2 characters matches the start of a value. A term of 3 or more characters matches any part of a value.
- The characters `%` and `_` match themselves.

#### Cache headers

- These responses send `Cache-Control: no-store`: `GET /api/v2/admin/pools/:address/vouchers`, `GET /api/v2/admin/vouchers/:address`, `PUT /api/v2/admin/vouchers/:address/restore`, `PUT /api/v2/admin/pools/:address/profile`, `GET /api/v2/admin/reports`, `GET /api/v2/admin/reports/:id`, `GET /api/v2/admin/review-notifications`, and `PUT /api/v2/admin/review-notifications/:id`.
- The other routes in this section send no cache header.

#### Routes that are not paginated

`GET /api/v2/admin/tokens`, `GET /api/v2/admin/pools` and `GET /api/v2/admin/deployments` return the full list in one response. They have no query parameters.

### 15.2 `GET /api/v2/admin/users`

**Access:** Superadmin.

Return one page of the account directory. Rows are newest first.

**Query parameters**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `q` | string | No | Search term. The server searches the name, email, phone and address of each account. It does not search country or role. See section 15.1. |
| `status` | string | No | Filter by approval status. Values are `PENDING`, `APPROVED` and `REJECTED`. The case does not matter. |
| `perPage`, `cursor`, `next` | | No | See section 15.1. |

**Response 200**

The description is `Users list`. The result has the keys `users`, `first` and `last`. The value of `first` and `last` is the `id` of an account.

```json
{
  "ok": true,
  "description": "Users list",
  "result": {
    "users": [
      {
        "id": 4210,
        "ethereumAddress": "0x0000000000000000000000000000000000000001",
        "username": "ada",
        "name": "Ada Lovelace",
        "email": "ada@example.com",
        "phone": "+254700000000",
        "country": "KE",
        "role": "USER",
        "status": "PENDING",
        "maxPools": 5,
        "maxTokens": 5,
        "maxCatalogs": 5,
        "maxOffers": 5,
        "createdAt": "2026-08-12T10:00:00Z",
        "updatedAt": "2026-08-12T10:00:00Z"
      }
    ],
    "first": 4210,
    "last": 4210
  }
}
```

**User object**

| Field | Type | Description |
|---|---|---|
| `id` | integer | Account ID. |
| `ethereumAddress` | string | Account address. |
| `username` | string or null | Public username. |
| `name` | string | Name. |
| `email` | string | Email. The value is an empty string when the account has none. |
| `phone` | string | Phone number. The value is an empty string when the account has none. |
| `country` | string | Country code. The value is an empty string when the account has none. |
| `preferredCurrency` | string | Unit of account. The field is absent when the value is null. |
| `bio` | string | Biography. The field is absent when the value is null. |
| `website` | string | Website. The field is absent when the value is null. |
| `locationName` | string | Location label. The field is absent when the value is null. |
| `geoLat` | number | Latitude. The field is absent when the value is null. |
| `geoLng` | number | Longitude. The field is absent when the value is null. |
| `avatarUrl` | string | Avatar URL. The field is absent when the value is null. |
| `role` | string | Role of the user. |
| `status` | string | `PENDING`, `APPROVED` or `REJECTED`. |
| `maxPools` | integer | Pool limit. |
| `maxTokens` | integer | Voucher limit. |
| `maxCatalogs` | integer | Catalog limit. |
| `maxOffers` | integer | Offering limit per voucher. |
| `createdAt` | string | RFC 3339 time. |
| `updatedAt` | string | RFC 3339 time. |

The routes in sections 15.3, 15.4 and 15.5 return the same user object under `result.user`.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `q` is shorter than 2 or longer than 64 characters. |
| `400` | `E04` | `status` is not one of the three values. The description is `status must be "PENDING", "APPROVED" or "REJECTED"`. |

### 15.3 `PUT /api/v2/admin/users/:address/approve` and `PUT /api/v2/admin/users/:address/reject`

**Access:** Superadmin.

Set the status of an account. `approve` sets `APPROVED`. `reject` sets `REJECTED`. The route does not check the old status or the role of the account.

**Path parameters**

| Parameter | Type | Description |
|---|---|---|
| `address` | string | Account address. |

The request has no body.

**Response 200**

The description is `User status updated to APPROVED` or `User status updated to REJECTED`. The result has the key `user` (see the user object in section 15.2).

```json
{
  "ok": true,
  "description": "User status updated to APPROVED",
  "result": {
    "user": { "id": 4210, "ethereumAddress": "0x0000000000000000000000000000000000000001", "status": "APPROVED" }
  }
}
```

The example shows only some fields of `user`.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `address` is not a valid address. |
| `404` | `E07` | No user has this address. |

### 15.4 `PUT /api/v2/admin/users/:address/limits`

**Access:** Superadmin.

Change the creation limits of an account. The server changes only the fields that you send.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `maxPools` | integer | No | Pool limit. The value must be from `0` to `2147483647`. |
| `maxTokens` | integer | No | Voucher limit. The value must be from `0` to `2147483647`. |
| `maxCatalogs` | integer | No | Catalog limit. The value must be from `0` to `2147483647`. |
| `maxOffers` | integer | No | Offering limit per voucher. The value must be from `0` to `2147483647`. |

A field that is absent or `null` keeps its old value. A body with no fields changes nothing and still returns `200`.

```json
{ "maxPools": 20, "maxTokens": 50 }
```

**Response 200**

The description is `User limits updated`. The result has the key `user`.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A value is negative or above `2147483647`, or `address` is not valid. |
| `404` | `E07` | No user has this address. |

### 15.5 `PUT /api/v2/admin/users/:address/profile`

**Access:** Superadmin.

Correct the identity, contact details and preferences of another account.
The route uses the same body, the same validation and the same store call as the self-service profile update (see section 14, Current user).
The route does not change the status, the role or the limits.

**Request body**

| Field | Type | Description |
|---|---|---|
| `username` | string | Optional. The server trims the value. It must have 1 to 80 characters. If you omit it, the account has no username, and `name` is not empty, the server assigns a username. |
| `name` | string | Maximum 80 characters. |
| `email` | string | Must be a valid email address. Must be unique across accounts. |
| `phone` | string | Must be unique across accounts. |
| `country` | string | Exactly 2 characters when present. |
| `preferredCurrency` | string | Optional. Maximum 32 characters. See below. |
| `bio` | string | Optional. Maximum 500 characters. Send `""` to clear. |
| `website` | string | Optional. Must be an `http` or `https` URL. Send `""` to clear. |
| `locationName` | string | Optional. Maximum 200 characters. It must not be blank. |
| `geoLat` | number | Optional. From `-90` to `90`. Send it with `geoLng`. |
| `geoLng` | number | Optional. From `-180` to `180`. Send it with `geoLat`. |
| `clearLocation` | boolean | Optional. If `true`, the server clears the location label and the coordinates. |
| `avatarUrl` | string | Optional. Must be an `http` or `https` URL. |
| `clearAvatar` | boolean | Optional. If `true`, the server clears the avatar. |

- The fields `name`, `email`, `phone` and `country` are always written. If you omit one, the server writes an empty value. An empty `email` or `phone` becomes null.
- Send the current values of these four fields when you want to keep them.
- The fields `username`, `preferredCurrency`, `bio`, `website`, `locationName`, `geoLat`, `geoLng` and `avatarUrl` keep the stored value when you omit them.
- `preferredCurrency` is a unit of account. The server removes extra spaces. It finds an existing value without regard to case and stores the existing spelling. If no value matches, the server creates a custom unit of account. A blank value is an error.
- You cannot send `clearLocation` with `locationName`, `geoLat` or `geoLng`.
- You cannot send `clearAvatar` with `avatarUrl`.

```json
{
  "name": "Ada Lovelace",
  "email": "ada@example.com",
  "phone": "+254700000000",
  "country": "KE",
  "preferredCurrency": "KES",
  "bio": "Community organiser",
  "website": "https://example.com",
  "locationName": "Nairobi",
  "geoLat": -1.2921,
  "geoLng": 36.8219
}
```

**Response 200**

The description is `User profile updated`. The result has the key `user` with the full row.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field fails validation. The description is `Validation failed on one or more fields`. |
| `400` | `E04` | Only one of `geoLat` and `geoLng` is present. The description is `Validation failed: geoLat and geoLng must be provided together`. |
| `400` | `E04` | A rule between fields fails (`clearLocation`, `clearAvatar`, blank `locationName`, invalid `username`, blank or too long `preferredCurrency`). The description names the rule. |
| `404` | `E03` | No user has this address. The description is `user not found`. |
| `409` | `E16` | The username is in use. |
| `409` | `E17` | The email belongs to another account. |
| `409` | `E18` | The phone number belongs to another account. |

### 15.6 `GET /api/v2/admin/users/:address/pools`

**Access:** Superadmin.

Return the pools to which an account belongs, with cursor pagination. The server derives the membership. An account belongs to each pool that allows a voucher that the account owns. Pool ownership and deployment history do not count.

**Query parameters:** `perPage`, `cursor`, `next` (see section 15.1).

**Response 200**

The description is `Account pool memberships`. The result has the keys `pools`, `first` and `last`. Each pool has the fields of the catalog pool object in section 15.11.

The `first` and `last` values are pool IDs.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `address` is not valid. |
| `400` | `E02` | A pagination parameter has an invalid type. |

### 15.7 `GET /api/v2/admin/users/:address/stats`

**Access:** Superadmin.

Return the counts for one account.

**Response 200**

The description is `Account statistics`.

```json
{
  "ok": true,
  "description": "Account statistics",
  "result": {
    "reports": 3,
    "vouchers": 2,
    "poolMemberships": 1,
    "transactions": 118,
    "swaps": 4,
    "partners": 9
  }
}
```

| Field | Source | Description |
|---|---|---|
| `reports` | catalog | Approved posts that the account wrote. |
| `vouchers` | catalog | Vouchers that the account owns, that are active and have a location. |
| `poolMemberships` | catalog | Number of different pools that allow a voucher that the account owns. |
| `transactions` | federation | Number of successful transactions of the account. |
| `swaps` | federation | Number of successful swaps. |
| `partners` | federation | Number of different counterparties. |

- The three federation fields are `null` when federation is not configured.
  They are also `null` when the federation read fails. The route still returns `200`.
- `null` means that the server has no answer. `0` means that the count is zero.
- The route returns zero counts for an address that has no data. It does not return `404`.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `address` is not valid. |

### 15.8 `GET /api/v2/admin/tokens`

**Access:** Superadmin.

Return all confirmed voucher deployments of all users. The list is not paginated. Rows are newest first by `createdAt`. The name is the display name of the voucher when one exists.

**Response 200**

The description is `All deployed tokens`. The result has the key `tokens`.

```json
{
  "ok": true,
  "description": "All deployed tokens",
  "result": {
    "tokens": [
      {
        "id": 12,
        "otxId": 340,
        "platformContractId": 1,
        "status": "CONFIRMED",
        "address": "0x1111111111111111111111111111111111111111",
        "symbol": "MTK",
        "name": "My Token",
        "decimals": 6,
        "admin": "0x0000000000000000000000000000000000000001",
        "description": null,
        "website": null,
        "email": null,
        "phone": null,
        "terms": null,
        "voucherUoa": null,
        "voucherValue": null,
        "locationName": null,
        "geoLat": null,
        "geoLng": null,
        "iconUrl": null,
        "bannerUrl": null,
        "markerIcon": null,
        "markerColor": null,
        "tags": null,
        "trackingId": "",
        "createdAt": "2026-08-12T10:00:00Z",
        "updatedAt": "2026-08-12T10:00:00Z"
      }
    ]
  }
}
```

The query for this route fills only these fields: `id`, `otxId`, `platformContractId`, `status`, `address`, `symbol`, `name`, `decimals`, `admin`, `createdAt` and `updatedAt`. The other fields have their zero value: `null` for optional fields and an empty string for `trackingId`.

### 15.9 `GET /api/v2/admin/pools`

**Access:** Superadmin.

Return all confirmed pool deployments of all users. The list is not paginated. Rows are newest first by `createdAt`.

**Response 200**

The description is `All deployed pools`. The result has the key `pools`.

```json
{
  "ok": true,
  "description": "All deployed pools",
  "result": {
    "pools": [
      {
        "id": 7,
        "otxId": 351,
        "platformContractId": 2,
        "status": "CONFIRMED",
        "address": "0x2222222222222222222222222222222222222222",
        "name": "My Pool",
        "symbol": "MYP",
        "decimals": 6,
        "tokenRegistryAddress": "0x0000000000000000000000000000000000000003",
        "limiterAddress": "0x0000000000000000000000000000000000000004",
        "feePolicyAddress": "0x0000000000000000000000000000000000000005",
        "quoterAddress": "0x0000000000000000000000000000000000000006",
        "quoterType": "decimal",
        "protocolFeeControllerAddress": "0x0000000000000000000000000000000000000007",
        "feesDecoupled": false,
        "feePpm": null,
        "feeAddress": null,
        "admin": "0x0000000000000000000000000000000000000001",
        "description": null,
        "unitOfAccount": null,
        "website": null,
        "email": null,
        "phone": null,
        "terms": null,
        "locationName": null,
        "geoLat": null,
        "geoLng": null,
        "imageUrl": null,
        "bannerUrl": null,
        "tags": null,
        "trackingId": "",
        "createdAt": "2026-08-12T10:00:00Z",
        "updatedAt": "2026-08-12T10:00:00Z"
      }
    ]
  }
}
```

The route fills these fields: `id`, `otxId`, `platformContractId`, `status`, `address`, `name`, `symbol`, `decimals`, `tokenRegistryAddress`, `limiterAddress`, `feePolicyAddress`, `quoterAddress`, `quoterType`, `protocolFeeControllerAddress`, `feesDecoupled`, `admin`, `imageUrl`, `bannerUrl`, `createdAt` and `updatedAt`. The other fields have their zero value.

This pool object is the deployment record. The catalog pool object in sections 15.6 and 15.11 is different. See section 15.11.

### 15.10 `GET /api/v2/admin/pools/:address/vouchers`

**Access:** Superadmin.

Return the vouchers of one pool, with cursor pagination. Unlike the public route `GET /api/v2/pools/:address/vouchers` (see section 8, Pools), this route includes retired vouchers.

**Query parameters:** `perPage`, `cursor`, `next` (see section 15.1).

**Response 200**

The description is `Vouchers in pool <address>`. The `<address>` value is the address from the path as you sent it. The route sends `Cache-Control: no-store`.

| Key | Type | Description |
|---|---|---|
| `vouchers` | array | Page of pool vouchers. |
| `first` | integer | `id` of the first voucher. |
| `last` | integer | `id` of the last voucher. |
| `total` | integer | Number of vouchers of the pool, including retired vouchers. It does not depend on the page. |

Fields of a pool voucher:

| Field | Type | Description |
|---|---|---|
| `id` | integer | Voucher ID. |
| `chainId` | integer | Chain ID. |
| `address` | string | Voucher address. |
| `name` | string or null | Name. |
| `symbol` | string or null | Symbol. |
| `decimals` | integer or null | Decimals. |
| `ownerAddress` | string or null | Owner. |
| `voucherType` | string or null | Voucher type. |
| `sinkAddress` | string or null | Sink address. |
| `redemptionAddress` | string or null | Redemption address. |
| `description` | string or null | Description. |
| `voucherUoa` | string or null | Unit of account. |
| `voucherValue` | string or null | Value in the unit of account, as a decimal string. |
| `locationName` | string or null | Location label. |
| `geoLat`, `geoLng` | number or null | Coordinates. |
| `email`, `website`, `phone` | string or null | Contact details. |
| `iconUrl`, `bannerUrl` | string or null | Image URLs. |
| `terms` | string or null | Terms. |
| `contractVersion` | string or null | Contract version. |
| `active` | boolean | `false` for a retired voucher. |
| `internal` | boolean | Internal flag of the voucher. |
| `isAllowed` | boolean | `true` when the pool allows the voucher. |
| `tokenLimit` | string or null | Pool limit for the voucher. |
| `syncedAt` | string | RFC 3339 time of the last sync. |
| `oracleFeedId` | integer or null | Oracle feed ID. |
| `oracleName` | string or null | Oracle name. |
| `oracleAddress` | string or null | Oracle address. |
| `offerCount` | integer | Number of offerings. |
| `availableOfferCount` | integer | Number of available offerings. |
| `tags` | array of strings | Tags. |
| `createdAt`, `updatedAt` | string | RFC 3339 times. |

```json
{
  "ok": true,
  "description": "Vouchers in pool 0x2222222222222222222222222222222222222222",
  "result": {
    "vouchers": [
      {
        "id": 88,
        "chainId": 1,
        "address": "0x1111111111111111111111111111111111111111",
        "name": "Community Credit",
        "symbol": "CCR",
        "active": false,
        "isAllowed": true,
        "tags": []
      }
    ],
    "first": 88,
    "last": 88,
    "total": 1
  }
}
```

The example shows only some fields of a voucher.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `address` is not a valid address. |

### 15.11 `PUT /api/v2/admin/pools/:address/profile`

**Access:** Superadmin.

Replace the off-chain record of a pool. The route changes catalog data only. It does not change the name, symbol, decimals or contract addresses, which are facts from the chain. The owner can use `PUT /api/v2/pools/:address/profile` with the same body. See section 8, Pools.

**Request body**

| Field | Type | Description |
|---|---|---|
| `displayName` | string | Maximum 80 characters. The server trims the value. Omit it to keep the old value. Send `""` to remove the override. |
| `description` | string | Maximum 500 characters. |
| `unitOfAccount` | string | Maximum 32 characters. See below. |
| `email` | string | Must be a valid email address. |
| `website` | string | Must be a valid URL. |
| `phone` | string | No validation. |
| `terms` | string | Maximum 5000 characters. |
| `locationName` | string | Maximum 200 characters. |
| `geoLat` | number | From `-90` to `90`. Send it with `geoLng`. |
| `geoLng` | number | From `-180` to `180`. Send it with `geoLat`. |
| `imageUrl` | string | An `http` or `https` URL. Omit it to keep the old value. Send `""` to clear. |
| `bannerUrl` | string | An `http` or `https` URL. Omit it to keep the old value. Send `""` to clear. |
| `tags` | array of strings | The new full set of tags. |

- The route assigns the whole record.
  The fields `description`, `unitOfAccount`, `email`, `website`, `phone`, `terms`, `locationName`, `geoLat`, `geoLng` and `tags` are replaced by the values that you send.
  If you omit one of these fields, the server clears it.
  If you omit `tags`, the server removes all tags.
- Only `displayName`, `imageUrl` and `bannerUrl` keep the old value when you omit them.
- Each tag is trimmed and has its spaces collapsed.
  A tag must not be blank, must not have more than 100 characters, and must not contain a comma.
  Two tags must not be equal without regard to case.
  The server uses the stored spelling of a tag that exists.
  A new tag becomes a custom tag with each word capitalized.
  The server stores the tags in sorted order.
- `unitOfAccount` works like `preferredCurrency` in section 15.5. An oracle pool is a special case. For an oracle pool, you can omit the field or send the current value. Any other value is an error.
- The server saves the profile and the tags together. If a tag is not valid, the server saves nothing.

```json
{
  "displayName": "My Pool",
  "description": "Pool for the community",
  "unitOfAccount": "KES",
  "locationName": "Nairobi",
  "geoLat": -1.2921,
  "geoLng": 36.8219,
  "tags": ["Food", "Farming"]
}
```

**Response 200**

The description is `Pool profile updated`. The result has the key `pool`. The route sends `Cache-Control: no-store`.

The `pool` value is a catalog pool object:

| Field | Type | Description |
|---|---|---|
| `id` | integer | Pool ID. |
| `chainId` | integer | Chain ID. |
| `address` | string | Pool address. |
| `name` | string or null | Display name, or the contract name when there is no override. |
| `contractName` | string or null | Name from the contract. |
| `symbol` | string or null | Symbol. |
| `ownerAddress` | string or null | Owner. |
| `feePpm` | number or null | Fee in parts per million. |
| `feeAddress` | string or null | Fee recipient. |
| `tokenRegistryAddress` | string or null | Token registry address. |
| `limiterAddress` | string or null | Limiter address. |
| `quoterAddress` | string or null | Quoter address. |
| `quoterType` | string or null | `decimal`, `oracle` or `relative`. |
| `feePolicyAddress` | string or null | Fee policy address. |
| `protocolFeeControllerAddress` | string or null | Protocol fee controller address. |
| `feesDecoupled` | boolean or null | Fee mode. |
| `decimals` | integer or null | Decimals. |
| `description` | string or null | Description. |
| `unitOfAccount` | string or null | Unit of account. |
| `locationName` | string or null | Location label. |
| `geoLat`, `geoLng` | number or null | Coordinates. |
| `email`, `website`, `phone` | string or null | Contact details. |
| `imageUrl`, `bannerUrl` | string or null | Image URLs. |
| `terms` | string or null | Terms. |
| `defaultVoucherId` | integer or null | Default voucher. |
| `platformContractId` | integer or null | Platform contract ID. |
| `voucherCount` | integer | Number of vouchers. |
| `tags` | array of strings | Tags. |
| `createdAt`, `updatedAt` | string | RFC 3339 times. |

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field fails validation. |
| `400` | `E04` | Only one of `geoLat` and `geoLng` is present. |
| `400` | `E04` | A tag or the unit of account is not valid. The description names the problem. |
| `404` | `E03` | The pool does not exist. The description is `pool not found`. |
| `409` | `E16` | Another pool has the same `displayName`. The description is `a pool with this name already exists`. |
| `409` | `E16` | The request changes the unit of account of an oracle pool. |

### 15.12 `GET /api/v2/admin/deployments`

**Access:** Superadmin.

Return the deployment history of all users. The list is not paginated. Rows are newest first by `createdAt`.

**Response 200**

The description is `All deployment history`. The result has the key `deployments`.

```json
{
  "ok": true,
  "description": "All deployment history",
  "result": {
    "deployments": [
      {
        "id": 55,
        "userId": 4210,
        "trackingId": "00000000-0000-0000-0000-000000000001",
        "resourceType": "TOKEN",
        "createdAt": "2026-08-12T10:00:00Z",
        "ethereumAddress": "0x0000000000000000000000000000000000000001",
        "userName": "Ada Lovelace"
      }
    ]
  }
}
```

### 15.13 `GET /api/v2/admin/vouchers`

**Access:** Superadmin.

Return the voucher catalog for the console, with cursor pagination.
The public list `GET /api/v2/vouchers` (see section 7, Vouchers and offerings) shows only active vouchers that have a location.
This route shows all vouchers.
Each row has the field `active`.
This route has no `bbox` parameter.

**Query parameters**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `q` | string | No | Search term. See section 15.1. |
| `tags` | string | No | Tag filter. Repeat the parameter or use a comma list. Maximum 12 values. Each value must be in the tag vocabulary. |
| `tagMatch` | string | No | `any` (default) or `all`. The case does not matter. |
| `owner` | string | No | Owner address. |
| `status` | string | No | `all` (default), `active` or `retired`. The case does not matter. |
| `perPage`, `cursor`, `next` | | No | See section 15.1. |

The route checks the parameters `facets` and `nearby` like the public list. A value other than `true` or `false` is an error. The route then ignores them.

**Response 200**

The description is `Vouchers`. The result has the keys `vouchers`, `first` and `last`.

```json
{
  "ok": true,
  "description": "Vouchers",
  "result": {
    "vouchers": [
      {
        "id": 88,
        "chainId": 1,
        "address": "0x1111111111111111111111111111111111111111",
        "name": "Community Credit",
        "symbol": "CCR",
        "iconUrl": null,
        "locationName": null,
        "geoLat": null,
        "geoLng": null,
        "kind": "voucher",
        "active": false
      }
    ],
    "first": 88,
    "last": 88
  }
}
```

| Field | Type | Description |
|---|---|---|
| `id` | integer | Voucher ID. |
| `chainId` | integer | Chain ID. |
| `address` | string | Voucher address. |
| `name` | string or null | Display name, or the contract name. |
| `symbol` | string or null | Symbol. |
| `iconUrl` | string or null | Icon URL. |
| `locationName` | string or null | Location label. |
| `geoLat`, `geoLng` | number or null | Coordinates. |
| `kind` | string | Marker kind that the server derives. |
| `active` | boolean | `false` for a retired voucher. |

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `q`, `tags`, `tagMatch`, `owner`, `status`, `facets` or `nearby` is not valid. The description names the rule. |
| `400` | `E04` | `tags` has a value that is not in the tag vocabulary. |

### 15.14 `GET /api/v2/admin/vouchers/:address`

**Access:** Superadmin.

Return one voucher, whatever its visibility. The route has no `active` filter. A `404` means that the catalog does not hold the address.

**Response 200**

The description is `Voucher`. The result has the key `voucher`. The route sends `Cache-Control: no-store`.

The `voucher` object has these fields. The same object is the result of the routes in sections 15.15 and 15.16, except that `active` is absent in section 15.15.

| Field | Type | Description |
|---|---|---|
| `id` | integer | Voucher ID. |
| `chainId` | integer | Chain ID. |
| `address` | string | Voucher address. |
| `name` | string or null | Display name, or the contract name. |
| `contractName` | string or null | Name from the contract. |
| `symbol` | string or null | Symbol. |
| `decimals` | integer or null | Decimals. |
| `ownerAddress` | string or null | Owner. |
| `description` | string or null | Description. |
| `voucherUoa` | string or null | Unit of account. |
| `voucherValue` | string or null | Value as a decimal string. |
| `locationName` | string or null | Location label. |
| `geoLat`, `geoLng` | number or null | Coordinates. |
| `email`, `website`, `phone` | string or null | Contact details. |
| `terms` | string or null | Terms. |
| `iconUrl`, `bannerUrl` | string or null | Image URLs. |
| `tags` | array of strings | Tags. The array is empty when there are no tags. |
| `kind` | string | Marker kind that the server derives. |
| `markerIcon` | string or null | Marker icon. |
| `markerColor` | string or null | Marker color. |
| `active` | boolean | `false` for a retired voucher. |

```json
{
  "ok": true,
  "description": "Voucher",
  "result": {
    "voucher": {
      "id": 88,
      "chainId": 1,
      "address": "0x1111111111111111111111111111111111111111",
      "name": "Community Credit",
      "contractName": "Community Credit",
      "symbol": "CCR",
      "decimals": 6,
      "ownerAddress": "0x0000000000000000000000000000000000000001",
      "description": null,
      "voucherUoa": "KES",
      "voucherValue": "1",
      "locationName": null,
      "geoLat": null,
      "geoLng": null,
      "email": null,
      "website": null,
      "phone": null,
      "terms": null,
      "iconUrl": null,
      "bannerUrl": null,
      "tags": [],
      "kind": "voucher",
      "markerIcon": null,
      "markerColor": null,
      "active": false
    }
  }
}
```

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `address` is not valid. |
| `404` | `E07` | The catalog does not hold this address. |

### 15.15 `PUT /api/v2/admin/vouchers/:address/profile`

**Access:** Superadmin.

Replace the off-chain record of a voucher.
The route changes catalog data only.
It does not change the contract name, symbol, decimals or owner.
It does not retire or restore the voucher.
The owner can use `PUT /api/v2/vouchers/:address/profile` with the same body.
See section 7, Vouchers and offerings.

**Request body**

| Field | Type | Description |
|---|---|---|
| `displayName` | string | Maximum 120 characters. Omit it to keep the old value. Send `""` to remove the override. |
| `description` | string | Maximum 500 characters. |
| `voucherUoa` | string | Maximum 32 characters. The server handles it like `unitOfAccount` in section 15.11. |
| `voucherValue` | number | Value in the unit of account. Must be `0` or more. |
| `email` | string | Must be a valid email address. |
| `website` | string | Must be a valid URL. |
| `phone` | string | No validation. |
| `terms` | string | Maximum 5000 characters. |
| `locationName` | string | Maximum 200 characters. |
| `geoLat` | number | From `-90` to `90`. Send it with `geoLng`. |
| `geoLng` | number | From `-180` to `180`. Send it with `geoLat`. |
| `iconUrl` | string | An `http` or `https` URL. Omit it to keep the old value. Send `""` to clear. |
| `bannerUrl` | string | An `http` or `https` URL. Omit it to keep the old value. Send `""` to clear. |
| `markerIcon` | string | One of `basket`, `hand-coin`, `coupon`, `food`, `farm`, `tools`, `health`, `education`. Omit it to keep the old value. Send `""` to return to the automatic icon. |
| `markerColor` | string | One of `blue`, `purple`, `orange`, `pink`, `cyan`, `brown`. Omit it to keep the old value. Send `""` to return to the automatic color. |
| `tags` | array of strings | The new full set of tags. The tag rules are the same as in section 15.11. |

- The route assigns the whole record. The fields `description`, `voucherUoa`, `voucherValue`, `email`, `website`, `phone`, `terms`, `locationName`, `geoLat`, `geoLng` and `tags` are replaced by the values that you send. If you omit one of these fields, the server clears it.
- Only `displayName`, `iconUrl`, `bannerUrl`, `markerIcon` and `markerColor` keep the old value when you omit them.
- The server saves the profile and the tags together.

**Response 200**

The description is `Voucher profile updated`. The result has the key `voucher`. The object has the fields in section 15.14 except `active`.

The server reads the voucher back through the public query. This query hides a retired voucher. For a retired voucher, the route returns `404` and the server does not save the change. Use `PUT /api/v2/admin/vouchers/:address/restore` first.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | A field fails validation. |
| `400` | `E04` | Only one of `geoLat` and `geoLng` is present. |
| `400` | `E04` | A tag or the unit of account is not valid. |
| `404` | `E03` | The voucher does not exist, or the voucher is retired. The description is `voucher not found`. |
| `409` | `E16` | Another voucher has the same `displayName`. The description is `a voucher with this name already exists`. |

### 15.16 `PUT /api/v2/admin/vouchers/:address/restore`

**Access:** Superadmin.

Return a retired voucher to the public catalog. The route has no owner version. The request has no body.

- The restore must fit the limits of the affected users. If it does not fit, the route returns `429`.
- If the voucher is already active, the route changes nothing and returns `200`.

**Response 200**

The description is `Voucher restored`. The result has the key `voucher` with the fields in section 15.14. The route sends `Cache-Control: no-store`.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `address` is not valid. |
| `404` | `E07` | The catalog does not hold this address. |
| `429` | `E11` | The restore puts a user over a limit. The response has `result` with `resource` (`vouchers` or `offers`), `used` and `limit`. |

```json
{
  "ok": false,
  "description": "vouchers creation limit reached (6 of 5 used)",
  "code": "E11",
  "result": { "resource": "vouchers", "used": 6, "limit": 5 }
}
```

### 15.17 `GET /api/v2/admin/vocabulary`

**Access:** Superadmin.

Return all values of the three shared vocabularies: tags, offering units and units of account. The list includes built-in values and custom values. The response is not paginated.

**Response 200**

The description is `Vocabulary`.

```json
{
  "ok": true,
  "description": "Vocabulary",
  "result": {
    "tags": [
      {
        "value": "Urban Farming",
        "isCustom": true,
        "deployedTokens": 0,
        "deployedPools": 0,
        "vouchers": 2,
        "pools": 0,
        "posts": 1,
        "offerings": 0,
        "catalogs": 0
      }
    ],
    "units": [
      { "value": "Produce Crate", "isCustom": true, "offerings": 4 }
    ],
    "unitsOfAccount": [
      {
        "value": "Community Credits",
        "isCustom": true,
        "profiles": 1,
        "vouchers": 2,
        "pools": 1,
        "voucherDeployments": 2,
        "poolDeployments": 1,
        "total": 7
      }
    ]
  }
}
```

| Key | Fields |
|---|---|
| `tags` | `value`, `isCustom`, and the usage counts `deployedTokens`, `deployedPools`, `vouchers`, `pools`, `posts`, `offerings`, `catalogs`. |
| `units` | `value`, `isCustom`, and the usage count `offerings`. |
| `unitsOfAccount` | `value`, `isCustom`, the usage counts `profiles`, `vouchers`, `pools`, `voucherDeployments`, `poolDeployments`, and `total` (the sum of the five counts). |

The usage counts show what a rename or a delete reaches.

### 15.18 `PUT /api/v2/admin/vocabulary/tags` and `PUT /api/v2/admin/vocabulary/units`

**Access:** Superadmin.

Rename a tag or an offering unit. All records that use the value follow the new name.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `value` | string | Yes | Current value. The server trims it and finds it without regard to case. |
| `replacement` | string | Yes | New value. The server removes extra spaces and keeps the case that you send. Maximum 100 characters for a tag and 32 for a unit. A tag must not contain a comma. |

```json
{ "value": "Urban Farming", "replacement": "Community Gardens" }
```

- You can use the rename to change only the case of a value, for example `kwh` to `kWh`.
- You can rename a built-in tag. It stays built-in.
- You cannot rename a built-in offering unit.
- You cannot rename a tag that an unfinished voucher or pool deployment uses. This rule does not apply when `replacement` is equal to the current value.

**Response 200**

The description is `Vocabulary value renamed`. The result has the key `value`, which is the stored spelling.

```json
{
  "ok": true,
  "description": "Vocabulary value renamed",
  "result": { "value": "Community Gardens" }
}
```

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | `value` or `replacement` is missing or blank. |
| `400` | `E04` | `replacement` is too long (`vocabulary value is too long`), or a tag has a comma. |
| `404` | `E03` | The value does not exist. The description is `vocabulary value not found`. |
| `409` | `E16` | A value with this name already exists (not case-sensitive). |
| `409` | `E16` | The value is a built-in offering unit. |
| `409` | `E16` | An unfinished deployment uses the tag. |

### 15.19 `PUT /api/v2/admin/vocabulary/units-of-account`

**Access:** Superadmin.

Rename a custom unit of account. The new name applies to profiles, vouchers, pools and deployment data. It does not change the stored valuation snapshots of old transfers.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `value` | string | Yes | Current value. The server trims it and finds it without regard to case. |
| `replacement` | string | Yes | New value. Maximum 32 characters. The server removes extra spaces and writes each word with a capital first letter and lower-case other letters. |

**Response 200**

The description is `Unit of account renamed`. The result has the key `value`, which is the stored spelling.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | `value` or `replacement` is missing or blank. |
| `400` | `E04` | `replacement` has more than 32 characters. The description is `unit of account must be 32 characters or fewer`. |
| `404` | `E03` | The value does not exist. |
| `409` | `E16` | The value is built-in. The description is `built-in units of account cannot be renamed`. |
| `409` | `E16` | An unfinished deployment uses the value. |
| `409` | `E16` | A value with this name already exists. |

### 15.20 `POST /api/v2/admin/vocabulary/units-of-account`

**Access:** Superadmin.

Create a custom unit of account. This is the only vocabulary value that has a create route. The server creates tags and offering units when a write names them.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `value` | string | Yes | New value. The server removes extra spaces. Maximum 32 characters. |

- If a custom value with this name exists (not case-sensitive), the route does not change anything and returns `201` with the stored spelling.
- A new value gets the stored spelling with a capital first letter for each word.

**Response 201**

The description is `Custom unit of account created`. The result has the key `value`.

```json
{
  "ok": true,
  "description": "Custom unit of account created",
  "result": { "value": "Community Credits" }
}
```

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | `value` is missing, blank, or longer than 32 characters. |
| `409` | `E16` | The value exists as a built-in value. The description is `unit of account already exists as a built-in value`. |

### 15.21 `DELETE /api/v2/admin/vocabulary/tags` and `DELETE /api/v2/admin/vocabulary/units`

**Access:** Superadmin.

Delete a custom tag or a custom offering unit. The value is in the JSON body, not in the path.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `value` | string | Yes | Value to delete. The server trims it and finds it without regard to case. |

```json
{ "value": "Urban Farming" }
```

- You cannot delete a built-in value.
- Deleting a tag removes it from all vouchers, pools, posts, offerings, catalogs and deployment data that use it. The records stay.
- Deleting an offering unit sets the unit to null on all offerings that use it. The offerings stay.
- The delete does not block the value. A later write that names the value creates it again.

**Response 200**

The description is `Custom vocabulary value deleted`. The result has the keys `value`, `usage` and `total`.

For a tag, `usage` has the counts `deployedTokens`, `deployedPools`, `vouchers`, `pools`, `posts`, `offerings` and `catalogs`. `total` is the sum of the counts.

```json
{
  "ok": true,
  "description": "Custom vocabulary value deleted",
  "result": {
    "value": "Urban Farming",
    "usage": {
      "deployedTokens": 0,
      "deployedPools": 0,
      "vouchers": 2,
      "pools": 0,
      "posts": 1,
      "offerings": 0,
      "catalogs": 0
    },
    "total": 3
  }
}
```

For an offering unit, `usage` has the count `offerings`. `total` is equal to `offerings`.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | `value` is missing or blank. |
| `404` | `E03` | The value does not exist. The description is `vocabulary value not found`. |
| `409` | `E16` | The value is built-in. The description is `built-in vocabulary values cannot be deleted`. |

### 15.22 `DELETE /api/v2/admin/vocabulary/units-of-account`

**Access:** Superadmin.

Delete a custom unit of account. The route deletes the value only when no record uses it.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `value` | string | Yes | Value to delete. The server trims it and finds it without regard to case. |

**Response 200**

The description is `Custom unit of account deleted`. The result has the keys `value`, `usage` and `total`. `usage` has the counts `profiles`, `vouchers`, `pools`, `voucherDeployments` and `poolDeployments`. All five counts are `0` after a successful delete. `total` is the sum.

```json
{
  "ok": true,
  "description": "Custom unit of account deleted",
  "result": {
    "value": "Community Credits",
    "usage": {
      "profiles": 0,
      "vouchers": 0,
      "pools": 0,
      "voucherDeployments": 0,
      "poolDeployments": 0
    },
    "total": 0
  }
}
```

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E02` | The body is not valid JSON. |
| `400` | `E04` | `value` is missing or blank. |
| `404` | `E03` | The value does not exist. |
| `409` | `E16` | The value is built-in (`built-in units of account cannot be deleted`). |
| `409` | `E16` | An unfinished deployment uses the value. |
| `409` | `E16` | A record still uses the value (`unit of account is still in use`). |

### 15.23 `GET /api/v2/admin/review-notifications`

**Access:** Superadmin.

Return the open flags on voucher reviews. An owner of a voucher makes a flag with `POST /api/v2/vouchers/:address/reviews/:id/flag`. The list shows flags that nobody has resolved. Rows are newest first. A page has at most 20 rows.

**Query parameters**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `before` | integer | No | Return flags with an `id` below this value. Use `next` from the previous response. |

**Response 200**

This route does not send a `description`. The route sends `Cache-Control: no-store`.

| Key | Type | Description |
|---|---|---|
| `notifications` | array | Open flags. |
| `next` | integer or null | Value for `before` that returns the next page. The value is `null` when there are no more rows. |

| Field of a notification | Type | Description |
|---|---|---|
| `id` | integer | Flag ID. Use this ID in section 15.24. |
| `reviewId` | integer | Review ID. |
| `voucherAddress` | string | Voucher address. |
| `reason` | string | Reason from the owner. |
| `stars` | integer | Current stars of the review. |
| `comment` | string | Current comment of the review. |
| `reportedStars` | integer | Stars when the owner made the flag. |
| `reportedComment` | string | Comment when the owner made the flag. |
| `createdAt` | string | RFC 3339 time. |

```json
{
  "ok": true,
  "result": {
    "notifications": [
      {
        "id": 31,
        "reviewId": 12,
        "voucherAddress": "0x1111111111111111111111111111111111111111",
        "reason": "Abusive language",
        "stars": 1,
        "comment": "Bad comment",
        "reportedStars": 1,
        "reportedComment": "Bad comment",
        "createdAt": "2026-08-12T10:00:00Z"
      }
    ],
    "next": null
  }
}
```

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | none | `before` is not a valid number. The description is `invalid notification cursor`. The `code` field is empty. |

### 15.24 `PUT /api/v2/admin/review-notifications/:id`

**Access:** Superadmin.

Resolve a flag on a review.

**Path parameters**

| Parameter | Type | Description |
|---|---|---|
| `id` | integer | Flag ID. It must be above `0`. |

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `action` | string | Yes | `hide` hides the review. `dismiss` keeps the review visible. |

```json
{ "action": "hide" }
```

In both cases the server marks all open flags of the review as resolved. It records the address of the caller.

**Response 200**

This route does not send a `description`. The route sends `Cache-Control: no-store`.

```json
{ "ok": true, "result": { "saved": true } }
```

**Errors**

These errors have an empty `code` field.

| Status | Code | Cause |
|---|---|---|
| `400` | none | `id` is not a positive integer. The description is `invalid notification ID`. |
| `400` | none | `action` is not `hide` or `dismiss`. The description is `action must be hide or dismiss`. |
| `400` | `E02` | The body is not valid JSON. |
| `404` | none | The flag does not exist, or it is already resolved. The description is `review or voucher not found, or action is no longer available`. |

### 15.25 `GET /api/v2/admin/reports`

**Access:** Superadmin.

Return posts in all statuses from all authors. The public list `GET /api/v2/reports` (see section 10, Posts) returns approved posts only.

This list does not use the ID cursor of section 15.1. It orders posts by `updatedAt`, newest first, with `id` as the tie-breaker. The cursor has two parts.

**Query parameters**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `perPage` | integer | `20` | Page size. The maximum is `100`. A value of `0` becomes `20`. A value above `100` becomes `100`. A value that is not a non-negative integer returns `400`. |
| `cursorUpdatedAt` | string | none | `updatedAt` of the cursor row, in RFC 3339 format. |
| `cursorId` | integer | none | `id` of the cursor row. You must send it with `cursorUpdatedAt`. |
| `next` | boolean | see below | Direction from the cursor. |
| `status` | string | all | `DRAFT`, `SUBMITTED`, `APPROVED` or `REJECTED`. The case does not matter. There is no value for all statuses. Omit the parameter. |
| `author` | string | all | Author address. The match is exact and not case-sensitive. |
| `q` | string | none | Search term. See section 15.1. |
| `tags` | string | none | Tag filter. Repeat the parameter or use a comma list. Maximum 12 values. Each value must be in the tag vocabulary. |
| `tagMatch` | string | `any` | `any` or `all`. |
| `vouchers` | string | none | Voucher filter. A list of `chainId:address` values, with commas or repeated parameters. Maximum 12 values. Each voucher must exist in the catalog. |
| `voucherMatch` | string | `any` | `any` or `all`. |

- If you omit `next` and send a cursor, the route returns the next page. These posts are older.
- `next=true` also returns older posts. `next=false` returns newer posts. You must send a cursor with `next`.
- To get the next page, send the `last` cursor of the response. To get the previous page, send the `first` cursor with `next=false`.
- The route also reads `facets` and `nearby` with the same checks as the public list. The route ignores them.

**Response 200**

The description is `Reports`. The route sends `Cache-Control: no-store`.

| Key | Type | Description |
|---|---|---|
| `reports` | array | Page of post summaries. |
| `first` | object or null | Cursor of the first post: `updatedAt` and `id`. The value is `null` for an empty page. |
| `last` | object or null | Cursor of the last post. The value is `null` for an empty page. |

A post summary has these fields: `id`, `title`, `description`, `imageUrl`, `geoLat`, `geoLng`, `periodFrom`, `periodTo`, `authorAddress`, `status`, `createdAt`, `updatedAt`, `tags` and `vouchers`. Each item of `vouchers` has `id`, `chainId`, `address`, `name` and `symbol`. The detail route in section 15.26 returns more fields.

```json
{
  "ok": true,
  "description": "Reports",
  "result": {
    "reports": [
      {
        "id": 4633,
        "title": "Market day",
        "description": "Short text",
        "imageUrl": null,
        "geoLat": null,
        "geoLng": null,
        "periodFrom": null,
        "periodTo": null,
        "authorAddress": "0x0000000000000000000000000000000000000001",
        "status": "APPROVED",
        "createdAt": "2026-08-12T10:00:00Z",
        "updatedAt": "2026-08-12T10:00:00Z",
        "tags": ["Food"],
        "vouchers": []
      }
    ],
    "first": { "updatedAt": "2026-08-12T10:00:00Z", "id": 4633 },
    "last": { "updatedAt": "2026-08-12T10:00:00Z", "id": 4633 }
  }
}
```

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `perPage` is not a non-negative integer. |
| `400` | `E04` | The cursor is not valid. The description is one of: `cursorUpdatedAt and cursorId must be sent together`, `cursorUpdatedAt must be an RFC3339 timestamp`, `cursorId must be a positive integer`. |
| `400` | `E04` | `next` is not `true` or `false`, or `next` has no cursor. |
| `400` | `E04` | `status` is not valid. The description is `status must be one of DRAFT, SUBMITTED, APPROVED, REJECTED`. |
| `400` | `E04` | `author` is not a valid address. |
| `400` | `E04` | `q`, `tags`, `tagMatch`, `vouchers` or `voucherMatch` is not valid, or a tag or voucher does not exist. |

### 15.26 `GET /api/v2/admin/reports/:id`

**Access:** Superadmin.

Return one complete post in any status.

**Path parameters**

| Parameter | Type | Description |
|---|---|---|
| `id` | integer | Post ID. It must be above `0`. |

**Response 200**

The description is `Report`. The result has the key `report`. The route sends `Cache-Control: no-store`.

The `report` object has the fields of a post summary (section 15.25) and these fields. A field is absent when it has no value.

| Field | Type | Description |
|---|---|---|
| `body` | string | Text in Markdown. |
| `hostName` | string | Host name from the field report form. |
| `participants` | string | Participants from the form. |
| `timeTaken` | string | Time taken from the form. |
| `images` | array | Images. Each item has `url`, `width`, `height`, `maxWidth`, `maxHeight`, `byteSize` and `caption`. |
| `attachments` | array | Other media. Each item has `kind` (`FILE` or `EMBED`), `url`, `filename` and `byteSize`. |

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `id` is not a positive integer. |
| `404` | `E03` | The post does not exist. The description is `report not found`. |

### 15.27 `GET /api/v2/admin/dashboard/stats`

**Access:** Superadmin.

Return the totals and the time series for the console dashboard.

**Query parameters**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `range` | string | `1d` | `5m`, `15m`, `30m`, `1h`, `6h`, `1d` or `custom`. The window ends now. |
| `since` | string | none | Start of a custom window, in RFC 3339 format. |
| `until` | string | none | End of a custom window, in RFC 3339 format. |

- If you send `since` or `until`, or `range=custom`, the route uses a custom window. You must then send both values.
- In a custom window, `until` must be after `since`. The window must be at least 1 minute and at most 90 days.
- The server picks the bucket size of the series from the length of the window.

| Window length | Bucket size |
|---|---|
| up to 5 minutes | 30 seconds |
| up to 15 minutes | 1 minute |
| up to 30 minutes | 2 minutes |
| up to 1 hour | 5 minutes |
| up to 6 hours | 15 minutes |
| up to 24 hours | 1 hour |
| up to 7 days | 6 hours |
| more than 7 days | 24 hours |

**Response 200**

The description is `Admin dashboard totals`.

```json
{
  "ok": true,
  "description": "Admin dashboard totals",
  "result": {
    "range": {
      "preset": "1d",
      "since": "2026-08-12T00:00:00Z",
      "until": "2026-08-13T00:00:00Z",
      "bucket": "1h"
    },
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

| Field | Description |
|---|---|
| `range.preset` | The `range` value, or `custom`. |
| `range.since`, `range.until` | Window limits in UTC, RFC 3339. |
| `range.bucket` | Bucket size, for example `30s`, `5m`, `1h` or `1d`. |
| `pools` | Number of all pools in the catalog. It does not depend on the window. |
| `vouchers` | Number of all vouchers in the catalog. It does not depend on the window. |
| `accounts` | Number of all accounts. It does not depend on the window. |
| `reports` | Number of approved posts created in the window. |
| `transactions` | Number of transactions in the window. It is the sum of `transactionSeries`. |
| `catalogSeries` | One item for each bucket: `t`, and the numbers of `reports`, `vouchers` and `pools` created in the bucket. |
| `transactionSeries` | One item for each bucket: `t` and `transactions`. The array is empty when federation is not available. |

`transactions` is `null` when federation is not configured or when its read fails.
The federation read stops after 8 seconds.
The route still returns `200` with the catalog data.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `range` is not one of the listed values. The description is `unknown range "<value>"; use 5m,15m,30m,1h,6h,1d,custom`. |
| `400` | `E04` | A custom window has only one of `since` and `until`. The description is `custom range requires since and until (RFC3339)`. |
| `400` | `E04` | `since` or `until` is not in RFC 3339 format. |
| `400` | `E04` | `until` is not after `since`, the window is shorter than 1 minute, or the window is longer than 90 days. |

There is no route `GET /api/v2/admin/dashboard/transactions`. Use the routes in sections 15.28 and 15.29.

### 15.28 `GET /api/v2/admin/transactions/vouchers` and `GET /api/v2/admin/transactions/pools`

**Access:** Superadmin.

Return events from federation, with cursor pagination.
`vouchers` returns the movements of vouchers. `pools` returns the operations of pools.

**Query parameters**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `type` | string | No | Event type. The case does not matter. For `vouchers`: `transfer`, `mint` or `burn`. For `pools`: `swap` or `deposit`. |
| `voucher` | string | No | Voucher address. Only for `vouchers`. |
| `pool` | string | No | Pool address. Only for `pools`. |
| `q` | string | No | Search term. See section 15.1. The server searches the transaction hash and the addresses of the event. |
| `perPage`, `cursor`, `next` | | No | See section 15.1. |

**Response 200**

The description is `Voucher transactions` or `Pool transactions`.
The result has the keys `transactions`, `first` and `last`.
Rows are in chain order, newest first.
The `id` of a row is a packed number of the transaction and the log index.
Use it only as a cursor.

Fields of a voucher transaction:

| Field | Type | Description |
|---|---|---|
| `id` | integer | Cursor value. |
| `txId` | integer | Transaction ID. |
| `logIndex` | integer | Index of the event in the transaction. |
| `txHash` | string | Transaction hash. |
| `t` | string | Time of the block, RFC 3339. |
| `success` | boolean | `true` if the transaction succeeded. |
| `type` | string | `transfer`, `mint` or `burn`. |
| `voucher` | string | Voucher address. |
| `voucherName` | string | Name from the local catalog. An empty string if the catalog does not know the voucher. |
| `voucherSymbol` | string | Symbol from the local catalog. An empty string if the catalog does not know the voucher. |
| `voucherDecimals` | integer or null | Decimals from the catalog. `null` if the catalog has no value. |
| `from` | string | Sender. For a mint, the minter. For a burn, the account that burns. |
| `to` | string or null | Recipient. `null` for a burn. |
| `amount` | string | Amount in the smallest unit, as a decimal string. |

Fields of a pool transaction:

| Field | Type | Description |
|---|---|---|
| `id`, `txId`, `logIndex`, `txHash`, `t`, `success` | | Same as for a voucher transaction. |
| `type` | string | `swap` or `deposit`. |
| `pool` | string | Pool address. |
| `initiator` | string | Account that sent the transaction. |
| `tokenIn` | string | Voucher that goes in. |
| `tokenInSymbol` | string | Symbol from the catalog. An empty string if unknown. |
| `tokenInDecimals` | integer or null | Decimals from the catalog. |
| `amountIn` | string | Amount in. |
| `tokenOut` | string or null | Voucher that comes out. `null` for a deposit. |
| `tokenOutSymbol` | string | Symbol from the catalog. An empty string if unknown or for a deposit. |
| `tokenOutDecimals` | integer or null | Decimals from the catalog. |
| `amountOut` | string or null | Amount out. `null` for a deposit. |
| `fee` | string or null | Fee, in `tokenOut`, without the protocol fee. `null` for a deposit. |

```json
{
  "ok": true,
  "description": "Voucher transactions",
  "result": {
    "transactions": [
      {
        "id": 905000003,
        "txId": 905,
        "logIndex": 3,
        "txHash": "0x00000000000000000000000000000000000000000000000000000000000000a1",
        "t": "2026-08-12T10:00:00Z",
        "success": true,
        "type": "transfer",
        "voucher": "0x1111111111111111111111111111111111111111",
        "voucherName": "Community Credit",
        "voucherSymbol": "CCR",
        "voucherDecimals": 6,
        "from": "0x0000000000000000000000000000000000000001",
        "to": "0x0000000000000000000000000000000000000002",
        "amount": "1000000"
      }
    ],
    "first": 905000003,
    "last": 905000003
  }
}
```

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `type` is not valid. The description is `unknown type; use ` followed by the list of values. |
| `400` | `E04` | `voucher` or `pool` is not a valid address. The description is `voucher must be a valid hex address` or `pool must be a valid hex address`. |
| `400` | `E04` | `q` is not valid. |
| `503` | none | Federation is not configured. The description is `Federation is not configured`. The `code` field is empty. |

### 15.29 `GET /api/v2/admin/transactions/otx`

**Access:** Superadmin.

Return the origin transactions that this service signed, with cursor pagination.
The route reads only local data. It works when federation is not available.
An origin transaction with the status `REVERTED` is not the same as a reverted transfer on the chain.

**Query parameters**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `type` | string | No | Transaction type. The server converts the value to upper case. See the list below. |
| `status` | string | No | Dispatch status. The server converts the value to upper case. See the list below. |
| `q` | string | No | Search term. See section 15.1. The server searches the tracking ID and the transaction hash. |
| `perPage`, `cursor`, `next` | | No | See section 15.1. |

Values of `type`: `TOKEN_PROXY_DEPLOY`, `POOL_DEPLOY_STEP`, `POOL_PROXY_DEPLOY`, `POOL_TOKEN_INDEX_DEPLOY`, `POOL_LIMITER_DEPLOY`, `POOL_FEE_POLICY_DEPLOY`, `POOL_ORACLE_QUOTER_DEPLOY`, `POOL_RELATIVE_QUOTER_DEPLOY`, `PROXY_UPGRADE`, `RELAY_DELEGATE`, `RELAY_EXEC`, `RELAY_DELEGATE_EXEC`, `ACCOUNT_REGISTER`.

Values of `status`: `PENDING`, `IN_NETWORK`, `SUCCESS`, `REVERTED`, `LOW_NONCE`, `NO_GAS`, `LOW_GAS_PRICE`, `NETWORK_ERROR`, `REPLACEMENT_UNDERPRICED`, `UNKNOWN_RPC_ERROR`.

**Response 200**

The description is `Origin transactions`. The result has the keys `otx`, `first` and `last`. The `first` and `last` values are origin transaction IDs.

```json
{
  "ok": true,
  "description": "Origin transactions",
  "result": {
    "otx": [
      {
        "id": 351,
        "trackingId": "00000000-0000-0000-0000-000000000001",
        "otxType": "POOL_PROXY_DEPLOY",
        "signer": "0x0000000000000000000000000000000000000009",
        "txHash": "0x00000000000000000000000000000000000000000000000000000000000000b2",
        "nonce": 17,
        "replaced": false,
        "status": "SUCCESS",
        "createdAt": "2026-08-12T10:00:00Z",
        "updatedAt": "2026-08-12T10:01:00Z"
      }
    ],
    "first": 351,
    "last": 351
  }
}
```

| Field | Type | Description |
|---|---|---|
| `id` | integer | Origin transaction ID. |
| `trackingId` | string | Tracking ID. |
| `otxType` | string | Transaction type. |
| `signer` | string | Service signer account that sent the transaction. |
| `txHash` | string | Transaction hash. |
| `nonce` | integer | Nonce. |
| `replaced` | boolean | `true` if another transaction replaced this one. |
| `status` | string | Dispatch status. |
| `createdAt`, `updatedAt` | string | RFC 3339 times. |

The row does not include the raw signed transaction.

**Errors**

| Status | Code | Cause |
|---|---|---|
| `400` | `E04` | `type` is not valid. The description is `unknown type; use one of ` followed by the list. |
| `400` | `E04` | `status` is not valid. The description is `unknown status; use one of ` followed by the list. |
| `400` | `E04` | `q` is not valid. |

---

## 16. Route index

This table lists every route. The "Access" column shows the access level.
Routes marked with * exist only when the related feature is configured: `/api/v2/pretium/*` needs Pretium, and `/api/v2/accounts/*` and `/api/v2/federation/*` need federation.

| Method | Path | Access |
|---|---|---|
| POST | `/auth/nonce` | Public |
| POST | `/auth/signup` | Public |
| POST | `/auth/signin` | Public |
| POST | `/auth/logout` | Public |
| POST | `/auth/webauthn/verify` | Public |
| GET | `/auth/session` | Public (the route checks the session cookie) |
| GET | `/auth/webauthn/credentials` | Public (the route checks the session cookie) |
| POST | `/auth/siwe/nonce` | Public |
| POST | `/auth/siwe/verify` | Public |
| GET | `/auth/profile` | User session |
| POST | `/auth/profile` | User session |
| POST | `/auth/storage-token` | User session |
| GET | `/api/v2/vouchers` | Public (user session if nearby=true) |
| GET | `/api/v2/vouchers/:address` | Public |
| GET | `/api/v2/vouchers/:address/offerings` | Public |
| GET | `/api/v2/vouchers/:address/offerings/:id` | Public |
| GET | `/api/v2/vouchers/:address/reviews` | Public |
| GET | `/api/v2/offerings` | Public (user session if nearby=true) |
| GET | `/api/v2/marketplace` | Public (user session if nearby=true) |
| GET | `/api/v2/catalogs` | Public (user session if nearby=true) |
| GET | `/api/v2/catalogs/:id` | Public |
| GET | `/api/v2/catalogs/:id/pools` | Public |
| GET | `/api/v2/catalogs/:id/vouchers` | Public |
| GET | `/api/v2/pools` | Public (user session if nearby=true) |
| GET | `/api/v2/pools/:address` | Public |
| GET | `/api/v2/pools/:address/vouchers` | Public |
| GET | `/api/v2/pools/:address/offerings` | Public |
| GET | `/api/v2/vouchers/:address/reviews/me` | User session |
| PUT | `/api/v2/vouchers/:address/reviews/me` | User session |
| PUT | `/api/v2/vouchers/:address/reviews/:id/reply` | User session |
| POST | `/api/v2/vouchers/:address/reviews/:id/flag` | User session |
| POST | `/api/v2/catalogs` | User session |
| PUT | `/api/v2/catalogs/:id` | User session |
| GET | `/api/v2/catalogs/:id/membership` | User session |
| PUT | `/api/v2/catalogs/:id/pools/:poolId` | User session |
| DELETE | `/api/v2/catalogs/:id/pools/:poolId` | User session |
| PUT | `/api/v2/catalogs/:id/vouchers/:voucherId` | User session |
| DELETE | `/api/v2/catalogs/:id/vouchers/:voucherId` | User session |
| POST | `/api/v2/contracts/token` | User or service token |
| POST | `/api/v2/contracts/pool` | User or service token |
| POST | `/api/v2/contracts/version` | Service token |
| POST | `/api/v2/contracts/upgrade/token` | Service token |
| POST | `/api/v2/contracts/upgrade/pool` | Service token |
| GET | `/api/v2/token-tags` | User or service token |
| GET | `/api/v2/offering-units` | User or service token |
| GET | `/api/v2/units-of-account` | User session |
| GET | `/api/v2/symbols` | User or service token |
| GET | `/api/v2/names` | User or service token |
| GET | `/api/v2/recipients` | User or service token |
| GET | `/api/v2/recipients/suggest` | Public |
| GET | `/api/v2/recipients/resolve` | User or service token |
| GET | `/api/v2/recipients/resolve/:name` | User or service token |
| GET | `/api/v2/recipients/reverse/:address` | User or service token |
| GET | `/api/v2/oracle-feeds` | User or service token |
| GET | `/api/v2/system` | User or service token |
| GET | `/api/v2/otx/track/:trackingId` | User or service token |
| GET | `/api/v2/otx/track/:trackingId/contract` | User or service token |
| GET | `/api/v2/account/otx/:address` | User or service token |
| GET | `/api/v2/account/status/:address` | User or service token |
| GET | `/api/v2/reports` | Public (user session if nearby=true) |
| GET | `/api/v2/reports/:id` | Public |
| POST | `/api/v2/reports` | User session |
| PUT | `/api/v2/reports/:id` | User session |
| PUT | `/api/v2/reports/:id/approve` | Superadmin |
| DELETE | `/api/v2/reports/:id` | Superadmin |
| POST | `/api/v2/vouchers/:address/offerings` | User session |
| PUT | `/api/v2/vouchers/:address/offerings/:id` | User session |
| DELETE | `/api/v2/vouchers/:address/offerings/:id` | User session |
| PUT | `/api/v2/pools/:address/profile` | User session |
| PUT | `/api/v2/vouchers/:address/display-name` | User session |
| PUT | `/api/v2/vouchers/:address/profile` | User session |
| DELETE | `/api/v2/vouchers/:address` | User session |
| POST | `/api/v2/pretium/trigger-onramp`* | User or service token |
| POST | `/api/v2/pretium/link`* | User or service token |
| GET | `/api/v2/pretium/link/:phoneNumber`* | Service token or superadmin |
| GET | `/api/v2/pretium/link-by-address/:address`* | User or service token |
| GET | `/api/v2/pretium/transactions/:phoneNumber`* | Service token or superadmin |
| GET | `/api/v2/pretium/transactions-by-address/:address`* | User or service token |
| GET | `/api/v2/pretium/transactions-recent`* | Service token or superadmin |
| GET | `/api/v2/pretium/rates`* | User or service token |
| GET | `/api/v2/accounts/:address/tokens/latest`* | User or service token |
| GET | `/api/v2/accounts/:address/tokens/issued`* | User or service token |
| GET | `/api/v2/accounts/:address/tokens`* | User or service token |
| GET | `/api/v2/accounts/:address/transfers`* | User or service token |
| GET | `/api/v2/accounts/:address/activity`* | User or service token |
| GET | `/api/v2/users/me` | User session |
| GET | `/api/v2/users/me/resource-usage` | User session |
| GET | `/api/v2/users/me/catalogs` | User session |
| GET | `/api/v2/users/me/vouchers` | User session |
| GET | `/api/v2/users/me/tokens` | User session |
| GET | `/api/v2/users/me/tokens/:address` | User session |
| GET | `/api/v2/users/me/pools` | User session |
| GET | `/api/v2/users/me/pools/:address` | User session |
| GET | `/api/v2/users/me/deployments` | User session |
| GET | `/api/v2/users/me/shares/eligibility/:address` | User session |
| POST | `/api/v2/users/me/shares/:txHash` | User session |
| GET | `/api/v2/users/me/shares/:txHash` | User session |
| GET | `/api/v2/users/me/reports` | User session |
| GET | `/api/v2/users/me/reports/:id` | User session |
| GET | `/api/v2/admin/users` | Superadmin |
| PUT | `/api/v2/admin/users/:address/approve` | Superadmin |
| PUT | `/api/v2/admin/users/:address/reject` | Superadmin |
| PUT | `/api/v2/admin/users/:address/limits` | Superadmin |
| PUT | `/api/v2/admin/users/:address/profile` | Superadmin |
| GET | `/api/v2/admin/users/:address/pools` | Superadmin |
| GET | `/api/v2/admin/users/:address/stats` | Superadmin |
| GET | `/api/v2/admin/tokens` | Superadmin |
| GET | `/api/v2/admin/pools` | Superadmin |
| GET | `/api/v2/admin/pools/:address/vouchers` | Superadmin |
| PUT | `/api/v2/admin/pools/:address/profile` | Superadmin |
| PUT | `/api/v2/admin/vouchers/:address/profile` | Superadmin |
| GET | `/api/v2/admin/vouchers` | Superadmin |
| GET | `/api/v2/admin/vouchers/:address` | Superadmin |
| PUT | `/api/v2/admin/vouchers/:address/restore` | Superadmin |
| GET | `/api/v2/admin/deployments` | Superadmin |
| GET | `/api/v2/admin/vocabulary` | Superadmin |
| PUT | `/api/v2/admin/vocabulary/tags` | Superadmin |
| PUT | `/api/v2/admin/vocabulary/units` | Superadmin |
| POST | `/api/v2/admin/vocabulary/units-of-account` | Superadmin |
| PUT | `/api/v2/admin/vocabulary/units-of-account` | Superadmin |
| DELETE | `/api/v2/admin/vocabulary/tags` | Superadmin |
| DELETE | `/api/v2/admin/vocabulary/units` | Superadmin |
| DELETE | `/api/v2/admin/vocabulary/units-of-account` | Superadmin |
| GET | `/api/v2/admin/review-notifications` | Superadmin |
| PUT | `/api/v2/admin/review-notifications/:id` | Superadmin |
| GET | `/api/v2/admin/reports` | Superadmin |
| GET | `/api/v2/admin/reports/:id` | Superadmin |
| GET | `/api/v2/admin/dashboard/stats` | Superadmin |
| GET | `/api/v2/admin/transactions/vouchers` | Superadmin |
| GET | `/api/v2/admin/transactions/pools` | Superadmin |
| GET | `/api/v2/admin/transactions/otx` | Superadmin |
| GET | `/api/v2/federation/system`* | User or service token |
| GET | `/api/v2/relayer/health` | Public |
| GET | `/api/v2/relayer/nonce/:address` | Public |
| GET | `/api/v2/relayer/calibur-nonce/:address` | Public |
| GET | `/api/v2/relayer/calibur-nonce/:address/:keyHash` | Public |
| GET | `/api/v2/relayer/track/:trackingId` | Public |
| POST | `/api/v2/relayer/delegate` | Public |
| POST | `/api/v2/relayer/relay` | Public |
| POST | `/api/v2/relayer/relay/preview` | Public |
| GET | `/api/v2/relayer/webauthn/challenge` | Public |
| POST | `/api/v2/relayer/webauthn/recover` | Public |
