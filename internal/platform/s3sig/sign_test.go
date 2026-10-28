package s3sig_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/builtly/builtly/internal/platform/s3sig"
)

var suiteCreds = s3sig.Credentials{
	AccessKeyID:     "AKIDEXAMPLE",
	SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
}

func TestDeriveSigningKey(t *testing.T) {
	// AWS docs: deriving a signing key
	got := s3sig.DeriveSigningKeyHex("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20120215", "us-east-1", "iam")
	want := "f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d"
	if got != want {
		t.Fatalf("key\n got %s\nwant %s", got, want)
	}
}

func TestGetVanilla(t *testing.T) {
	h := http.Header{}
	h.Set("Host", "example.amazonaws.com")
	auth := s3sig.SignTestSuite("GET", "/", nil, h, "20150830T123600Z", "us-east-1", "service", suiteCreds)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if auth != want {
		t.Fatalf("auth\n got %s\nwant %s", auth, want)
	}
}

func TestGetVanillaQuery(t *testing.T) {
	h := http.Header{}
	h.Set("Host", "example.amazonaws.com")
	q := url.Values{"Param1": {"value1"}}
	auth := s3sig.SignTestSuite("GET", "/", q, h, "20150830T123600Z", "us-east-1", "service", suiteCreds)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=a67d582fa61cc504c4bae71f336f98b97f1ea3c7a6bfe1b6e45aec72011b9aeb"
	if auth != want {
		t.Fatalf("auth\n got %s\nwant %s", auth, want)
	}
}

func TestPresignRoundTripMinIO(t *testing.T) {
	// Local fake S3 that verifies our signed Authorization on PUT via query auth by accepting any signed URL shape.
	var gotMethod, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		q := r.URL.Query()
		if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-Signature") == "" {
			http.Error(w, "missing sig", 403)
			return
		}
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "4")
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "no", 405)
	}))
	t.Cleanup(srv.Close)

	u, _ := url.Parse(srv.URL + "/bucket/key")
	hdrs := http.Header{}
	hdrs.Set("Content-Type", "image/png")
	presigned := s3sig.Presign(s3sig.PresignInput{
		Method: "PUT", URL: u, Headers: hdrs,
		SignedHeaderKeys: []string{"host", "content-type"},
		Expires:          15 * time.Minute,
		Service:          "s3", Region: "auto",
		Credentials: suiteCreds,
		Now:         time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC),
	})
	req, err := http.NewRequest(http.MethodPut, presigned.String(), strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "image/png")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("put status %d", res.StatusCode)
	}
	if gotMethod != "PUT" || gotCT != "image/png" {
		t.Fatalf("got %s %s", gotMethod, gotCT)
	}
}
