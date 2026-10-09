# Fake cloud database API

Base URL: `$FAKECLOUD_URL`. JSON in and out. No authentication.

## Database object

```json
{
  "id": "db-7f3a9c",
  "name": "orders-shop",
  "engine": "postgres",
  "sizeGB": 20,
  "status": "creating",
  "endpoint": "orders-shop.db.fakecloud.local:5432",
  "username": "admin"
}
```

`status` is one of `creating`, `available`, `resizing`, `deleting`.
A new database stays `creating` for a few seconds, then becomes `available`.
A resize goes through `resizing` back to `available`.

## Endpoints

| Method and path | Body | Success | Errors |
| --- | --- | --- | --- |
| `POST /v1/databases` | `{"name","engine","sizeGB"}` | `201` with the database object plus `"password"` | `409` if a database with that `name` already exists, `400` if invalid |
| `GET /v1/databases?name=<name>` | none | `200` with `{"items":[...]}`, zero or one item | none |
| `GET /v1/databases/{id}` | none | `200` with the database object | `404` |
| `PATCH /v1/databases/{id}` | `{"sizeGB"}` | `200` with the database object | `404`, `409` if not `available` |
| `DELETE /v1/databases/{id}` | none | `202`; the database is gone a few seconds later | `404` |
| `POST /v1/databases/{id}/reset-password` | none | `200` with `{"password"}` | `404` |

Notes:

- Names are unique across the account. Names may contain lowercase letters,
  digits and `-`, and must be at most 63 characters.
- **The password is returned only by create and reset-password.** It can't be
  read back later.
- The API may be slow. Clients should use timeouts.
