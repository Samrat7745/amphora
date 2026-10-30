# Java test cases -> Go tests

Mapping of the original Amphora Java tests (amphora-service, amphora-common, amphora-java-client) to the Go tests that cover
the same behaviour. Part A covers REST, OPA, calculation, config and the common entities; part B covers persistence, the
storage service and the client. Entries marked "n/a" have no Go counterpart, with the reason.

## Part A - REST, OPA, calculation, config, common entities

Paths are relative to `go/`. "HTTP-level" means the Java controller unit test (direct method call with
mocks) was ported to an HTTP request against `httpapi.API` with a fake `Backend` (controllers_test.go).

## amphora-service

### rest/InputMaskShareControllerTest (3)
| Java test | Go test |
|---|---|
| givenSuccessfulRequest_whenGetInputMasks_thenReturnExpectedResult | internal/httpapi/controllers_test.go `TestInputMasksController/givenSuccessfulRequest...` |
| givenEmptyStringAsRequestId_whenGetInputMasks_thenThrowIAE | `TestInputMasksController/givenMissingOrEmptyRequestId...` (Java IAE; over HTTP a missing param is an empty 400) |
| givenInvalidCountArgument_whenGetInputMasks_thenThrowIAE | `TestInputMasksController/givenInvalidCountArgument...` (400, quoted message) |

### rest/InterVcpControllerTest (1)
| givenSuccessfulRequest_whenReceivingInterimValues_thenStoreValuesInCache | `TestInterVcpController` (202, PutInterim called once with the object) |

### rest/IntraVcpControllerTest (3)
| givenArgumentIsNull_whenUploadSecretShare | `TestIntraVcpController/givenArgumentIsNull...` (body `null` -> 400) |
| givenSuccessfulRequest_whenUploadSecretShare | `TestIntraVcpController/givenSuccessfulRequest_whenUploadSecretShare...` (201 + URI) |
| givenSuccessfulRequest_whenDownloadSecretShare | `TestIntraVcpController/givenSuccessfulRequest_whenDownloadSecretShare...` (+ missing/empty programId -> 400) |

### rest/MaskedInputControllerTest (3)
| givenArgumentIsNull_whenUpload | `TestMaskedInputController/givenArgumentIsNull...` |
| givenMaskedInputDataIsEmpty_whenUpload | `.../givenMaskedInputDataIsEmpty...` (400 `"MaskedInput data must not be empty"`) |
| givenSuccessfulRequest_whenUpload | `.../givenSuccessfulRequest_whenUpload...` (201 + URI, user id from JWT passed on) |

### rest/SecretShareControllerTest (12)
| 4x getObjectList (with/without filter, with/without paging) | `TestListController` cases withoutFilterAndWithoutPaging, withoutFilterButWithPaging, withFilterAndWithoutPaging, withFilterButWithPaging (+ explicit zero paging) |
| getSecretShare request id missing | `TestGetSecretShareController/givenRequestIdArgumentIsNull...` |
| getSecretShare success | `TestGetSecretShareController/givenSuccessfulRequest_whenGetSecretShare...` |
| deleteSecretShare success | `TestGetSecretShareController/givenSuccessfulRequest_whenDeleteSecretShare...` |
| getSort x3 (invalid direction -> ASC, no property -> unsorted, valid DESC) | `TestListController` sort cases (ListParams.SortKey/SortDesc) |
Note: the empty-page numbers asserted in Java (totalPages 1) are a property of the mocked `PageImpl`; the real
values (unpaged empty list -> totalPages 0, size Integer.MAX_VALUE) are asserted in `TestSystemListEmpty`.

### rest/TagsControllerTest (11)
All eleven methods (getTags, createTag null/success, updateTags empty/success, getTag, putTag null/key mismatch/success,
deleteTag) -> `TestTagsController/*` (same names). Extra: `TestMissingTokenIsUnauthorized` (401 `No token provided`).

