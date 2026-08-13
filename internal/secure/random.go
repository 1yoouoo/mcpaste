package secure

import (
	"crypto/rand"
	"errors"
	"io"
)

var errInvalidRandom = errors.New("random source is invalid")

type Random interface {
	Read([]byte) (int, error)
}

type SystemRandom struct{}

func (SystemRandom) Read(target []byte) (int, error) {
	return rand.Read(target)
}

func randomBytes(source Random, size int) ([]byte, error) {
	if source == nil {
		return nil, errInvalidRandom
	}
	value := make([]byte, size)
	if _, err := io.ReadFull(source, value); err != nil {
		return nil, err
	}
	return value, nil
}
