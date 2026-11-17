# Amphora (Go)

Go implementation of the Carbyne Stack Amphora secret-share service. It is a
drop-in replacement for the Java service in this repository: same REST API,
Postgres schema, MinIO layout, Redis keys (Java-serialised values are written
byte-compatibly) and MP-SPDZ share format. The Java sources stay in the
repository as the reference implementation.

## Build and run

```bash
cd go
go build -o amphora ./cmd/amphora
docker build -t amphora-service:dev .      # distroless image
```

## Configuration (environment variables)

| Variable                              | Default                 | Meaning                                                     |
|---------------------------------------|-------------------------|-------------------------------------------------------------|
| `AMPHORA_SERVICE_PORT`                | `10000`                 | HTTP port                                                   |
| `PLAYER_ID`                           | `0`                     | This party's id                                             |
| `AMPHORA_VC_PARTNERS`                 |                         | Comma separated base URIs of the other parties' Amphoras    |
| `CASTOR_SERVICE_URI`                  |                         | Local Castor base URI                                       |
| `AMPHORA_OPA_URL`                     | `http://opa.carbynestack.io` | OPA endpoint                                           |
| `AMPHORA_OPA_DEFAULT_POLICY_PACKAGE`  | `carbynestack.def`      | Default OPA policy package                                  |
| `AMPHORA_USER_ID_FIELD_NAME`          | `sub`                   | JWT claim that carries the user id                          |
| `AMPHORA_OPENING_TIMEOUT`             | `5000`                  | Wait for partners' interim values (ms)                      |
| `MAC_KEY`                             |                         | This party's MAC key share                                  |
| `SPDZ_PRIME` / `SPDZ_R` / `SPDZ_R_INV`|                         | SPDZ field parameters                                       |
| `DB_HOST` / `DB_PORT` / `DB_NAME`     | `localhost` / `5432` / `amphora` | Postgres (`DB_NAME` is an extension)               |
| `DB_USER` / `DB_PASSWORD`             |                         | Postgres credentials                                        |
| `REDIS_HOST` / `REDIS_PORT`           |                         | Redis                                                       |
| `MINIO_ENDPOINT`                      |                         | MinIO endpoint                                              |
| `NO_SSL_VALIDATION`, `TRUSTED_CERTIFICATES` |                   | TLS towards partners                                        |
| `COMPAT_JAVA_TAG_REPLACE_BUG`         | `true`                  | Java behaviour for PUT /tags; see below                     |
| `LOG_LEVEL`                           | `INFO`                  |                                                             |

## Go client

`go/client` replaces `amphora-java-client` (Java-to-Go table in
`client/doc.go`): create/get/delete secrets (secret sharing, input masks and MAC
verification across all parties), tags, listing with filter/sort/paging, and the
intra-/inter-VCP service clients. `go/spdz` exposes the SPDZ share encoding.

```go
c, _ := client.New(client.Options{ServiceAddresses: []string{"http://a0:10000", "http://a1:10000"},
	Prime: p, R: r, RInv: rInv, BearerTokenProvider: func(string) string { return token }})
id, _ := c.CreateSecret(ctx, client.NewSecret(nil, []*big.Int{big.NewInt(42)}))
s, _ := c.GetSecret(ctx, id)
```

## Testing

```bash
go vet ./... && go test -race ./...            # needs docker (Postgres/Redis/MinIO via testcontainers), skipped otherwise
go test -race -coverprofile=cover.out -coverpkg=./... $(go list ./... | grep -v /e2e) && scripts/check-coverage.sh cover.out
go test -run='^$' -fuzz='^FuzzParseMaskedInput$' -fuzztime=20s ./internal/model   # fuzz targets, see TESTS.md part C
AMPHORA_URLS=... CASTOR_URLS=... go test -tags integration -run TestLive ./client   # live two-party flow
go run ./tools/e2erun -amphora u0,u1 -castor c0,c1 -upload                           # full flow with MAC checks
```

Parity against the Java service (163 recorded request/response and state
fixtures, two parties, real Castors, OPA) is the `parity/amphora` harness of the
migration workspace (`IMPL=go go test` against `scripts/up_go.sh`). The existing
Java client is verified against the Go service with
`amphora-java-client/src/test/.../LiveGoServiceIT.java`.

## Behaviour compared with the Java service

Verified against the ORIGINAL published image (`ghcr.io/carbynestack/amphora-service:0.3.0`, OpenJDK 1.8.0_292):
all 157 parity fixtures recorded from that image pass with no exceptions (`parity/amphora`, `scripts/up_img.sh`).

* `PUT /secret-shares/{id}/tags` behaves like Java by default: it always fails with HTTP 500
  (Hibernate `ObjectDeletedException`). `COMPAT_JAVA_TAG_REPLACE_BUG=false` (environment
  variable; the chart is the original one and does not set it) opts into actually replacing the tags.
* `GET /intra-vcp/secret-shares/{id}?programId=` (empty program id) answers 500 "Subject is required to evaluate
  the policy", like Java; a missing `programId` answers 400.
* A secret-share upload without tags/data answers 500 with body `null`, which is what the Java 8 image returns
  (the longer NullPointerException text only exists on JDK 15+).
* UUID path/query values follow Spring + Java 8 `UUID.fromString` and JSON values follow Jackson, exactly
  (`internal/model/javauuid.go`, cases measured on the image).
* **Authentication is unchanged**: like the Java `JwtReader`, the service reads the user id claim from the
  bearer token without verifying signature, expiry, issuer or audience. Token validation is expected at the
  gateway (Istio/Thymus). Do not expose Amphora without it.
* `DB_NAME` is configurable (extension; default `amphora` as in Java). Log format differs (structured `slog`).
