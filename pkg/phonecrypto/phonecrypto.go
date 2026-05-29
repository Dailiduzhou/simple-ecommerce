package phonecrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

// deriveKey uses HKDF-SHA256 to derive a key of given length from the master secret.
func deriveKey(secret []byte, info string, length int) ([]byte, error) {
	r := hkdf.New(sha256.New, secret, nil, []byte(info))
	k := make([]byte, length)
	if _, err := io.ReadFull(r, k); err != nil {
		return nil, err
	}
	return k, nil
}

// HashPhone returns the HMAC-SHA256 digest of phone, used for equality matching (login lookup).
func HashPhone(phone string, secret []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(phone))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// EncryptPhone encrypts phone with AES-256-GCM, returning base64-encoded ciphertext.
func EncryptPhone(phone string, secret []byte) (string, error) {
	key, err := deriveKey(secret, "phone-encryption-aes-gcm", 32)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aesgcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := aesgcm.Seal(nonce, nonce, []byte(phone), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// EncryptEncoded is like EncryptPhone but prepends a short prefix for identification.
var EncryptEncoded = EncryptPhone

// DecryptPhone decrypts a base64-encoded ciphertext produced by EncryptPhone.
func DecryptPhone(encoded string, secret []byte) (string, error) {
	key, err := deriveKey(secret, "phone-encryption-aes-gcm", 32)
	if err != nil {
		return "", err
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := aesgcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, cipherdata := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plain, err := aesgcm.Open(nil, nonce, cipherdata, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
