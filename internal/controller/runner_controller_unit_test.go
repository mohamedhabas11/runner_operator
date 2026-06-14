package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestCheckSecretHasKeys_allPresent(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"ssh-privatekey": []byte("key-content"),
			"known_hosts":    []byte("host-key"),
		},
	}
	if err := checkSecretHasKeys(secret, "git-ssh", []string{"ssh-privatekey"}); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheckSecretHasKeys_missingKey(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"username": []byte("user"),
		},
	}
	if err := checkSecretHasKeys(secret, "git-basic", []string{"username", "password"}); err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
}

func TestCheckSecretHasKeys_emptyKeys(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{}}
	if err := checkSecretHasKeys(secret, "git-secret", []string{}); err != nil {
		t.Fatalf("expected no error for empty key list, got: %v", err)
	}
}

func TestCheckSecretHasKeys_caseSensitive(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"TOKEN": []byte("value"),
		},
	}
	if err := checkSecretHasKeys(secret, "git-token", []string{"token"}); err == nil {
		t.Fatal("expected error for case-sensitive key mismatch (TOKEN != token)")
	}
}

func TestCheckSecretHasKeys_multipleMissing(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"irrelevant": []byte("data"),
		},
	}
	if err := checkSecretHasKeys(secret, "git-basic", []string{"username", "password"}); err == nil {
		t.Fatal("expected error when multiple keys are missing")
	}
}

func TestCheckSecretHasKeys_stringData(t *testing.T) {
	secret := &corev1.Secret{
		StringData: map[string]string{
			"password": "supersecret",
		},
	}
	if err := checkSecretHasKeys(secret, "git-basic", []string{"password"}); err != nil {
		t.Fatalf("expected no error for key in StringData, got: %v", err)
	}
}
