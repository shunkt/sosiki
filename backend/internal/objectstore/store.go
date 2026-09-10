// Package objectstore reads knowledge-base source objects out of MinIO.
package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/shun/kaigi/backend/internal/config"
)

type Store struct {
	// client is what the backend dials — an in-cluster hostname like
	// "minio:9000" that the browser cannot resolve.
	client *minio.Client
	// presignClient is built against PublicEndpoint so links handed to the
	// frontend resolve outside the compose network.
	presignClient *minio.Client
	bucket        string
}

func New(cfg config.ObjectStoreConfig) (*Store, error) {
	creds := credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")

	client, err := minio.New(cfg.Endpoint, &minio.Options{Creds: creds, Secure: cfg.UseSSL})
	if err != nil {
		return nil, fmt.Errorf("objectstore: new client: %w", err)
	}

	presignClient := client
	if cfg.PublicEndpoint != cfg.Endpoint {
		presignClient, err = minio.New(cfg.PublicEndpoint, &minio.Options{Creds: creds, Secure: cfg.UseSSL})
		if err != nil {
			return nil, fmt.Errorf("objectstore: new presign client: %w", err)
		}
	}

	return &Store{client: client, presignClient: presignClient, bucket: cfg.Bucket}, nil
}

// ExpandContext reads the original object around a chunk's byte range so the
// model sees more than the embedded snippet — the chunk is the search unit,
// this is the reading unit. size is the object's total byte length, used to
// keep the range request from exceeding it.
func (s *Store) ExpandContext(ctx context.Context, key string, start, end, size int64, pad int) (string, error) {
	rangeStart := start - int64(pad)
	if rangeStart < 0 {
		rangeStart = 0
	}
	rangeEnd := end + int64(pad)
	if size > 0 && rangeEnd > size-1 {
		rangeEnd = size - 1
	}

	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(rangeStart, rangeEnd); err != nil {
		return "", fmt.Errorf("objectstore: set range: %w", err)
	}

	obj, err := s.client.GetObject(ctx, s.bucket, key, opts)
	if err != nil {
		return "", fmt.Errorf("objectstore: get object: %w", err)
	}
	defer obj.Close()

	buf, err := io.ReadAll(obj)
	if err != nil {
		return "", fmt.Errorf("objectstore: read object: %w", err)
	}

	// A byte range can slice a multi-byte rune in half; Japanese text makes
	// this the common case rather than the edge case.
	buf = bytes.ToValidUTF8(buf, nil)
	return trimToSentence(string(buf)), nil
}

// trimToSentence drops a leading/trailing partial sentence so the expanded
// context reads naturally instead of starting or ending mid-clause. "。" and
// "\n" have different byte widths, so the cut point must account for
// whichever one matched rather than assuming either width.
func trimToSentence(s string) string {
	if i := strings.IndexAny(s, "。\n"); i >= 0 {
		_, width := utf8.DecodeRuneInString(s[i:])
		if cut := i + width; cut < len(s) {
			s = s[cut:]
		}
	}
	if i := strings.LastIndexAny(s, "。\n"); i >= 0 {
		_, width := utf8.DecodeRuneInString(s[i:])
		s = s[:i+width]
	}
	return s
}

// PresignedURL returns a short-lived link the frontend can use to open the
// source document.
func (s *Store) PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.presignClient.PresignedGetObject(ctx, s.bucket, key, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("objectstore: presign: %w", err)
	}
	return u.String(), nil
}
