package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/nevindra/oasis/internal/ixd"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := ixd.NewServer(*addr)
	if err := srv.ListenAndServe(ctx); err != nil {
		log.Fatal(err)
	}
}
