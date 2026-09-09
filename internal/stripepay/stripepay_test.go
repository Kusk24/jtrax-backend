package stripepay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The signature check is the wall between the internet and "this payment is
// settled" — every way over it is tried here.
func TestVerifyWebhook(t *testing.T) {
	payload := []byte(`{"type":"checkout.session.completed"}`)
	secret := "whsec_test"
	now := time.Unix(1_700_000_000, 0)

	t.Run("a correctly signed payload passes", func(t *testing.T) {
		if err := VerifyWebhook(payload, Sign(payload, secret, now), secret, now); err != nil {
			t.Fatalf("valid signature refused: %v", err)
		}
	})

	t.Run("the wrong secret fails", func(t *testing.T) {
		if err := VerifyWebhook(payload, Sign(payload, "whsec_other", now), secret, now); err == nil {
			t.Fatal("signature from the wrong secret accepted")
		}
	})

	t.Run("a tampered payload fails", func(t *testing.T) {
		header := Sign(payload, secret, now)
		tampered := []byte(`{"type":"checkout.session.completed","amount":1}`)
		if err := VerifyWebhook(tampered, header, secret, now); err == nil {
			t.Fatal("tampered payload accepted")
		}
	})

	t.Run("a replayed signature fails once it is stale", func(t *testing.T) {
		header := Sign(payload, secret, now)
		if err := VerifyWebhook(payload, header, secret, now.Add(Tolerance+time.Second)); err == nil {
			t.Fatal("stale signature accepted")
		}
	})

	t.Run("garbage headers fail without panicking", func(t *testing.T) {
		for _, h := range []string{"", "t=,v1=", "v1=zz", "t=abc,v1=00", "t=170,v1=nothex"} {
			if err := VerifyWebhook(payload, h, secret, now); err == nil {
				t.Fatalf("header %q accepted", h)
			}
		}
	})

	t.Run("a rolling secret's extra v1 entries still pass", func(t *testing.T) {
		good := Sign(payload, secret, now)
		bad := Sign(payload, "whsec_old", now)
		combined := good + "," + strings.TrimPrefix(bad, strings.Split(bad, ",")[0]+",")
		if err := VerifyWebhook(payload, combined, secret, now); err != nil {
			t.Fatalf("valid signature among several refused: %v", err)
		}
	})
}

func TestCreateCheckoutSession(t *testing.T) {
	var gotAuth, gotBody string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		gotBody = string(b)
		w.Write([]byte(`{"id":"cs_test_1","url":"https://checkout.stripe.com/c/pay/cs_test_1"}`))
	}))
	defer stub.Close()

	c := New(Config{SecretKey: "sk_test_x", BaseURL: stub.URL})
	s, err := c.CreateCheckoutSession(context.Background(), "pay_9", "JCA — Beginner (Penny)", 450000,
		"https://api/pay/done", "https://api/pay/cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "cs_test_1" || !strings.Contains(s.URL, "cs_test_1") {
		t.Fatalf("wrong session back: %+v", s)
	}
	if gotAuth != "Bearer sk_test_x" {
		t.Fatalf("key not sent as bearer auth: %q", gotAuth)
	}
	for _, want := range []string{
		"mode=payment",
		"metadata%5Bpayment_id%5D=pay_9",
		"unit_amount%5D=450000",
		"currency%5D=thb",
	} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body missing %q:\n%s", want, gotBody)
		}
	}
}

func TestNewIsOffWithoutAKey(t *testing.T) {
	if New(Config{}) != nil {
		t.Fatal("client without a key should be nil (checkout off)")
	}
}
