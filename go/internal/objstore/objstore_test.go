package objstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/carbynestack/amphora-go/internal/testkit"
	"github.com/minio/minio-go/v7"
)

// Ports MinioSecretShareDataStoreTest (the Mockito based Java test is re-expressed against a real
// MinIO container, so the Minio client behaviour it stubs is observed instead of mocked).

var minioC = testkit.NewLazy(testkit.StartMinio)

func TestMain(m *testing.M) {
	code := m.Run()
	minioC.Close()
	os.Exit(code)
}

const testSecretID = "3bcf8308-8f50-4d24-a37b-b0075bb5e779"

func newStore(t *testing.T, bucket string) (*Store, context.Context) {
	t.Helper()
	ep := minioC.Get(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	s, err := New(ctx, "http://"+ep, bucket, testkit.MinioAccessKey, testkit.MinioSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// givenMakingMinioBucketFails_whenInstantiating...: construction fails when the bucket cannot be
// checked/created (here: endpoint not reachable; a bucket name MinIO rejects).
func TestNewFailsWhenBucketCannotBeCreated(t *testing.T) {
	ep := minioC.Get(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := New(ctx, "http://"+ep, "Invalid_Bucket!", testkit.MinioAccessKey, testkit.MinioSecretKey)
	var ie *InitError
	if !errors.As(err, &ie) || err.Error() != "Failed initializing MinoSecretShareDataStore." || ie.Cause == nil {
		t.Fatalf("want InitError wrapping the cause, got %v", err)
	}
	if _, err := New(ctx, "http://127.0.0.1:1", "b-unreachable", testkit.MinioAccessKey, testkit.MinioSecretKey); err == nil {
		t.Fatal("unreachable endpoint must fail")
	}
	if _, err := New(ctx, "http://"+ep, "b-wrong-creds", testkit.MinioAccessKey, "wrong-secret-key"); err == nil {
		t.Fatal("wrong credentials must fail")
	}
}

func TestNewCreatesBucketAndIsIdempotent(t *testing.T) {
	s, ctx := newStore(t, "amphora-bucket-new")
	ok, err := s.client.BucketExists(ctx, "amphora-bucket-new")
	if err != nil || !ok {
		t.Fatalf("bucket must exist: %v %v", ok, err)
	}
	if err := s.Put(ctx, testSecretID, []byte{1}); err != nil {
		t.Fatal(err)
	}
	// a second instantiation reuses the existing bucket (and its content)
	s2, _ := newStore(t, "amphora-bucket-new")
	got, err := s2.Get(ctx, testSecretID)
	if err != nil || !bytes.Equal(got, []byte{1}) {
		t.Fatalf("existing bucket content lost: %v %v", got, err)
	}
}

// givenSuccessfulRequest_whenStoreSecretShareData_thenWriteExpectedDataToMinio
func TestPutWritesExpectedObject(t *testing.T) {
	s, ctx := newStore(t, "amphora-bucket-put")
	data := randBytes(32 * 5)
	if err := s.Put(ctx, testSecretID, data); err != nil {
		t.Fatal(err)
	}
	// object name is the secret id, content type is application/octet-stream
	st, err := s.client.StatObject(ctx, "amphora-bucket-put", testSecretID, minio.StatObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.ContentType != "application/octet-stream" || st.Size != int64(len(data)) {
		t.Fatalf("unexpected object: %q size %d", st.ContentType, st.Size)
	}
	// an empty share is storable too (StorageIT: object without data)
	if err := s.Put(ctx, "00000000-0000-0000-0000-000000000001", nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "00000000-0000-0000-0000-000000000001")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty object: %v %v", got, err)
	}
}

// givenSuccessfulRequest_whenGetSecretShareData_thenReturnExpectedContent
func TestGetReturnsStoredContent(t *testing.T) {
	s, ctx := newStore(t, "amphora-bucket-get")
	data := randBytes(32 * 7)
	if err := s.Put(ctx, testSecretID, data); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, testSecretID)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("content mismatch: %v", err)
	}
}

// givenNoDataAvailableForGivenId_whenGetSecretShareData_thenThrowAmphoraServiceException: the message
// the service embeds into GetDataMsg is the Minio error text (StorageIT expects the S3 text).
func TestGetUnknownIDFails(t *testing.T) {
	s, ctx := newStore(t, "amphora-bucket-missing")
	_, err := s.Get(ctx, testSecretID)
	if err == nil || err.Error() != "The specified key does not exist." {
		t.Fatalf("want S3 NoSuchKey text, got %v", err)
	}
}

// givenMinioThrowsException_whenStoreSecretShareData_thenWrapInAmphoraServiceException
func TestPutFailureIsReported(t *testing.T) {
	s, ctx := newStore(t, "amphora-bucket-putfail")
	if err := s.client.RemoveBucket(ctx, "amphora-bucket-putfail"); err != nil {
		t.Fatal(err)
	}
	err := s.Put(ctx, testSecretID, []byte{1, 2, 3})
	if err == nil || !strings.Contains(err.Error(), "bucket does not exist") {
		t.Fatalf("want bucket error, got %v", err)
	}
}

// givenSuccessfulRequest_whenDeleteSecretShareData_thenReturnExpectedContent: the service reads
// the object (to fail on unknown ids) and removes it afterwards.
func TestRemoveDeletesObject(t *testing.T) {
	s, ctx := newStore(t, "amphora-bucket-rm")
	data := randBytes(64)
	if err := s.Put(ctx, testSecretID, data); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, testSecretID)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, testSecretID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, testSecretID); err == nil {
		t.Fatal("object must be gone")
	}
}

// givenDeleteCallOnMinioClientFails_whenDeleteSecretShareData_thenWrapExceptionInAmphoraServiceException
func TestRemoveFailureIsReported(t *testing.T) {
	s, ctx := newStore(t, "amphora-bucket-rmfail")
	if err := s.Put(ctx, testSecretID, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.client.RemoveObject(ctx, "amphora-bucket-rmfail", testSecretID, minio.RemoveObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.client.RemoveBucket(ctx, "amphora-bucket-rmfail"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, testSecretID); err == nil {
		t.Fatal("remove on a vanished bucket must fail")
	}
}
