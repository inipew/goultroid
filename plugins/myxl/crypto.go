package myxl

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Default API constants for MyXL.
const (
	DefaultBaseAPIURL     = "https://api.myxl.xlaxiata.co.id"
	DefaultBaseCIAMURL    = "https://gede.ciam.xlaxiata.co.id"
	DefaultAPIKey         = "vT8tINqHaOxXbGE7eOWAhA=="
	DefaultBasicAuth      = "OWZjOTdlZDEtNmEzMC00OGQ1LTk1MTYtNjBjNTNjZTNhMTM1OllEV21GNExKajlYSUt3UW56eTJlMmxiMHRKUWIyOW8z"
	DefaultUA             = "myXL / 8.9.0(1202); com.android.vending; (samsung; SM-N935F; SDK 33; Android 13)"
	DefaultAxFPKey        = "18b4d589826af50241177961590e6693"
	DefaultAxDeviceID     = "a1b2c3d4e5f6g7h8"
	DefaultXDataKey       = "5dccbf08920a5527b99e222789c34bb7"
	DefaultAxAPISigKey    = "18b4d589826af50241177961590e6693"
	DefaultXAPIBaseSecret = "mU1Y4n1vBjf3M7tMnRkFU08mVyUJHed8B5En3EAniu1mXLixeuASmBmKnkyzVziOye7rG5nIekMdthensbQMcOJ6SLnrkGyfXALD7mrBC6vuWv6G01pmD3XlU5rT7Tzx"
	DefaultXVersionApp    = "8.9.0"
	DefaultAxDevice       = "samsung"
	DefaultAxDeviceModel  = "SM-N935F"
)

// DeriveIV generates a 16-byte ASCII hex IV from xtimeMs using SHA-256:
// hex(SHA256(str(xtime)))[0..16].
// SERVER-MANDATED: MyXL protocol derives IV deterministically from xtime.
func DeriveIV(xtimeMs int64) ([]byte, error) {
	strTime := strconv.FormatInt(xtimeMs, 10)
	hash := sha256.Sum256([]byte(strTime))
	ivHex := hex.EncodeToString(hash[:8]) // 8 bytes -> 16 hex characters
	return []byte(ivHex), nil
}

// PKCS7Pad adds PKCS#7 padding to data.
func PKCS7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - (len(data) % blockSize)
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padtext...)
}

// PKCS7Unpad removes PKCS#7 padding from data.
func PKCS7Unpad(data []byte, blockSize int) ([]byte, error) {
	length := len(data)
	if length == 0 || length%blockSize != 0 {
		return nil, errors.New("invalid padding: data length is not a multiple of block size")
	}
	padding := int(data[length-1])
	if padding == 0 || padding > blockSize || padding > length {
		return nil, errors.New("invalid padding: pad value out of range")
	}
	for i := length - padding; i < length; i++ {
		if data[i] != byte(padding) {
			return nil, errors.New("invalid padding: byte mismatch")
		}
	}
	return data[:length-padding], nil
}

// EncryptXData encrypts plaintext with AES-CBC and returns a URL-Safe Base64 string.
func EncryptXData(plaintext string, xtimeMs int64, xdataKey string) (string, error) {
	if len(xdataKey) == 0 {
		return "", errors.New("xdata_key is empty")
	}
	key := []byte(xdataKey)
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return "", fmt.Errorf("xdata_key must be 16, 24, or 32 bytes (got %d)", len(key))
	}

	iv, err := DeriveIV(xtimeMs)
	if err != nil {
		return "", fmt.Errorf("derive iv: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create aes cipher: %w", err)
	}

	padded := PKCS7Pad([]byte(plaintext), aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, padded)

	return base64.URLEncoding.EncodeToString(ciphertext), nil
}

// DecryptXData decrypts URL-safe or standard Base64 string using AES-CBC and returns plaintext.
func DecryptXData(xdata string, xtimeMs int64, xdataKey string) (string, error) {
	if len(xdataKey) == 0 {
		return "", errors.New("xdata_key is empty")
	}
	key := []byte(xdataKey)
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return "", fmt.Errorf("xdata_key must be 16, 24, or 32 bytes (got %d)", len(key))
	}

	ciphertext, err := decodeBase64Flexible(xdata)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("ciphertext length is not a multiple of AES block size")
	}

	iv, err := DeriveIV(xtimeMs)
	if err != nil {
		return "", fmt.Errorf("derive iv: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create aes cipher: %w", err)
	}

	plainPadded := make([]byte, len(ciphertext))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(plainPadded, ciphertext)

	plaintext, err := PKCS7Unpad(plainPadded, aes.BlockSize)
	if err != nil {
		return "", fmt.Errorf("unpad pkcs7: %w", err)
	}

	return string(plaintext), nil
}

// MakeXSignature generates HMAC-SHA512 signature for Engsel API requests.
func MakeXSignature(idToken, method, path string, sigTimeSec int64, baseSecret string) string {
	sigTimeStr := strconv.FormatInt(sigTimeSec, 10)
	keyStr := fmt.Sprintf("%s;%s;%s;%s;%s", baseSecret, idToken, method, path, sigTimeStr)

	mac := hmac.New(sha512.New, []byte(keyStr))
	mac.Write([]byte(idToken))
	mac.Write([]byte(";"))
	mac.Write([]byte(sigTimeStr))
	mac.Write([]byte(";"))

	return hex.EncodeToString(mac.Sum(nil))
}

// MakeAxAPISignature generates HMAC-SHA256 signature for CIAM API requests.
func MakeAxAPISignature(tsForSign, contact, code, contactType, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(tsForSign))
	mac.Write([]byte("password"))
	mac.Write([]byte(contactType))
	mac.Write([]byte(contact))
	mac.Write([]byte(code))
	mac.Write([]byte("openid"))

	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// GenerateDeviceFingerprint generates an AES-256-CBC encrypted device fingerprint with zero IV.
func GenerateDeviceFingerprint(msisdn, fpKey string) string {
	key := []byte(fpKey)
	if len(key) != 32 {
		return ""
	}

	plain := fmt.Sprintf("Xiaomi|M2012K11AG|en|1080x2400|GMT+07:00|192.168.1.100|1.0|Android 11|%s", msisdn)
	iv := make([]byte, aes.BlockSize) // SERVER-MANDATED zero IV

	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}

	padded := PKCS7Pad([]byte(plain), aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, padded)

	return base64.StdEncoding.EncodeToString(ciphertext)
}

// FormatMyXLHeaderTS formats a time.Time into the Java-like ISO format expected by MyXL CIAM & Engsel:
// `YYYY-MM-DDTHH:mm:ss.cs+07:00` (where cs is centiseconds / 2-digit).
func FormatMyXLHeaderTS(t time.Time) string {
	wib := time.FixedZone("WIB", 7*3600)
	tWIB := t.In(wib)
	cs := tWIB.Nanosecond() / 10_000_000
	return fmt.Sprintf("%s.%02d%s", tWIB.Format("2006-01-02T15:04:05"), cs, tWIB.Format("-07:00"))
}

func decodeBase64Flexible(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	// Try URL-safe with padding
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	// Try URL-safe without padding
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	// Try Standard with padding
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	// Try Standard without padding
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}

	// Try adding padding if missing
	if padMod := len(s) % 4; padMod != 0 {
		s += strings.Repeat("=", 4-padMod)
		if b, err := base64.URLEncoding.DecodeString(s); err == nil {
			return b, nil
		}
		if b, err := base64.StdEncoding.DecodeString(s); err == nil {
			return b, nil
		}
	}

	return nil, errors.New("failed to decode base64 in any known encoding")
}
