package dscpkg

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

func verifyDetachedJWS(compact string, payload []byte, keys []JWK) error {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[1] != "" {
		return errors.New("signature is not a detached compact JWS")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("decode JWS header: %w", err)
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return fmt.Errorf("parse JWS header: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("decode JWS signature: %w", err)
	}
	signingInput := []byte(parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload))
	var failures []string
	for _, key := range keys {
		if header.KeyID != "" && key.Kid != header.KeyID {
			continue
		}
		if err := verifyJWSKey(header.Algorithm, key, signingInput, signature); err == nil {
			return nil
		} else {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) == 0 {
		return fmt.Errorf("no trusted key matched JWS key id %q", header.KeyID)
	}
	return fmt.Errorf("signature did not verify with a trusted key: %s", strings.Join(failures, "; "))
}

func verifyJWSKey(algorithm string, jwk JWK, input, signature []byte) error {
	switch algorithm {
	case "ES256":
		if jwk.Kty != "EC" || jwk.Crv != "P-256" {
			return errors.New("ES256 requires an EC P-256 key")
		}
		x, err := decodeInt(jwk.X)
		if err != nil {
			return err
		}
		y, err := decodeInt(jwk.Y)
		if err != nil {
			return err
		}
		curve := elliptic.P256()
		if !curve.IsOnCurve(x, y) {
			return errors.New("invalid EC public key point")
		}
		if len(signature) != 64 {
			return errors.New("ES256 signature must be 64 bytes")
		}
		pub := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
		digest := sha256.Sum256(input)
		if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
			return errors.New("ES256 verification failed")
		}
		return nil
	case "RS256", "PS256":
		if jwk.Kty != "RSA" {
			return fmt.Errorf("%s requires an RSA key", algorithm)
		}
		n, err := decodeInt(jwk.N)
		if err != nil {
			return err
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil || len(eBytes) == 0 || len(eBytes) > 4 {
			return errors.New("invalid RSA exponent")
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		digest := sha256.Sum256(input)
		pub := &rsa.PublicKey{N: n, E: e}
		if algorithm == "RS256" {
			err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], signature)
		} else {
			err = rsa.VerifyPSS(pub, crypto.SHA256, digest[:], signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256})
		}
		return err
	case "EdDSA":
		if jwk.Kty != "OKP" || jwk.Crv != "Ed25519" {
			return errors.New("EdDSA requires an Ed25519 key")
		}
		publicKey, err := base64.RawURLEncoding.DecodeString(jwk.X)
		if err != nil || len(publicKey) != ed25519.PublicKeySize {
			return errors.New("invalid Ed25519 public key")
		}
		if !ed25519.Verify(ed25519.PublicKey(publicKey), input, signature) {
			return errors.New("EdDSA verification failed")
		}
		return nil
	default:
		return fmt.Errorf("unsupported JWS algorithm %q", algorithm)
	}
}

func decodeInt(value string) (*big.Int, error) {
	bytes, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(bytes) == 0 {
		return nil, errors.New("invalid JWK integer")
	}
	return new(big.Int).SetBytes(bytes), nil
}
