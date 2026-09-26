package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		secret string // must not survive
		keep   string // must survive, if set
	}{
		{
			name:   "PEM private key",
			in:     "key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7s3cr3t\nmore\n-----END RSA PRIVATE KEY-----\ndone",
			secret: "MIIEpAIBAAKCAQEA7s3cr3t",
			keep:   "done",
		},
		{
			name:   "OpenSSH private key",
			in:     "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAA\n-----END OPENSSH PRIVATE KEY-----",
			secret: "b3BlbnNzaC1rZXktdjEAAAA",
		},
		{
			name:   "bearer token in a header",
			in:     `GET /api 401 Authorization: Bearer abcDEF123456ghiJKL789`,
			secret: "abcDEF123456ghiJKL789",
			keep:   "Authorization: Bearer [REDACTED:auth_header]",
		},
		{
			name:   "basic auth",
			in:     `Authorization: Basic dXNlcjpodW50ZXIy`,
			secret: "dXNlcjpodW50ZXIy",
		},
		{
			name:   "JWT on its own",
			in:     `token loaded eyJhbGciOiJSUzI1NiIsImtpZCI6IjEifQ.eyJpc3MiOiJrdWJlcm5ldGVzIn0.c2lnbmF0dXJlX2hlcmU`,
			secret: "eyJpc3MiOiJrdWJlcm5ldGVzIn0",
		},
		{
			name:   "GitHub token",
			in:     `cloning with ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789`,
			secret: "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
		},
		{
			name:   "Anthropic API key",
			in:     `ANTHROPIC_KEY sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123`,
			secret: "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123",
		},
		{
			name:   "AWS access key ID",
			in:     `using AKIAIOSFODNN7EXAMPLE for s3`,
			secret: "AKIAIOSFODNN7EXAMPLE",
			keep:   "for s3",
		},
		{
			name:   "Slack token",
			in:     `webhook xoxb-123456789012-abcdefghijkl`,
			secret: "xoxb-123456789012-abcdefghijkl",
		},
		{
			name:   "postgres connection string",
			in:     `dial postgres://app:hunter2@db.internal:5432/orders failed`,
			secret: "hunter2",
			keep:   "postgres://[REDACTED:url_credentials]@db.internal:5432/orders failed",
		},
		{
			name:   "redis URL with password only",
			in:     `redis://default:s3cretpass@cache:6379`,
			secret: "s3cretpass",
		},
		{
			name:   "password in a log line",
			in:     `level=error msg="auth failed" password=hunter2 user=app`,
			secret: "hunter2",
			keep:   "user=app",
		},
		{
			name:   "YAML-style secret",
			in:     "db_password: correct-horse-battery",
			secret: "correct-horse-battery",
			keep:   "db_password: ",
		},
		{
			name:   "JSON secret field",
			in:     `{"clientSecret":"x8f7a6s5d4f3","region":"eu"}`,
			secret: "x8f7a6s5d4f3",
			keep:   `"region":"eu"`,
		},
		{
			name:   "API key in a query string",
			in:     `GET /v1/data?api_key=ab12cd34ef56&page=2`,
			secret: "ab12cd34ef56",
			keep:   "&page=2",
		},
		{
			name:   "Azure SAS signature",
			in:     `https://acct.blob.core.windows.net/c?sv=2022&sig=Zm9vYmFyYmF6cXV4`,
			secret: "Zm9vYmFyYmF6cXV4",
		},
		{
			name:   "Kubernetes env var named as a secret",
			in:     `{"name":"DB_PASSWORD","value":"hunter2"}`,
			secret: "hunter2",
			keep:   `"name":"DB_PASSWORD"`,
		},
		{
			name:   "Kubernetes env var with an API key name",
			in:     `{"name":"STRIPE_API_KEY","value":"not-a-real-key-abc"}`,
			secret: "not-a-real-key-abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New().String(tt.in)
			if strings.Contains(got, tt.secret) {
				t.Errorf("secret %q survived:\n%s", tt.secret, got)
			}
			if !strings.Contains(got, "[REDACTED:") {
				t.Errorf("no redaction marker in output:\n%s", got)
			}
			if tt.keep != "" && !strings.Contains(got, tt.keep) {
				t.Errorf("expected %q to survive:\n%s", tt.keep, got)
			}
		})
	}
}

// Over-redaction silently degrades diagnosis, so the things that must not be
// touched are tested as carefully as the things that must.
func TestRedactLeavesDiagnosticTextAlone(t *testing.T) {
	for _, in := range []string{
		`2026-09-20T12:00:00.123456789Z container started`,
		`Reallocated_Sector_Ct 1544 Current_Pending_Sector 168`,
		`blk_update_request: I/O error, dev sda, sector 1937570 op 0x0:(READ)`,
		`image: nginx@sha256:0d17b565c37bcbd895e9d92315a05c1c3c9a29f762b011a10c54a66cd53c9b31`,
		`link 52:54:00:12:34:56 up`,
		`"automountServiceAccountToken": false`,
		`"serviceAccountToken":{"expirationSeconds":3607,"path":"token"}`,
		`{"secretName":"db-creds","optional":false}`,
		`Back-off restarting failed container postgres in pod postgres-0`,
		`kernel 6.8.0-45-generic, kubelet v1.31.2`,
		`std::vector<int> a::b`,
	} {
		if got := New().String(in); got != in {
			t.Errorf("text should be unchanged:\n  in: %s\n got: %s", in, got)
		}
	}
}

