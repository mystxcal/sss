# HTTP Contract

## Authentication

Public endpoints require:

```http
Authorization: Basic base64("sss:<password>")
```

over HTTPS.

The fixed username is `sss`.

Local Unix-socket endpoints rely on filesystem authorization and must not be exposed by the public listener.

## Simple send

### `POST /s`

Content type: `multipart/form-data`.

Fields:

- repeated `file` binary parts;
- optional `note` string;
- optional `ttl` integer minutes.

At least one `file` is required.

Success:

```http
HTTP/1.1 201 Created
Content-Type: text/plain; charset=utf-8
Location: /v1/transfers/K7M4-Q2PX

K7M4-Q2PX
```

Optional request header:

```text
Idempotency-Key: opaque client-generated value
```

### `POST /s/raw`

Body: arbitrary bytes.

Required header:

```text
X-SSS-Filename
```

Optional:

```text
X-SSS-Note
X-SSS-TTL
Idempotency-Key
```

## Simple receive

### `GET /r/{code}`

Query:

```text
format=auto|raw|zip|tar
```

Auto behavior:

- one regular root file: raw file;
- otherwise: ZIP.

Responses use safe `Content-Disposition`.

`HEAD /r/{code}` may expose content metadata when practical but must not force precomputation of generated archive length.

## Metadata

### `GET /v1/transfers/{code}`

Returns committed metadata and manifest summary. It does not return internal filesystem paths.

## Advanced create

### `POST /v1/transfers`

Creates an uncommitted transfer and upload resources.

The request declares:

- TTL;
- optional note;
- segment plan summary;
- expected sizes;
- optional client transfer ID.

## Resumable upload

### `HEAD /v1/uploads/{upload_id}`

Returns:

```text
Upload-Offset
Upload-Length
Tus-Resumable: 1.0.0
```

### `PATCH /v1/uploads/{upload_id}`

Content type:

```text
application/offset+octet-stream
```

Requires exact `Upload-Offset`.

## Commit

### `POST /v1/transfers/{transfer_id}/commit`

Validates the final manifest and digests, materializes, publishes, allocates code, and returns committed metadata.

Must be idempotent. Repeating after success returns the original code.

## Claims

### `POST /v1/claims`

Request contains code. Response contains:

- claim ID;
- bounded claim token;
- lease expiry;
- manifest;
- segment download information.

### `GET /v1/claims/{claim_id}/segments/{segment_id}`

Requires claim bearer token. Supports ranges on immutable segment content.

### `POST /v1/claims/{claim_id}/complete`

Records completion; it does not consume the transfer.

## Local path

### `GET /local/r/{code}`

Unix socket only.

Default response: plain path.

Under `Accept: application/json`:

```json
{
  "ok": true,
  "code": "K7M4-Q2PX",
  "path": "/srv/sss/live/7c/<transfer-id>/payload",
  "read_only": true,
  "lease_until": "..."
}
```

## Error envelope

JSON APIs:

```json
{
  "error": {
    "code": "TRANSFER_EXPIRED",
    "message": "transfer has expired",
    "request_id": "..."
  }
}
```

Simple plain-text endpoints may return:

```text
TRANSFER_EXPIRED: transfer has expired
```

with the same HTTP status.
