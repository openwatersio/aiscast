package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// s3Client is the minimum of S3 SigV4 that the archive and replay need from R2 (or any S3): put, head, list, and get.
// Payload is sent as UNSIGNED-PAYLOAD (R2 and S3 both accept it), so large files stream without a pre-hash.
type s3Client struct {
	endpoint, region, bucket, accessKey, secretKey string
}

// s3HTTP bounds every archive request. The default client has no timeout, and one stalled connection
// would hang the sweep goroutine for good, quietly ending disk reclamation. The limit has to cover a
// whole hour file, and the largest so far is under 200 MB.
var s3HTTP = &http.Client{Timeout: 5 * time.Minute}

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
	res, err := s3HTTP.Do(req)
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
// A client without keys reads a public bucket, unsigned.
func (c *s3Client) sign(req *http.Request, now time.Time) {
	if c.accessKey == "" {
		return
	}
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		req.Method, uriEncode(req.URL.Path), canonicalQuery(req.URL.Query()),
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

// canonicalQuery is SigV4's query string: parameters sorted by name, names and values encoded, '/' included.
func canonicalQuery(q url.Values) string {
	keys := slices.Sorted(maps.Keys(q))
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, strings.ReplaceAll(uriEncode(k), "/", "%2F")+"="+strings.ReplaceAll(uriEncode(v), "/", "%2F"))
		}
	}
	return strings.Join(parts, "&")
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

// size returns the length of the stored object, or -1 if it is not there.
func (c *s3Client) size(key string) (int64, error) {
	req, err := http.NewRequest(http.MethodHead, c.endpoint+"/"+c.bucket+"/"+key, nil)
	if err != nil {
		return 0, err
	}
	c.sign(req, time.Now().UTC())
	res, err := s3HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return -1, nil
	}
	if res.StatusCode/100 != 2 {
		return 0, fmt.Errorf("s3 head %s: %s", key, res.Status)
	}
	return res.ContentLength, nil
}

// s3Object is a key, its size, and its ETag, as list returns them.
type s3Object struct {
	Key  string
	Size int64
	ETag string
}

// list returns every object under prefix, a page of up to 1,000 at a time.
func (c *s3Client) list(ctx context.Context, prefix string) ([]s3Object, error) {
	var out []s3Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/"+c.bucket+"?"+canonicalQuery(q), nil)
		if err != nil {
			return nil, err
		}
		c.sign(req, time.Now().UTC())
		res, err := s3HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		var page struct {
			Contents              []s3Object
			IsTruncated           bool
			NextContinuationToken string
		}
		if res.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			res.Body.Close()
			return nil, fmt.Errorf("s3 list %s: %s: %s", prefix, res.Status, b)
		}
		err = xml.NewDecoder(res.Body).Decode(&page)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, err)
		}
		out = append(out, page.Contents...)
		if !page.IsTruncated {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

// get writes the object at key to path, creating its directory.
func (c *s3Client) get(key, path string) error {
	req, err := http.NewRequest(http.MethodGet, c.endpoint+"/"+c.bucket+"/"+key, nil)
	if err != nil {
		return err
	}
	c.sign(req, time.Now().UTC())
	res, err := s3HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("s3 get %s: %s: %s", key, res.Status, b)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}
