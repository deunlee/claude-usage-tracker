package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"claudeusage/internal/util"
)

// 둘 중 하나라도 없으면 Claude Code는 사용량 요청을 아예 보내지 않는다.
var requiredScopes = []string{"user:profile", "user:inference"}

type CredentialStatus struct {
	ExpiresAt             *float64 `json:"expiresAt"`
	RefreshTokenExpiresAt *float64 `json:"refreshTokenExpiresAt"`
	SubscriptionType      *string  `json:"subscriptionType"`
	RateLimitTier         *string  `json:"rateLimitTier"`
	MissingScopes         []string `json:"missingScopes"`
}

type Credentials struct {
	// 구조체를 JSON으로 내보내도 토큰이 따라 나가지 않도록 내보내지 않는 필드에만 둔다. 로그 쪽은 String이 막는다.
	accessToken string
	Fingerprint string
	// 크리덴셜 파일의 키 이름을 따라 expiresAt이 액세스 토큰의 만료 시각이다. 두 만료 시각 모두 Unix 밀리초다.
	expiresAt             *float64
	refreshTokenExpiresAt *float64
	scopes                []string
	subscriptionType      *string
	rateLimitTier         *string
}

func credentialsFromFileContent(content any, source string) (*Credentials, error) {
	root, _ := content.(map[string]any)
	oauth, _ := root["claudeAiOauth"].(map[string]any)
	accessToken, _ := oauth["accessToken"].(string)
	if accessToken == "" {
		return nil, fmt.Errorf("%s에 claudeAiOauth.accessToken이 없습니다", source)
	}
	scopes := []string{}
	if list, ok := oauth["scopes"].([]any); ok {
		for _, scope := range list {
			if s, ok := scope.(string); ok {
				scopes = append(scopes, s)
			}
		}
	}
	digest := sha256.Sum256([]byte(accessToken))
	return &Credentials{
		accessToken:           accessToken,
		Fingerprint:           hex.EncodeToString(digest[:]),
		expiresAt:             util.Num(oauth["expiresAt"]),
		refreshTokenExpiresAt: util.Num(oauth["refreshTokenExpiresAt"]),
		scopes:                scopes,
		subscriptionType:      util.Str(oauth["subscriptionType"]),
		rateLimitTier:         util.Str(oauth["rateLimitTier"]),
	}, nil
}

func (c *Credentials) String() string {
	return "oauthCredentials(sha256:" + c.Fingerprint[:12] + ")"
}

func (c *Credentials) GoString() string {
	return c.String()
}

func (c *Credentials) authorization() string {
	return "Bearer " + c.accessToken
}

func (c *Credentials) MissingScopes() []string {
	missing := []string{}
	for _, scope := range requiredScopes {
		if !slices.Contains(c.scopes, scope) {
			missing = append(missing, scope)
		}
	}
	return missing
}

func (c *Credentials) ExpiresWithin(margin time.Duration, now time.Time) bool {
	return c.expiresAt != nil && float64(now.Add(margin).UnixMilli()) >= *c.expiresAt
}

func (c *Credentials) Status() *CredentialStatus {
	return &CredentialStatus{
		ExpiresAt:             c.expiresAt,
		RefreshTokenExpiresAt: c.refreshTokenExpiresAt,
		SubscriptionType:      c.subscriptionType,
		RateLimitTier:         c.rateLimitTier,
		MissingScopes:         c.MissingScopes(),
	}
}

type CredentialStore struct {
	path string
}

func (s CredentialStore) Read() (*Credentials, error) {
	text, err := util.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var content any
	if err := json.Unmarshal(text, &content); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return credentialsFromFileContent(content, s.path)
}
