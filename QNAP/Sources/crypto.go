package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// Config encryption.
//
// The whole settings file (channels, Twitch token, paths) is stored encrypted
// with AES-256-GCM. The key is derived from the admin password with PBKDF2
// (SHA-256, 210000 iterations) and a random per-file salt, so the password
// itself is never written anywhere and the file cannot be decrypted without it.
//
// On-disk layout:  4-byte magic "SD1:" | 16-byte salt | 12-byte GCM nonce | ciphertext

const (
	storeMagic = "SD1:"
	saltLen    = 16
	pbkdf2Iter = 210000
)

func deriveKey(password string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, 32)
}

func storeKey(password string) (key, salt []byte, err error) {
	salt = make([]byte, saltLen)
	if _, err = rand.Read(salt); err != nil {
		return nil, nil, err
	}
	key, err = deriveKey(password, salt)
	if err != nil {
		return nil, nil, err
	}
	return key, salt, nil
}

func blobSalt(enc []byte) []byte {
	if len(enc) < len(storeMagic)+saltLen {
		return nil
	}
	return enc[len(storeMagic) : len(storeMagic)+saltLen]
}

func pack(blob, key, salt []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nonce, nonce, blob, nil)
	out := make([]byte, 0, len(storeMagic)+saltLen+len(sealed))
	out = append(out, storeMagic...)
	out = append(out, salt...)
	out = append(out, sealed...)
	return out, nil
}

func unpack(enc []byte, key, salt []byte) ([]byte, error) {
	if len(enc) < len(storeMagic)+saltLen {
		return nil, fmt.Errorf("malformed config")
	}
	if string(enc[:len(storeMagic)]) != storeMagic {
		return nil, fmt.Errorf("not an encrypted StreamDVR config")
	}
	for i := range salt {
		if enc[len(storeMagic)+i] != salt[i] {
			return nil, fmt.Errorf("bad password")
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	body := enc[len(storeMagic)+saltLen:]
	if len(body) < gcm.NonceSize() {
		return nil, fmt.Errorf("malformed config")
	}
	nonce := body[:gcm.NonceSize()]
	ct := body[gcm.NonceSize():]
	open, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("bad password")
	}
	return open, nil
}
