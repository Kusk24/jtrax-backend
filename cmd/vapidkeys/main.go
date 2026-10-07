// Command vapidkeys prints a new VAPID key pair for browser notifications, as
// the environment lines the server reads (internal/webpush). Run it once per
// deployment and put the output in the server's environment — never in git:
// the private key is what lets a push service trust this server.
//
//	go run ./cmd/vapidkeys
package main

import (
	"fmt"
	"log"

	wp "github.com/SherClockHolmes/webpush-go"
)

func main() {
	priv, pub, err := wp.GenerateVAPIDKeys()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("WEBPUSH_VAPID_PUBLIC_KEY=%s\nWEBPUSH_VAPID_PRIVATE_KEY=%s\n", pub, priv)
}
