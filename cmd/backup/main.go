package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/floriankesslergmbh/bartl-keycloak/internal/native"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var err error
	switch {
	case len(os.Args) == 1:
		err = native.Backup(ctx, os.Stdout)
	case len(os.Args) == 2 && os.Args[1] == "recover":
		err = native.Recovery(ctx, os.Stdout)
	default:
		err = fmt.Errorf("usage: backup [recover]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
