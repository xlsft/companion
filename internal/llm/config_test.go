package llm

import (
	"crypto/tls"
	"errors"
	"net/http"
	"testing"
	"time"
)

func validConfig() ProviderConfig {
	return ProviderConfig{
		ID:        "p1",
		Name:      "OpenAI",
		Kind:      KindOpenAI,
		BaseURL:   "https://api.example.com/v1/",
		Model:     "gpt-x",
		SecretRef: "provider/p1/api_key",
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*ProviderConfig)
		field string // empty: valid
	}{
		{"valid", func(*ProviderConfig) {}, ""},
		{"default base url", func(c *ProviderConfig) { c.BaseURL = "" }, ""},
		{"anthropic", func(c *ProviderConfig) { c.Kind = KindAnthropic }, ""},
		{"no name", func(c *ProviderConfig) { c.Name = " " }, "Name"},
		{"bad kind", func(c *ProviderConfig) { c.Kind = "gemini" }, "Kind"},
		{"no model", func(c *ProviderConfig) { c.Model = "" }, "Model"},
		{"relative url", func(c *ProviderConfig) { c.BaseURL = "/v1" }, "BaseURL"},
		{"garbage url", func(c *ProviderConfig) { c.BaseURL = "::not a url" }, "BaseURL"},
		{"ftp", func(c *ProviderConfig) { c.BaseURL = "ftp://example.com" }, "BaseURL"},
		{"remote http", func(c *ProviderConfig) { c.BaseURL = "http://api.example.com/v1" }, "BaseURL"},
		{"public ip http", func(c *ProviderConfig) { c.BaseURL = "http://8.8.8.8/v1" }, "BaseURL"},
		{"localhost http", func(c *ProviderConfig) { c.BaseURL = "http://localhost:11434/v1" }, ""},
		{"loopback http", func(c *ProviderConfig) { c.BaseURL = "http://127.0.0.1:8080" }, ""},
		{"ipv6 loopback http", func(c *ProviderConfig) { c.BaseURL = "http://[::1]:8080" }, ""},
		{"private http", func(c *ProviderConfig) { c.BaseURL = "http://192.168.1.20:8000/v1" }, ""},
		{"private 10/8 http", func(c *ProviderConfig) { c.BaseURL = "http://10.0.0.5" }, ""},
		{"ula http", func(c *ProviderConfig) { c.BaseURL = "http://[fd00::1]:8000" }, ""},
		{"lan hostname http", func(c *ProviderConfig) { c.BaseURL = "http://gpu-box.lan:8000" }, "BaseURL"},
		{"userinfo", func(c *ProviderConfig) { c.BaseURL = "https://user:pw@api.example.com" }, "BaseURL"},
		{"fragment", func(c *ProviderConfig) { c.BaseURL = "https://api.example.com#x" }, "BaseURL"},
		{"custom header", func(c *ProviderConfig) { c.Headers = map[string]string{"X-Org": "acme"} }, ""},
		{"auth header", func(c *ProviderConfig) { c.Headers = map[string]string{"Authorization": "Bearer x"} }, "Headers"},
		{"api key header", func(c *ProviderConfig) { c.Headers = map[string]string{"X-Api-Key": "x"} }, "Headers"},
		{"bad header name", func(c *ProviderConfig) { c.Headers = map[string]string{"X Org": "a"} }, "Headers"},
		{"header injection", func(c *ProviderConfig) { c.Headers = map[string]string{"X-Org": "a\r\nX-Evil: 1"} }, "Headers"},
		{"negative timeout", func(c *ProviderConfig) { c.Timeout = -time.Second }, "Timeout"},
		{"negative retry", func(c *ProviderConfig) { c.Retry.MaxAttempts = -1 }, "Retry"},
		{"capability override", func(c *ProviderConfig) { c.Capabilities = map[Capability]bool{CapVision: false} }, ""},
		{"unknown capability", func(c *ProviderConfig) { c.Capabilities = map[Capability]bool{"teleport": true} }, "Capabilities"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.edit(&c)
			err := c.Validate()
			if tt.field == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var ce *ConfigError
			if !errors.As(err, &ce) || ce.Field != tt.field || !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Validate() = %v, want ConfigError on %s", err, tt.field)
			}
		})
	}
}

func TestEndpoint(t *testing.T) {
	tests := []struct {
		kind Kind
		base string
		want string
	}{
		{KindOpenAI, "", DefaultOpenAIBaseURL},
		{KindAnthropic, "", DefaultAnthropicBaseURL},
		{KindOpenAI, "https://x.example/v1///", "https://x.example/v1"},
	}
	for _, tt := range tests {
		c := validConfig()
		c.Kind, c.BaseURL = tt.kind, tt.base
		u, err := c.Endpoint()
		if err != nil || u.String() != tt.want {
			t.Errorf("Endpoint(%q) = %v, %v; want %s", tt.base, u, err, tt.want)
		}
	}
}

func TestNew(t *testing.T) {
	c := validConfig()
	c.Kind = "test-kind-unregistered"
	if _, err := New(c, "", nil); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("unknown kind: %v", err)
	}

	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if _, err := New(validConfig(), "", insecure); err == nil {
		t.Error("insecure transport accepted")
	}

	var got *http.Client
	registryMu.Lock()
	saved := registry[KindAnthropic]
	registry[KindAnthropic] = func(cfg ProviderConfig, key string, hc *http.Client) (Provider, error) {
		got = hc
		return &scriptedProvider{}, nil
	}
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		if saved == nil {
			delete(registry, KindAnthropic)
		} else {
			registry[KindAnthropic] = saved
		}
		registryMu.Unlock()
	})

	c = validConfig()
	c.Kind = KindAnthropic
	orig := &http.Client{}
	p, err := New(c, "k", orig)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*retrying); !ok {
		t.Errorf("provider not wrapped with retry: %T", p)
	}
	if got == orig || got.CheckRedirect == nil || orig.CheckRedirect != nil {
		t.Error("client must be copied with redirects disabled")
	}
	if got.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Error("redirects must not be followed")
	}

	registryMu.Lock()
	delete(registry, KindOpenAI)
	registryMu.Unlock()
	if _, err := New(validConfig(), "", nil); !errors.Is(err, ErrNoAdapter) {
		t.Errorf("missing adapter: %v", err)
	}
}
