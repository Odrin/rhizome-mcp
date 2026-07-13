// Package pagination provides project-independent cursor encoding.
package pagination

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// CurrentVersion is the cursor envelope version emitted by Codec.
	CurrentVersion = 1
	// DefaultMaxCursorBytes bounds encoded cursor input before allocation-heavy decoding.
	DefaultMaxCursorBytes = 4096
)

var (
	// ErrCursorTooLarge identifies cursor input beyond the configured bound.
	ErrCursorTooLarge = errors.New("cursor is too large")
	// ErrMalformedCursor identifies invalid base64url or JSON cursor data.
	ErrMalformedCursor = errors.New("malformed cursor")
	// ErrUnsupportedVersion identifies an unknown cursor envelope version.
	ErrUnsupportedVersion = errors.New("unsupported cursor version")
)

type envelope[T any] struct {
	Version int `json:"version"`
	Payload T   `json:"payload"`
}

// Codec encodes and decodes a typed payload in a versioned JSON envelope.
type Codec[T any] struct {
	maxCursorBytes int
}

// NewCodec returns a cursor codec. A non-positive bound uses DefaultMaxCursorBytes.
func NewCodec[T any](maxCursorBytes int) Codec[T] {
	if maxCursorBytes <= 0 {
		maxCursorBytes = DefaultMaxCursorBytes
	}
	return Codec[T]{maxCursorBytes: maxCursorBytes}
}

// Encode returns deterministic unpadded base64url of a versioned JSON envelope.
func (c Codec[T]) Encode(payload T) (string, error) {
	raw, err := json.Marshal(envelope[T]{Version: CurrentVersion, Payload: payload})
	if err != nil {
		return "", fmt.Errorf("encode cursor JSON: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > c.maxCursorBytes {
		return "", ErrCursorTooLarge
	}
	return encoded, nil
}

// Decode validates and decodes an unpadded base64url cursor.
func (c Codec[T]) Decode(cursor string) (T, error) {
	var zero T
	if len(cursor) > c.maxCursorBytes {
		return zero, ErrCursorTooLarge
	}
	if cursor == "" {
		return zero, ErrMalformedCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return zero, fmt.Errorf("%w: base64url: %v", ErrMalformedCursor, err)
	}

	var value envelope[T]
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("%w: JSON: %v", ErrMalformedCursor, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return zero, fmt.Errorf("%w: JSON: %v", ErrMalformedCursor, err)
	}
	if value.Version != CurrentVersion {
		return zero, fmt.Errorf("%w: %d", ErrUnsupportedVersion, value.Version)
	}
	return value.Payload, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