### service/AmphoraServiceSystemTest (10)
All ten methods run in-process (httptest + real Postgres/Redis/Minio containers `ampA-*`, fake OPA), file internal/httpapi/system_test.go:
| givenMultipleVCPartnersDefined_whenInitialized | internal/config/bigint_test.go `TestMultipleVCPartnersDefined` |
| whenPostSecretShares | `TestSystemPostSecretShares` |
| whenListSecrets | `TestSystemListSecrets` |
| ...NumberComparisonFilter / StringEqualFilter / StringAndNumberComparisonFilters | `TestSystemListSecretsWithFilters` |
| ...NPaginationConfig | `TestSystemListSecretsWithPagination` |
| whenDeleteSecret | `TestSystemDeleteSecret` |
| givenUnknownSecretId_whenDeleteSecretShare | `TestSystemDeleteUnknownSecretDoesNotTouchOthers` |
| ...NSorting | `TestSystemListSecretsWithSorting` |
Extras: `TestSystemListEmpty`, `TestSystemTagLifecycleAndAuthorization`. Tests skip when docker is unavailable.

### service/calculation/OutputDeliveryServiceTest (6)
internal/service/odo_test.go: `TestMultiplySharesFetchingTriplesFails`, `...DistributingValuesFails`, `...ReceivingAllOpenedSharesFails`,
`TestMultiplySharesSuccess`, `TestComputeODOCastorDoesNotReturnInputMasks`, `TestComputeODOSuccess` (+ `TestOperationIDDerivation`).

### service/calculation/SecretShareUtilTest (2)
`TestConvertToSecretShare/*` (both methods, same vectors) + `TestConvertToSecretShareRejectsWrongLength`.

### service/config/BigIntegerConverterTest (3)
internal/config/bigint_test.go `TestBigIntegerConverter` (valid, empty, null == unset variable). `config.ParseBigInteger` added.

### service/opa/JwtReaderTest (5)
internal/jwt/jwtreader_test.go (`TestGivenTokenProvided_whenExtractSubject_thenReturnSubject`, `TestUnauthorizedCases` with the four unauthorized cases); also covered by jwt_test.go.

### service/opa/OpaClientTest (6) and OpaServiceTest (9) and OpaResultTest (2)
internal/opa/opa_test.go: request URL/body/headers (`TestEvaluateUsesPolicyPackageAndSendsSubjectAndTags`), default vs tag policy package
(`TestPolicyPackageSelection`, OpaServiceTest policy tests), false result, missing subject/action (`TestNoSubjectOrAction`),
transport failure -> denied (`TestClientFailureMeansDenied`), OpaResult parsing (`TestOpaResultParsing`), per-action path names
(`TestActionNames`, replaces the seven `whenCan*_thenUseProperAction`). The per-method action wiring inside `service.Service` is
asserted end to end in `TestSystemDeleteSecret`/`TestSystemTagLifecycleAndAuthorization`.
OpaClientTest.givenValidRequest/default-package merge OpaService.isAllowed's package selection because the Go `opa.Client.Allowed` does both.

## amphora-common

### AmphoraServiceUriTest (15)
client/amphora_uri_test.go: `TestServiceAddressValidation` (null/empty/no scheme/invalid), `TestServiceAddressForms` (domain, ip+port,
no port, path, port+path, trailing slash), `TestResourceURIs` (secret shares, masked inputs, input masks, tags, tag, intra/inter-VCP URIs for
four base forms), `TestPathParameterIsAppendedToBase`. Java `AmphoraServiceUri` itself has no Go type (URIs are plain string joins).
Messages differ from Java (see "Unresolved").

### exceptions/AmphoraClientExceptionTest (1)
client/exception_uri_test.go `TestClientErrorMessageAndCause`. n/a: `getUriStatusMap` (per-endpoint failures are folded into the message).

