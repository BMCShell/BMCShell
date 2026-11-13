package settings

// Source is a configured software source. The catalog validates its URL and TLS policy.
type Source struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Official    bool   `json:"official,omitempty"`
	InsecureTLS bool   `json:"insecureTls,omitempty"`
}
