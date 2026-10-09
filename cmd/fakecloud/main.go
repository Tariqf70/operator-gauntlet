// Command fakecloud serves the fake cloud database API on its own, for agents
// developing locally (setups S1–S4) and for kind mode.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/Tariqf70/operator-gauntlet/pkg/fakecloud"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8089", "listen address")
	create := flag.Duration("create-duration", 3*time.Second, "time from creating to available")
	resize := flag.Duration("resize-duration", 2*time.Second, "time from resizing to available")
	del := flag.Duration("delete-duration", 2*time.Second, "time from deleting to gone")
	latency := flag.Duration("latency", 0, "added latency per request")
	flag.Parse()

	s := fakecloud.New()
	s.CreateDuration, s.ResizeDuration, s.DeleteDuration, s.Latency = *create, *resize, *del, *latency
	log.Printf("fake cloud API on http://%s (admin: GET /_admin/databases)", *addr)
	srv := &http.Server{Addr: *addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