### entities/* (MaskedInputData 1, MaskedInput 2, Metadata 2, OutputDeliveryObject 5, SecretShare 5, TagFilter 4, Tag 15, VerifiableSecret 4)
internal/model/entities_test.go:
* Tag (15): `TestTagBuilding/*` (all, incl. null key/value -> binding error; TagFilter JSON round trip is n/a: Go parses filters only from query strings).
* TagFilter (4): `TestTagFilterFromString`.
* SecretShare (5): `TestSecretShareParsing/*`.  Metadata (2): `TestMetadata/*`.  MaskedInput (2): `TestMaskedInputEntity/*`.
* MaskedInputData (1): server-side behaviour only (`TestMaskedInputEntity/masked input data values keep their raw length`,
  service `TestConvertToSecretShareRejectsWrongLength`); the `MaskedInputData.of` factory is client-side (n/a, Go client always emits 16 bytes).
* OutputDeliveryObject (5): JSON shape `TestOutputDeliveryObjectJSON`. n/a: the equal-length constructor checks (the Go server only builds ODOs
  from equal-length arrays; the Go client's `verify` rejects unequal lengths with `IntegrityError`).
* VerifiableSecret (4): serialization `TestVerifiableSecretShareJSON` (compact flat JSON; the Java pretty printing is Jackson formatting).
  Deserialization / missing-field exceptions are client concerns (client package, agent B): n/a here.

## Skipped / n/a summary
* Spring-/Mockito-internal: `@InjectMocks` wiring, `ServletUriComponentsBuilderUtil`, testconfig containers (replaced by ampA-* helpers).
* Java-only validation in client-side builders (listed above).

## Part B - persistence, storage service, client

Java test class / group -> Go test that covers it. Container based tests use testcontainers-go
containers (postgres:11.1 like `ReusablePostgreSQLContainer`, redis:7.0, minio RELEASE.2022-04-16)
started lazily per package by `internal/testkit/docker.go` (terminated in `TestMain`; testcontainers' reaper removes them if the run is aborted);
they SKIP when docker is unavailable. The Java Mockito tests assert on interactions with mocked
repositories/clients; in Go the real stores are used and the same facts are asserted on the observable
result (rows, objects, keys, error kind and message). OPA is an httptest server that records each
evaluation (subject, action, package, tags).

## amphora-service

### persistence/cache

| Java | Go |
|---|---|
| InputMaskStoreTest: putInputMasks (stores `<store>::<requestId>`), getInputMasks, not in cache, removeInputMasks | `rediscache`: TestPutInputMasksKeyAndValue, TestGetInputMasksReadsJavaValue, TestGetInputMasksAbsent, TestDeleteInputMasks; `service`: TestInputMasksStoredInCacheAndODOReturned |
| InputMaskStoreRedisIT: no data -> exception, same result on repeated get, consecutive calls replace, remove | `rediscache`: TestGetInputMasksAbsent, TestGetInputMasksIsRepeatable, TestPutInputMasksReplacesExisting, TestDeleteInputMasks; `service`: TestInputMasksConsecutiveCallsReplaceCache; "No InputMaskList available" message: `service` TestCreateSecretWithoutCachedInputMasks |
| InterimValueCachingServiceTest: put with key `<store>::<op>_<player>`, get-and-evict returns values, absent -> none without evict | `rediscache`: TestPutInterimKeyAndValue, TestGetAndEvictInterim, TestGetAndEvictInterimAbsent (+ unreadable value, null operation id, prefixes) |
| Java serialization of the cached values | already covered by `javaser/redis_test.go` golden tests (real Java bytes); added `javaser/redis_roundtrip_test.go` (random round trips) |

### persistence/datastore: MinioSecretShareDataStoreTest (7)

All in `objstore/objstore_test.go`: instantiation failure (TestNewFailsWhenBucketCannotBeCreated, `InitError`
"Failed initializing MinoSecretShareDataStore." with cause), bucket creation (TestNewCreatesBucketAndIsIdempotent),
store (object name = id, application/octet-stream: TestPutWritesExpectedObject), get (TestGetReturnsStoredContent),
unknown id (TestGetUnknownIDFails, S3 text "The specified key does not exist."), store failure
(TestPutFailureIsReported), delete (TestRemoveDeletesObject), delete failure (TestRemoveFailureIsReported).
Note: `deleteSecretShareData` (get + remove) is split in Go into `Get` + `Remove` called by `Service.DeleteSecret`.

### persistence/metadata

StorageServiceTest (50 tests), all in `internal/service/storage*_test.go`:

| Java group | Go |
|---|---|
| createSecret x4 (already exists, duplicate keys, reserved key replaced, success) | storage_test.go: TestCreateSecretIDAlreadyInUse, TestCreateSecretDuplicateTagKeys, TestCreateSecretPersistsSecretTagsDataAndRemovesMasks (3 sub tests: plain / reserved creation-date / reserved owner; checks 3 tags, creation date in range, owner, stored share = mask + input, masks removed), + TestCreateSecretCreationDateUsesCurrentTime, ...WithoutCachedInputMasks, ...MoreDataThanMasks, ...DataStoreFailureRollsBack, TestCreateSecretThenReadBack |
| storeSecretShare x4 | TestStoreSecretShareIDAlreadyInUse, ...DuplicateTagKeys, ...ReservedTags (creation-date replaced, owner kept, success returns id), ...WithoutTagsIsRejected (NPE), ...DataStoreFailureRollsBack |
| getSecretList x4 (pageable, sort, filter+pageable, filter+sort) | TestListUnpaged, TestListWithSortAndFilter, TestListPaging, TestFilterSortAndPagingCombined |
| getSecretShare x4, useSecretShare x4 | TestGetAndUseSecretShare (not found, no access, data cannot be retrieved, success incl. OPA action/subject/tags), TestPolicyPackageFromTag |
| deleteSecret x4 | TestDeleteSecret (not found, no access, data deletion fails -> rollback, success) |
| storeTag x5 | TestStoreTagReservedKey, TestStoreTagUnknownSecret, TestStoreTagAlreadyExists, TestStoreTagNoAccess, TestStoreTagPersistsAndReturnsKey, TestStoreTagValueExceedingColumnWidth (SPEC H8) |
| replaceTags x5 | TestReplaceTagsDuplicateKeys, ...UnknownSecret, ...NoAccess, ...KeepsExistingReservedTags, ...WithoutReservedTags, ...JavaCompatBug (README divergence) |
| retrieveTags x3, retrieveTag x4 | TestRetrieveTagsUnknownSecret, ...NoAccess, TestRetrieveTags, TestRetrieveTagsOfSecretWithoutTags, TestRetrieveTagErrors, TestRetrieveTag |
| updateTag x6, deleteTag x6 | TestUpdateTagReservedKey, TestUpdateTagErrors, TestUpdateTag, TestDeleteTagReservedKey, TestDeleteTagErrors, TestDeleteTag |

StorageIT (14): TestStoreTagPersistsAndReturnsKey, TestListSingleSecretWithUnsortedConfig,
TestRetrieveTagsUnknownSecret, TestStoreTagUnknownSecret, TestRetrieveTagsOfSecretWithoutTags,
TestGetSecretShareWithoutTagsReturnsEmptyList, TestGetSecretShareWithoutDataPersisted (S3 message),
TestReplaceTagsUnknownSecret, TestDeleteTag (both delete tests), TestDeleteTagErrors (unknown key).

FilterPagingAndNestedSortingIT (15): storage_list_test.go: TestFilterOnlyOneMatching, TestFilterNoneMatching,
TestFilterMultipleMatching, TestFilterLongComparisons (long less/greater, mixed value types, bound
excluded), TestNumberComparisonFilterWithNonParsableValue (via model.ParseTagFilter), TestSortByLongTagValue
(asc/desc), TestSortByStringTagValue (asc/desc, database collation of postgres:11.1), TestSortWithMixedValueTypes,
TestSortByUnknownKeyActsUnsorted.

Store level (no Java counterpart, new): `pgstore/pgstore_test.go` (schema/column/FK/sequence names, tag CRUD,
DeleteTags, transactions, constraints incl. varchar(255), DSN escaping).

## amphora-java-client (Go: `client/`, new files `client_*_test.go`)

| Java | Go |
|---|---|
| AmphoraCommunicationClientTest (11) | client_comm_test.go: TestComm* (download/upload/update/delete, one and multiple players, forwarded and per-service errors, bearer token per service). The Java generic `Try<T>` map API maps to `base.do/all` results. |
| DefaultAmphoraClientBuilderTest (5) | client_default_test.go: TestBuilderValidation (4 error cases with the Java messages), TestNewUsesAllConfiguration, TestTLSConfiguration (no SSL validation, trusted certificates). `addEndpoint/endpoints` list merging has no Go counterpart (plain slice in `Options`). |
| DefaultAmphoraClientTest (25) | client_default_test.go: builder config / invalid cert (TestNewUsesAllConfiguration, TestTLSConfiguration), TestCreateSecretRecoversInitialData (25 random sizes instead of 100), TestCreateSecretUploadFails, TestGetSecretReturnsRecombinedContent, TestGetSecretOnePlayerFails, TestListingConstructsExpectedRequest (10 cases: exact Java query strings), TestGetTags, TestGetTagFailure, TestGetTag, TestCreateTag(+Failure), TestOverwriteTags, TestUpdateTag, TestDeleteTag, TestDeleteSecretFailure; extras: TestCreateSecretInputMaskDownloadFails, TestGetSecretDetectsTamperedShares, TestListingFailure, TestDeleteSecret |
| DefaultAmphoraInterVcpClientTest (5) | client_vcp_test.go: TestInterClientNoServiceURI, ...Open, ...OnePlayerUnreachable, ...OnePlayerRejects, ...MissingOperationID |
| DefaultAmphoraIntraVcpClientTest (5) | client_vcp_test.go: TestIntraClientNoServiceURI, ...UploadSecretShare, ...GetSecretShare, ...GetUnknownSecretShare; n/a: "givenIdIsNull" (a Go `uuid.UUID` cannot be null) |
| SecretShareUtilTest (2) | client_vcp_test.go: TestVerifySecretsAllValid, TestVerifySecretsInvalidW (message "Verification of secret has failed:..."); existing client_test.go TestVerifyRecombinesAndDetectsTampering |
| spdz (MpSpdzIntegrationUtils) | spdz_test.go (golden vectors from the Java jar, existing) + spdz_extra_test.go (random round trips, messages, nameUUIDFromBytes) |
| LiveGoServiceIT | out of scope |

## Part C - additional unit, property, fuzz and contract tests

Tests that go beyond the Java suites (new files, no Java counterpart). Paths relative to `go/`.

| File | Covers |
|---|---|
| internal/pgstore/pgstore_query_test.go | every query against a real Postgres: tag filters `:` `<` `>` on STRING/LONG values (incl. non-numeric values -> 22P02), sorting asc/desc with ties and missing tags, mixed-type sort error, paging boundaries (past the end, size 1, larger than total, unpaged, size 0), delete/replace tags incl. the nil keep-list regression, unique/FK/length violations, 255/256 char keys and values, unicode, NUL bytes, NULL columns, concurrent inserts of one secret id, closed pool errors |
| spdz/spdz_property_test.go | `testing/quick` and seeded round trips of ToGfp/FromGfp, edge values (0, 1, p-1, p, p+1, 2^128-1: p is accepted and decodes to 0, larger values are rejected with the Java message), limb helpers, additive sharing with MAC relation over wire bytes |
| internal/service/odo_property_test.go | Beaver multiplication with 2..5 real `ODO` parties wired through in-memory interim stores: reconstructed products equal s*r and v*r, MAC relation of masks/triples, single bit flips in secret/mask/triple shares and MACs are detected, opening orders (peer first, self first, simultaneous, delayed), timeout when a peer never delivers, eviction after read |
| internal/rediscache/cache_concurrent_test.go | interim values of two players in both orders and concurrently against real Redis; each value consumed exactly once by competing readers |
| internal/service/storage_concurrency_test.go | concurrent createSecret of one id (one winner, loser gets AlreadyExists or the constraint error), concurrent StoreTag/UpdateTag/ReplaceTags, delete while reading, concurrent deletes |
| internal/model/fuzz_test.go, internal/javaser/fuzz_test.go, internal/jwt/fuzz_test.go | fuzz targets: MaskedInput/SecretShare/ExchangeObject/Tag JSON parsers, tag filters, URLDecode, javaser decoders (never panic; round trip of encoder output), JWT claim extraction. HTTP-facing parsers must return 4xx class errors. Seed corpora and minimized crashers are in `testdata/fuzz/` and run as ordinary tests |
| internal/httpapi/api_table_test.go | method matrix (405 + Allow) and 404 for every route, malformed query parameters, missing/invalid Authorization, content types, 4xx for malformed bodies, error mapping of every service error kind for every endpoint, panic recovery, health with each dependency down, info, Prometheus labels |
| internal/opa/opa_failure_test.go | OPA 4xx/5xx, malformed/empty/odd JSON, slow header and slow body (context timeout), connection refused/reset, DNS failure: always "denied", never a hang |
| internal/config/config_matrix_test.go | every variable: defaults, parsing, missing/invalid values with the exact error text, validation order |
| client/contract_test.go | the public client against the real in-process `httpapi` handlers of 1..3 parties with an in-memory backend: create/get/delete secret (incl. integrity failure and a party down), tags, list/filter/sort/page, intra and inter VCP. Pins one inherited wire mismatch: `IntraVcpClient.GetSecretShare` sends no `programId`, the server answers 400 |
| chart/chart_test.go | `helm template`/`helm lint` of `amphora-service/charts/amphora` (skipped without helm): env vars, ports, securityContext, probes, service type/annotations, db name, compat flag, image, kubeVersion |

Fuzzing: `go test -run='^$' -fuzz='^FuzzParseMaskedInput$' -fuzztime=20s ./internal/model` (same for the other targets; use
`-parallel 2` on small machines). CI runs every target for 15 s on pull requests (see `.github/workflows/go.build-and-test.yaml`),
and checks the total statement coverage against a floor with `scripts/check-coverage.sh`.

Defects found by these tests and fixed: (1) `parseBigNumber` allocated gigabytes for `1e999999999` (fuzz crasher in
`internal/model/testdata/fuzz/FuzzParseExchangeObject`); (2) the Java serialization reader could loop forever on a class
descriptor whose super chain refers to itself, recurse without bound and allocate by an attacker chosen array length
(three crashers in `internal/javaser/testdata/fuzz`); (3) concurrent `POST /tags` with the same key could insert duplicate
tag rows (check-then-insert, `tag_key` is not unique): the secret row is now locked in the tag transactions
(`pgstore.LockSecret`); (4) a panic in a handler or backend aborted the connection instead of answering 500.

## n/a / differences

- StorageServiceTest `givenDeleteObjectDataFails_whenDeleteObject` stubs `deleteSecretShareData` to throw; with a real MinIO the
  failure that can be provoked is the preceding read (message `GetDataMsg`), the `DeleteDataMsg` branch (remove fails after a
  successful read) is only covered for the store (objstore TestRemoveFailureIsReported).
- Mockito `verify(..., times(n))` interaction counts are replaced by state assertions.
- Raw URL order of query parameters of the client now follows the Java client (filter, pageNumber, pageSize, sort, dir).
