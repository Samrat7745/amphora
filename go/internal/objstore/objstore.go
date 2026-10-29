// Package objstore wraps the Minio bucket holding secret share data (object key = secret UUID).
package objstore

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Store is the Minio-backed share store.
type Store struct {
	client *minio.Client
	bucket string
}

// New connects to endpoint (e.g. http://minio:9000) and ensures the bucket exists.
func New(ctx context.Context, endpoint, bucket, accessKey, secretKey string) (*Store, error) {
	host, secure := endpoint, false
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		host, secure = u.Host, u.Scheme == "https"
	}
	c, err := minio.New(host, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: secure})
	if err != nil {
		return nil, err
	}
	s := &Store{client: c, bucket: bucket}
	ok, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !ok {
		if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Put stores data under id.
func (s *Store) Put(ctx context.Context, id string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, id, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{})
	return err
}

// Get reads the full object.
func (s *Store) Get(ctx context.Context, id string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, id, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

// Remove deletes the object.
func (s *Store) Remove(ctx context.Context, id string) error {
	return s.client.RemoveObject(ctx, s.bucket, id, minio.RemoveObjectOptions{})
}
