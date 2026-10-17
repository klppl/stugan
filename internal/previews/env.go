package previews

import (
	"os"
	"strings"
)

// Env looks up a setting by its full environment-variable name.
type Env func(key string) string

// OSEnv reads the process environment. For any KEY it also honours KEY_FILE,
// whose value is a path to a file holding the secret (Docker/Kubernetes
// secrets), so API keys needn't sit in the container's environment.
func OSEnv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if path := os.Getenv(key + "_FILE"); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// Setting reads provider settings from the environment under a fixed
// namespace: STUGAN_PREVIEWS_<PROVIDER>_<NAME>, e.g.
// STUGAN_PREVIEWS_YOUTUBE_API_KEY. Adding a provider that needs a key means
// calling Setting(env, "reddit", "CLIENT_SECRET"); nothing else to register.
func Setting(env Env, provider, name string) string {
	return strings.TrimSpace(env("STUGAN_PREVIEWS_" + strings.ToUpper(provider) + "_" + strings.ToUpper(name)))
}
