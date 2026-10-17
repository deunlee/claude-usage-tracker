//go:build separatetest

package claude

import "testing"

func TestCredentialStatusReadsBothExpiries(t *testing.T) {
	credentials, err := credentialsFromFileContent(map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "sk-test", "expiresAt": 1000.0, "refreshTokenExpiresAt": 2000.0},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	status := credentials.Status()
	if status.ExpiresAt == nil || *status.ExpiresAt != 1000 || status.RefreshTokenExpiresAt == nil || *status.RefreshTokenExpiresAt != 2000 {
		t.Errorf("status %+v", status)
	}
}

func TestCredentialStatusWithoutRefreshTokenExpiry(t *testing.T) {
	credentials, err := credentialsFromFileContent(map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "sk-test", "expiresAt": 1000.0},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if status := credentials.Status(); status.RefreshTokenExpiresAt != nil {
		t.Errorf("refreshTokenExpiresAt = %v, want nil", *status.RefreshTokenExpiresAt)
	}
}
