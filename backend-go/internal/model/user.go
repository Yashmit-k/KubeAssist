package model

// User is a KubeAssist account. Users sign in through an identity
// provider (GitHub, or "dev" for local development) — KubeAssist never
// stores a password. Clusters registered through the relay are owned by
// exactly one user.
type User struct {
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	ProviderUserID string `json:"-"`
	Login          string `json:"login"`
	Name           string `json:"name,omitempty"`
	AvatarURL      string `json:"avatarUrl,omitempty"`
}
