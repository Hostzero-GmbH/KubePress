package wordpress

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"golang.org/x/crypto/ssh"
)

// GenerateSSHHostKeys generates RSA and ED25519 SSH host keys and returns them as PEM-encoded byte slices.
func GenerateSSHHostKeys() (rsaKeyPEM []byte, ed25519KeyPEM []byte, err error) {
	// Generate RSA key
	rsaKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}
	// Marshal the RSA private key to OpenSSH format
	rsaKeyPEMBlock, err := ssh.MarshalPrivateKey(rsaKey, "none")
	if err != nil {
		return nil, nil, err
	}

	rsaBytes := pem.EncodeToMemory(rsaKeyPEMBlock)

	// Generate ED25519 key
	_, ed25519Priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate ED25519 key: %w", err)
	}
	// Marshal the ED25519 private key to OpenSSH format
	ed25519KeyPEMBlock, err := ssh.MarshalPrivateKey(ed25519Priv, "none")
	if err != nil {
		return nil, nil, err
	}

	ed25519Bytes := pem.EncodeToMemory(ed25519KeyPEMBlock)

	return rsaBytes, ed25519Bytes, nil
}

// containsString checks if a string is present in a slice
func ContainsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// removeString removes a string from a slice and returns the new slice
func RemoveString(slice []string, s string) []string {
	var result []string
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}

func GoMemoryToPHPMemory(goMem string) (string, error) {
	var value int
	var unit string
	_, err := fmt.Sscanf(goMem, "%d%s", &value, &unit)
	if err != nil {
		return "", fmt.Errorf("invalid memory format: %w", err)
	}

	switch unit {
	case "Ki":
		// Convert Ki to M (rounded down)
		value = value / 1024
	case "Mi":
		// Mi is already in M
	case "Gi":
		// Convert Gi to M
		value = value * 1024
	default:
		return "", fmt.Errorf("unsupported unit: %s", unit)
	}

	return fmt.Sprintf("%dM", value), nil
}
