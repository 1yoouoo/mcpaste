package secure

import (
	"crypto/rand"
	"io"
)

type Random interface {
	Read([]byte) (int, error)
}

type SystemRandom struct{}

func (SystemRandom) Read(target []byte) (int, error) {
	return rand.Read(target)
}

func randomBytes(source Random, size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(source, value); err != nil {
		return nil, err
	}
	return value, nil
}
