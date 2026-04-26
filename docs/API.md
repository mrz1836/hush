# API

## Scope

Phase 0 documents the intended API surface before implementation hardens it.

All routes are prefixed by a random path segment under `/h/<prefix>/...`.

## Planned endpoints

### `POST /h/<prefix>/claim`
Request a session.

Inputs:
- scope
- reason
- ttl
- session_type
- ephemeral_pubkey
- nonce
- timestamp
- signature

Behavior:
- verify client identity and replay protections
- send Discord approval request
- issue scoped JWT on approval

### `GET /h/<prefix>/s/<name>`
Fetch one secret.

Behavior:
- validate JWT
- validate scope
- validate IP binding
- return ECIES-encrypted secret payload

### `POST /h/<prefix>/revoke/<jti>`
Revoke an active token.

### `GET /h/<prefix>/hz`
Health/status endpoint.

## Phase 0 API constraints

- no public endpoint model
- no auto-approve path
- no plaintext secret response body
- no endpoint that writes secret material to agent disk
