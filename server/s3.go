package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// s3Client is the minimum of S3 SigV4 needed to PUT archive objects into R2 (or any S3): no SDK, no listing.
// Payload is sent as UNSIGNED-PAYLOAD (R2 and S3 both accept it), so large files stream without a pre-hash.
type s3Client struct {
	endpoint, region, bucket, accessKey, secretKey string
}

// s3FromEnv: R2_BUCKET + R2_ACCOUNT_ID + R2_ACCESS_KEY_ID + R2_SECRET_ACCESS_KEY (R2), or S3_ENDPOINT/S3_REGION for others.
func s3FromEnv() *s3Client {
	bucket := os.Getenv("R2_BUCKET")
	if bucket == "" {
		return nil
	}
	c := &s3Client{bucket: bucket, region: env("S3_REGION", "auto"), accessKey: os.Getenv("R2_ACCESS_KEY_ID"), secretKey: os.Getenv("R2_SECRET_ACCESS_KEY")}
	c.endpoint = env("S3_ENDPOINT", "https://"+os.Getenv("R2_ACCOUNT_ID")+".r2.cloudflarestorage.com")
	if c.accessKey == "" || c.secretKey == "" {
		return nil
	}
	return c
}

func (c *s3Client) put(key, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	req, err := http.NewRequest(http.MethodPut, c.endpoint+"/"+c.bucket+"/"+key, f)
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Content-Type", "application/gzip")
	c.sign(req, time.Now().UTC())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("s3 put %s: %s: %s", key, res.Status, b)
	}
	return nil
}

// sign implements AWS Signature Version 4 for a request with an unsigned payload.
// https://docs.aws.amazon.com/IAM/latest/UserGuide/create-signed-request.html
func (c *s3Client) sign(req *http.Request, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		req.Method, uriEncode(req.URL.Path), "",
		"host:" + req.URL.Host, "x-amz-content-sha256:UNSIGNED-PAYLOAD", "x-amz-date:" + amzDate, "",
		signedHeaders, "UNSIGNED-PAYLOAD",
	}, "\n")
	scope := date + "/" + c.region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256hex([]byte(canonical))
	k := hmacSHA256([]byte("AWS4"+c.secretKey), date)
	k = hmacSHA256(k, c.region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+sig)
}

// uriEncode per SigV4: encode every byte except unreserved, but keep '/' in paths.
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~', ch == '/':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func sha256hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
