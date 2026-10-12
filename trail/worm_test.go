package trail

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// The write-once claims (#61), tried against the storage that makes them.
//
// `trail.WriteOnce` is the process keeping its half: it never erases a
// checkpoint or changes its labels. What keeps a checkpoint from everybody else
// is the store beneath, and docs/runtime.md says what each one keeps. These are
// what those sentences were tried against, and each is skipped without its
// store.

// s3Bucket is a bucket of an S3 service, asked directly, for what an act
// through flob never does: by version, and of the bucket itself.
type s3Bucket struct {
	endpoint, bucket string
	key, secret      string
}

// lockedBucket makes a bucket with Object Lock on, and a default retention in
// compliance mode, on the service PDTEST_S3_OBJECT_LOCK names --
// `http://<access key>:<secret>@host:port`, path-style, as MinIO is.
func lockedBucket(t *testing.T) s3Bucket {
	t.Helper()

	v := os.Getenv("PDTEST_S3_OBJECT_LOCK")
	if v == "" {
		t.Skip("PDTEST_S3_OBJECT_LOCK names no S3 service")
	}
	u, err := url.Parse(v)
	require.NoError(t, err)
	secret, _ := u.User.Password()

	b := s3Bucket{
		endpoint: u.Scheme + "://" + u.Host,
		bucket:   fmt.Sprintf("pdtest-lock-%d", time.Now().UnixNano()),
		key:      u.User.Username(),
		secret:   secret,
	}

	res, body := b.do(t, http.MethodPut, "", nil, map[string]string{"x-amz-bucket-object-lock-enabled": "true"}, nil)
	require.Equal(t, http.StatusOK, res.StatusCode, "make the bucket: %s", body)

	conf := []byte(`<ObjectLockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<ObjectLockEnabled>Enabled</ObjectLockEnabled>` +
		`<Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>1</Days></DefaultRetention></Rule>` +
		`</ObjectLockConfiguration>`)
	sum := md5.Sum(conf)
	res, body = b.do(t, http.MethodPut, "", url.Values{"object-lock": {""}}, map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(sum[:])}, conf)
	require.Equal(t, http.StatusOK, res.StatusCode, "lock the bucket: %s", body)

	return b
}

func (b s3Bucket) stores(t *testing.T) *flob.S3Stores {
	t.Helper()

	s, err := flob.NewS3Stores(flob.S3Config{
		Endpoint:     b.endpoint,
		Bucket:       b.bucket,
		Credentials:  flob.Credentials{AccessKeyID: b.key, SecretAccessKey: b.secret},
		UsePathStyle: true,
	})
	require.NoError(t, err)

	return s
}

