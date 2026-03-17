package interp

import "testing"

func TestValidateHTTPHostPatternRejectsProtocols(t *testing.T) {
	t.Parallel()

	tests := []string{
		"https://api.example.com",
		"^https://api.example.com$",
		`^https?:\/\/api\.example\.com$`,
	}
	for _, input := range tests {
		if err := ValidateHTTPHostPattern(input); err == nil {
			t.Fatalf("ValidateHTTPHostPattern(%q) error = nil, want protocol rejection", input)
		}
	}
}

func TestNormalizeHostRegexPatternTrimsOptionalAnchors(t *testing.T) {
	t.Parallel()

	got, err := NormalizeHostRegexPattern("^payments-[a-z0-9-]+\\.example\\.com$")
	if err != nil {
		t.Fatalf("NormalizeHostRegexPattern() error = %v", err)
	}
	want := "^(?:payments-[a-z0-9-]+\\.example\\.com)$"
	if got != want {
		t.Fatalf("NormalizeHostRegexPattern() = %q, want %q", got, want)
	}
}

func TestNewHTTPClientFactoryRegexMatchersUseFullHostMatching(t *testing.T) {
	t.Parallel()

	factory, err := NewHTTPClientFactory(RejectAllNetworkDialer{}, []HTTPHeaderRule{{
		Name:  "Authorization",
		Value: "Bearer secret",
		Domains: []HTTPDomainMatcher{{
			Regex: "payments-[a-z0-9-]+\\.example\\.com",
		}},
	}})
	if err != nil {
		t.Fatalf("NewHTTPClientFactory() error = %v", err)
	}
	typedFactory, ok := factory.(*defaultHTTPClientFactory)
	if !ok {
		t.Fatalf("factory type = %T, want *defaultHTTPClientFactory", factory)
	}
	if !typedFactory.rules[0].matches("payments-abc.example.com") {
		t.Fatal("compiled regex matcher rejected matching host")
	}
	if typedFactory.rules[0].matches("other-payments-abc.example.com") {
		t.Fatal("compiled regex matcher accepted partial host match, want full-host match only")
	}
	if typedFactory.rules[0].matches("payments-abc.example.com.extra") {
		t.Fatal("compiled regex matcher accepted suffixed host match, want full-host match only")
	}
}

func TestNewHTTPClientFactoryRejectsGlobWithProtocol(t *testing.T) {
	t.Parallel()

	_, err := NewHTTPClientFactory(RejectAllNetworkDialer{}, []HTTPHeaderRule{{
		Name:  "Authorization",
		Value: "Bearer secret",
		Domains: []HTTPDomainMatcher{{
			Glob: "https://api.example.com",
		}},
	}})
	if err == nil {
		t.Fatal("NewHTTPClientFactory() error = nil, want protocol rejection")
	}
}
