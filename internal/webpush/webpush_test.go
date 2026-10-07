package webpush

/* What reaches a push service is what a browser can read: the test plays the
   browser, decrypting the body by RFC 8291 (aes128gcm) with its own keys and
   checking the VAPID signature against the academy's public key, rather than
   trusting the library to agree with itself. */

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wp "github.com/SherClockHolmes/webpush-go"
)

var b64 = base64.RawURLEncoding

// browser is one subscribed browser: its key pair and auth secret.
type browser struct {
	key  *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) browser {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return browser{key, auth}
}

func (b browser) subscription(endpoint string) Subscription {
	return Subscription{Endpoint: endpoint, P256dh: b64.EncodeToString(b.key.PublicKey().Bytes()), Auth: b64.EncodeToString(b.auth)}
}

// decrypt reads an aes128gcm body the way a browser does.
func (b browser) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt, keyLen := body[:16], int(body[20])
	serverPub, err := ecdh.P256().NewPublicKey(body[21 : 21+keyLen])
	if err != nil {
		t.Fatalf("sender key: %v", err)
	}
	shared, err := b.key.ECDH(serverPub)
	if err != nil {
		t.Fatal(err)
	}
	prk, _ := hkdf.Extract(sha256.New, shared, b.auth)
	info := "WebPush: info\x00" + string(b.key.PublicKey().Bytes()) + string(serverPub.Bytes())
	ikm, _ := hkdf.Expand(sha256.New, prk, info, 32)
	prk2, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk2, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk2, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+keyLen:], nil)
	if err != nil {
		t.Fatalf("the browser could not decrypt the message: %v", err)
	}
	// The last record ends with a 0x02 delimiter, then any padding zeros.
	end := strings.LastIndexByte(string(plain), 2)
	if end < 0 {
		t.Fatal("no record delimiter")
	}
	return plain[:end]
}

// checkVAPID verifies the Authorization header's JWT with the public key and
// returns its claims.
func checkVAPID(t *testing.T, header, publicKey string) map[string]any {
	t.Helper()
	var token, key string
	for _, part := range strings.Split(strings.TrimPrefix(header, "vapid "), ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "t="); ok {
			token = v
		}
		if v, ok := strings.CutPrefix(part, "k="); ok {
			key = v
		}
	}
	if key != publicKey {
		t.Fatalf("k= is %q, want the academy's public key", key)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	raw, _ := b64.DecodeString(key)
	x, y := elliptic.Unmarshal(elliptic.P256(), raw)
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	sig, _ := b64.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the VAPID signature does not verify with the public key")
	}
	claimsJSON, _ := b64.DecodeString(parts[1])
	var claims map[string]any
	json.Unmarshal(claimsJSON, &claims)
	return claims
}

func testConfig(t *testing.T) Config {
	t.Helper()
	priv, pub, err := wp.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	return Config{PublicKey: pub, PrivateKey: priv, Subject: "mailto:office@jca.ac.th"}
}

func TestABrowserCanReadWhatIsSent(t *testing.T) {
	cfg := testConfig(t)
	b := newBrowser(t)
	var got []byte
	var claims map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" {
			t.Errorf("headers: encoding %q, TTL %q", r.Header.Get("Content-Encoding"), r.Header.Get("TTL"))
		}
		claims = checkVAPID(t, r.Header.Get("Authorization"), cfg.PublicKey)
		body, _ := io.ReadAll(r.Body)
		if rs := binary.BigEndian.Uint32(body[16:20]); rs == 0 {
			t.Error("record size is zero")
		}
		got = b.decrypt(t, body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	payload := `{"title":"Penny has arrived","body":"Checked in at 13:59"}`
	res := New(cfg).Send(context.Background(), b.subscription(srv.URL+"/push/abc"), []byte(payload))
	if !res.OK {
		t.Fatalf("send: %+v", res)
	}
	if string(got) != payload {
		t.Fatalf("decrypted %q, want %q", got, payload)
	}
	if claims["aud"] != srv.URL || claims["sub"] != "mailto:office@jca.ac.th" {
		t.Fatalf("claims: %v", claims)
	}
}

func TestASubscriptionThePushServiceDroppedIsGone(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		res := New(testConfig(t)).Send(context.Background(), newBrowser(t).subscription(srv.URL), []byte(`{}`))
		srv.Close()
		if res.OK || !res.Gone {
			t.Errorf("%d: %+v, want gone", status, res)
		}
	}
}

func TestAPushServiceErrorIsNotGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	res := New(testConfig(t)).Send(context.Background(), newBrowser(t).subscription(srv.URL), []byte(`{}`))
	if res.OK || res.Gone || !strings.Contains(res.Error, "429") {
		t.Fatalf("%+v", res)
	}
}

func TestNoKeysNoClient(t *testing.T) {
	if New(Config{PublicKey: "x", PrivateKey: "y"}) != nil {
		t.Fatal("a client without a subject should not be built")
	}
	var c *Client
	if c.PublicKey() != "" {
		t.Fatal("a nil client has no key")
	}
}

func TestOnlyAPushServiceIsAnEndpoint(t *testing.T) {
	ok := []string{
		"https://fcm.googleapis.com/fcm/send/abc:def",
		"https://updates.push.services.mozilla.com/wpush/v2/gAAA",
		"https://web.push.apple.com/QGuQ",
		"https://wns2-par02p.notify.windows.com/w/?token=BQYAAA",
	}
	bad := []string{
		"http://fcm.googleapis.com/fcm/send/abc",       // not https
		"https://fcm.googleapis.com.evil.example/x",    // lookalike
		"https://evilfcm.googleapis.com.example/x",     // lookalike
		"https://127.0.0.1/push",                       // this server's network
		"https://localhost:8790/api/v1/users",          // this server
		"https://fcm.googleapis.com:8443/fcm/send/abc", // odd port
		"https://user@fcm.googleapis.com/fcm/send/abc", // credentials
		"ExponentPushToken[abc]",
		"",
	}
	for _, u := range ok {
		if !AllowedEndpoint(u) {
			t.Errorf("%s: refused", u)
		}
	}
	for _, u := range bad {
		if AllowedEndpoint(u) {
			t.Errorf("%s: allowed", u)
		}
	}
}
