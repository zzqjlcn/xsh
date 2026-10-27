package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/zzqjlcn/xsh/internal/webstore"
	"github.com/zzqjlcn/xsh/internal/webui"
)

func runWebCommand(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("web", flag.ContinueOnError)
	flags.SetOutput(output)
	host := flags.String("host", "127.0.0.1", "Web UI listen host")
	port := flags.Int("port", 8080, "local Web UI port (0 selects a free port)")
	rootPath := flags.String("root-path", "/", "Web UI URL mount path (for example /xsh)")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*host) == "" || *port < 0 || *port > 65535 {
		return errors.New("usage: xsh web [--host address] [--port 0-65535] [--root-path /xsh]")
	}
	root, err := webui.NormalizeRootPath(*rootPath)
	if err != nil {
		return err
	}
	hasUsers, err := webstore.HasUsers()
	if err != nil {
		return err
	}
	if !hasUsers {
		code, err := webstore.SetupCode()
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Web administrator setup code: %s\n", code)
	}
	handler, err := webui.NewForHostAndRootPath(*host, root)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(*host, strconv.Itoa(*port)))
	if err != nil {
		return fmt.Errorf("start Web UI: %w", err)
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	fmt.Fprintf(output, "xsh Web UI: http://%s%s\nPress Ctrl+C to stop.\n", listener.Addr(), root)
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
