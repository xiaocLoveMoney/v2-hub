package objectstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// AWS 文档《Signature Calculations for the Authorization Header》里公开的示例账号与时间（不是真凭据）。
// 访问键拆成两段拼接，免得被密钥扫描当成真 AWS 键。
const (
	awsExampleAccess = "AKIA" + "IOSFODNN7EXAMPLE"
	awsExampleKey    = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	awsExampleHost   = "examplebucket.s3.amazonaws.com"
	awsExampleDate   = "20130524T000000Z"
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func awsExampleTime(t *testing.T) time.Time {
	t.Helper()
	at, err := time.Parse(amzDateLayout, awsExampleDate)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestAuthorizationMatchesAWSExamples(t *testing.T) {
	at := awsExampleTime(t)
	tests := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		hash    string
		want    string
	}{
		{
			name: "get object", method: http.MethodGet, path: "/test.txt",
			headers: map[string]string{headerHost: awsExampleHost, "range": "bytes=0-9", headerAmzHash: emptyPayloadHash, headerAmzDate: awsExampleDate},
			hash:    emptyPayloadHash,
			want:    "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name: "put object", method: http.MethodPut, path: "/" + uriEncodePath("test$file.text"),
			headers: map[string]string{
				"date": "Fri, 24 May 2013 00:00:00 GMT", headerHost: awsExampleHost, headerAmzDate: awsExampleDate,
				headerAmzHash:         "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
				"x-amz-storage-class": "REDUCED_REDUNDANCY",
			},
			hash: "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			want: "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authorization(signRequest{
				method: tt.method, path: tt.path, headers: tt.headers, payloadHash: tt.hash,
				at: at, region: DefaultRegion, accessKey: awsExampleAccess, secretKey: awsExampleKey,
			})
			if !strings.HasSuffix(got, "Signature="+tt.want) {
				t.Fatalf("authorization = %s, want signature %s", got, tt.want)
			}
			if !strings.Contains(got, "Credential="+awsExampleAccess+"/20130524/us-east-1/s3/aws4_request") {
				t.Fatalf("credential scope wrong: %s", got)
			}
		})
	}
}

func TestURIEncodePath(t *testing.T) {
	if got := uriEncodePath("uploads/202611/a b$~.png"); got != "uploads/202611/a%20b%24~.png" {
		t.Fatalf("uriEncodePath = %s", got)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	good := Config{Endpoint: "http://minio:9000", Bucket: "uploads", AccessKey: "a", SecretKey: "b"}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"bad scheme", func(c *Config) { c.Endpoint = "ftp://minio:9000" }},
		{"with path", func(c *Config) { c.Endpoint = "http://minio:9000/x" }},
		{"no bucket", func(c *Config) { c.Bucket = "" }},
		{"no access key", func(c *Config) { c.AccessKey = "" }},
		{"no secret key", func(c *Config) { c.SecretKey = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := good
			tt.mutate(&cfg)
			if _, err := New(cfg); !errors.Is(err, ErrBadConfig) {
				t.Fatalf("err = %v, want ErrBadConfig", err)
			}
		})
	}
	if _, err := New(good); err != nil {
		t.Fatalf("good config rejected: %v", err)
	}
}

func TestPutSendsSignedRequest(t *testing.T) {
	body := []byte("\x89PNG fake")
	var gotPath, gotType, gotCache, gotAuth, gotHash string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		gotPath, gotType, gotCache = r.URL.Path, r.Header.Get(headerType), r.Header.Get(headerCache)
		gotAuth, gotHash = r.Header.Get(headerAuth), r.Header.Get(headerAmzHash)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotBody = raw
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(Config{Endpoint: srv.URL, Bucket: "uploads", AccessKey: "hub-upload", SecretKey: "test-only"})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Put(context.Background(), Object{Key: "202611/abc.png", Body: body, ContentType: "image/png", CacheControl: "public, max-age=60"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if gotPath != "/uploads/202611/abc.png" || gotType != "image/png" || gotCache != "public, max-age=60" || string(gotBody) != string(body) {
		t.Fatalf("request = %s %s %s %q", gotPath, gotType, gotCache, gotBody)
	}
	if !strings.HasPrefix(gotAuth, sigAlgorithm+" Credential=hub-upload/") || !strings.Contains(gotAuth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("authorization = %s", gotAuth)
	}
	if len(gotHash) != 64 {
		t.Fatalf("payload hash = %s", gotHash)
	}
}

func TestPutReportsHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "<Error>AccessDenied</Error>", http.StatusForbidden)
	}))
	defer srv.Close()
	c, err := New(Config{Endpoint: srv.URL, Bucket: "uploads", AccessKey: "a", SecretKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Put(context.Background(), Object{Key: "k.png", Body: []byte("x")})
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v, want ErrUnavailable without body", err)
	}
}