func TestRedactEmailAndIPPseudonyms(t *testing.T) {
	r := New()
	a := r.String(`connect to 10.0.2.7 from 10.0.2.9 failed; paged ops@example.com`)
	b := r.String(`retry to 10.0.2.7 ok; cc Ops@Example.com and dev@example.org`)

	for _, leaked := range []string{"10.0.2.7", "10.0.2.9", "ops@example.com", "dev@example.org"} {
		if strings.Contains(strings.ToLower(a+b), strings.ToLower(leaked)) {
			t.Errorf("%s survived", leaked)
		}
	}

	// The same address gets the same pseudonym across strings, so the model
	// can still correlate an event with a log line.
	if !strings.Contains(a, "connect to [IP-1] from [IP-2]") {
		t.Errorf("a = %s", a)
	}
	if !strings.Contains(b, "retry to [IP-1] ok") {
		t.Errorf("same IP should keep its pseudonym: %s", b)
	}
	if !strings.Contains(a, "[EMAIL-1]") || !strings.Contains(b, "cc [EMAIL-1] and [EMAIL-2]") {
		t.Errorf("email pseudonyms inconsistent:\n%s\n%s", a, b)
	}

	// Separate Redactors do not share pseudonyms.
	if got := New().String("10.0.2.9"); got != "[IP-1]" {
		t.Errorf("fresh redactor = %s", got)
	}
}

func TestRedactIPv6(t *testing.T) {
	got := New().String(`peer fd00:10:244::1c dialed 2001:db8::8a2e:370:7334, loopback ::1`)
	for _, leaked := range []string{"fd00:10:244::1c", "2001:db8::8a2e:370:7334"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%s survived: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "[IP-1]") || !strings.Contains(got, "[IP-2]") {
		t.Errorf("got %s", got)
	}
}

func TestRedactInvalidIPsAreNotAddresses(t *testing.T) {
	if got := New().String("value 999.300.1.2 out of range"); got != "value 999.300.1.2 out of range" {
		t.Errorf("got %s", got)
	}
}

func TestRedactLiterals(t *testing.T) {
	r := New()
	r.AddLiteral("S5Y2NG0R123456K", "serial")
	r.AddLiteral("abc", "serial") // too short to redact safely; ignored
	got := r.String(`{"serial_number":"S5Y2NG0R123456K","model":"Samsung abc"}`)
	if strings.Contains(got, "S5Y2NG0R123456K") {
		t.Errorf("literal survived: %s", got)
	}
	if !strings.Contains(got, `"serial_number":"[REDACTED:serial]"`) || !strings.Contains(got, "Samsung abc") {
		t.Errorf("got %s", got)
	}
}

// Redacting serialised API objects must leave them parseable: the reasoning
// layer and fixtures both treat them as JSON.
func TestRedactPreservesJSON(t *testing.T) {
	pod := `{"metadata":{"name":"api-0","annotations":{"owner":"sre@example.com"}},` +
		`"spec":{"containers":[{"name":"api","env":[` +
		`{"name":"DB_PASSWORD","value":"hunter2"},` +
		`{"name":"DATABASE_URL","value":"postgres://app:hunter2@10.0.3.4:5432/db"},` +
		`{"name":"LOG_LEVEL","value":"debug"}]}]},` +
		`"status":{"podIP":"10.244.1.5","hostIP":"192.168.1.20"}}`

	got := New().String(pod)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("redacted JSON no longer parses: %v\n%s", err, got)
	}
	for _, leaked := range []string{"hunter2", "sre@example.com", "10.244.1.5", "192.168.1.20", "10.0.3.4"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%s survived:\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, `"value":"debug"`) {
		t.Errorf("non-secret env value should survive:\n%s", got)
	}
}

func TestSummaryCountsWithoutValues(t *testing.T) {
	r := New()
	r.String(`password=hunter2 from 10.0.0.1 and 10.0.0.1`)
	s := r.Summary()
	if s[KindSecret] != 1 || s[KindIP] != 2 {
		t.Errorf("summary = %v", s)
	}
	for k := range s {
		if strings.Contains(k, "hunter2") || strings.Contains(k, "10.0.0.1") {
			t.Errorf("summary leaks a value: %v", s)
		}
	}
}

func TestRedactIsIdempotent(t *testing.T) {
	in := `password=hunter2 Authorization: Bearer abcdefghijklmnop at 10.1.1.1 by a@b.io`
	once := New().String(in)
	if twice := New().String(once); twice != once {
		t.Errorf("redacting redacted text changed it:\n once: %s\ntwice: %s", once, twice)
	}
}