// do sends one request, signed with AWS Signature Version 4.
func (b s3Bucket) do(t *testing.T, method, key string, q url.Values, h map[string]string, body []byte) (*http.Response, []byte) {
	t.Helper()

	path := "/" + b.bucket
	if key != "" {
		path += "/" + key
	}
	canonPath := (&url.URL{Path: path}).EscapedPath()

	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	qs := []string{}
	for _, k := range keys {
		qs = append(qs, url.QueryEscape(k)+"="+strings.ReplaceAll(url.QueryEscape(q.Get(k)), "+", "%20"))
	}
	query := strings.Join(qs, "&")

	sum := sha256.Sum256(body)
	payload := hex.EncodeToString(sum[:])
	now := time.Now().UTC()
	amz, day := now.Format("20060102T150405Z"), now.Format("20060102")

	u, err := url.Parse(b.endpoint)
	require.NoError(t, err)
	hs := map[string]string{"host": u.Host, "x-amz-date": amz, "x-amz-content-sha256": payload}
	for k, v := range h {
		hs[strings.ToLower(k)] = v
	}
	names := make([]string, 0, len(hs))
	for k := range hs {
		names = append(names, k)
	}
	slices.Sort(names)
	canonHeaders := ""
	for _, k := range names {
		canonHeaders += k + ":" + strings.TrimSpace(hs[k]) + "\n"
	}
	signed := strings.Join(names, ";")

	req := strings.Join([]string{method, canonPath, query, canonHeaders, signed, payload}, "\n")
	rsum := sha256.Sum256([]byte(req))
	scope := day + "/us-east-1/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amz + "\n" + scope + "\n" + hex.EncodeToString(rsum[:])

	mac := func(k []byte, v string) []byte {
		m := hmac.New(sha256.New, k)
		m.Write([]byte(v))
		return m.Sum(nil)
	}
	k := mac(mac(mac(mac([]byte("AWS4"+b.secret), day), "us-east-1"), "s3"), "aws4_request")
	sig := hex.EncodeToString(mac(k, toSign))

	target := b.endpoint + canonPath
	if query != "" {
		target += "?" + query
	}
	r, err := http.NewRequestWithContext(t.Context(), method, target, bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range hs {
		if k != "host" {
			r.Header.Set(k, v)
		}
	}
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", b.key, scope, signed, sig))

	res, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	return res, out
}

// version is one version of an object, or a delete marker.
type version struct {
	Key, VersionId string
	IsLatest       bool
	marker         bool
}

// versions is every version of every object whose key starts with `prefix`.
func (b s3Bucket) versions(t *testing.T, prefix string) []version {
	t.Helper()

	res, body := b.do(t, http.MethodGet, "", url.Values{"versions": {""}, "prefix": {prefix}}, nil, nil)
	require.Equal(t, http.StatusOK, res.StatusCode, "%s", body)

	var v struct {
		Version      []version `xml:"Version"`
		DeleteMarker []version `xml:"DeleteMarker"`
	}
	require.NoError(t, xml.Unmarshal(body, &v))
	for i := range v.DeleteMarker {
		v.DeleteMarker[i].marker = true
	}

	return append(v.Version, v.DeleteMarker...)
}

// TestCheckpointsOnS3ObjectLock.
//
// flob writes a blob and then a marker carrying its labels, and relabels by
// writing the marker again, so the question was whether that layout survives
// a bucket that overwrites nothing. It does: an overwrite is a new version, a
// delete is a marker over the latest, and every version a checkpoint was ever
// written as is still there for its retention, whoever asks.
func TestCheckpointsOnS3ObjectLock(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	b := lockedBucket(t)
	cp := b.stores(t).Use(CheckpointNamespace)

	tenant := newTenant()
	s := &fakeStore{}
	p := Policy{Archive: flob.NewMemStores(), Keep: Keep{Retain: 30 * day}, Signer: newKey(t), Checkpoints: WriteOnce(cp)}
	for range 2 {
		s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day), value: "a"})
		p.Pass(ctx, s)
	}

	v, err := p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)
	x.Equal(2, v.Checkpoint)

	// The process keeps its half.
	ls, err := listCheckpoints(ctx, cp)
	x.NoError(err)
	x.Len(ls, 2)
	first := ls[0]
	if first.k != 1 {
		first = ls[1]
	}
	x.ErrorIs(p.Checkpoints.Erase(ctx, first.d), ErrWriteOnce)

	// Somebody with the bucket's credentials, past the process: an erase is
	// a delete marker over the latest version, and the chain finds it gone.
	x.NoError(cp.Erase(ctx, first.d))
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Contains(kinds(v), "unkept")

	// And cannot take the versions away for good: compliance mode refuses a
	// delete of a version, whoever's credentials ask.
	vs := b.versions(t, "refs/")
	marker, kept := version{}, []version{}
	for _, v := range vs {
		if !strings.HasSuffix(v.Key, first.d.Encoded()) {
			continue
		}
		if v.marker {
			marker = v
		} else {
			kept = append(kept, v)
		}
	}
	x.NotEmpty(marker.VersionId, "no delete marker: %+v", vs)
	x.Len(kept, 1)
	res, body := b.do(t, http.MethodDelete, kept[0].Key, url.Values{"versionId": {kept[0].VersionId}}, nil, nil)
	x.NotEqual(http.StatusNoContent, res.StatusCode, "a locked version was deleted: %s", body)
	for _, v := range b.versions(t, "blob/") {
		res, _ := b.do(t, http.MethodDelete, v.Key, url.Values{"versionId": {v.VersionId}}, nil, nil)
		x.NotEqual(http.StatusNoContent, res.StatusCode, "the bytes of a checkpoint were deleted")
	}

	// So what was hidden is recovered by taking the marker away.
	res, body = b.do(t, http.MethodDelete, marker.Key, url.Values{"versionId": {marker.VersionId}}, nil, nil)
	x.Equal(http.StatusNoContent, res.StatusCode, "%s", body)
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.True(v.Ok(), "%v", v.Findings)

	// A relabel is a new version, as locked as the first: it is found, and
	// what the checkpoint was labelled is still a version beneath it.
	l := flob.Labels{}
	l.Set(labelFormat, formatCheckpoint)
	l.Set(labelK, "7")
	x.NoError(cp.Label(ctx, first.d, l))
	v, err = p.Verify(ctx, s, false)
	x.NoError(err)
	x.Contains(kinds(v), "relabelled")
	n := 0
	for _, v := range b.versions(t, "refs/") {
		if strings.HasSuffix(v.Key, first.d.Encoded()) && !v.marker {
			n++
		}
	}
	x.Equal(2, n, "the relabel replaced the version it was written over")

	// And a pass goes on writing checkpoints, on top of all of it.
	s.add(fakeRow{tenant: tenant.Uuid(), domain: person, created: ago(40 * day), value: "b"})
	_, err = p.Forget(ctx, s, []pdid.Id{newTenant()})
	x.NoError(err)
	k, _, err := s.Latest(ctx)
	x.NoError(err)
	x.Equal(3, k)
}
