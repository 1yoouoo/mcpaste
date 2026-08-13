package secure

import (
	"crypto/rand"
	"encoding/hex"
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

func NewUUID(source Random) (string, error) {
	value, err := randomBytes(source, 16)
	if err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
