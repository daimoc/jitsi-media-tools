package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/daimoc/jitsi-media-tools/jmt"
)

// serveFlags are the serve subcommand's flags (contracts/cli.md).
type serveFlags struct {
	opts     jmt.GatewayOptions
	logLevel string
}

// parseServe maps serve's arguments to gateway options; the secret comes
// from $WHIP_API_SECRET or --secret-file.
func parseServe(args []string, getenv func(string) string, stderr io.Writer) (*serveFlags, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		f          serveFlags
		o          = &f.opts
		secretFile string
	)
	fs.StringVar(&o.Listen, "listen", ":8080", "HTTP address of the WHIP endpoint")
	fs.StringVar(&secretFile, "secret-file", "", "file holding the bearer token (default $WHIP_API_SECRET)")
	fs.IntVar(&o.UDPPort, "udp-port", 8189, "UDP port for all publishers' media")
	fs.StringVar(&o.PublicIP, "public-ip", "", "public address given to publishers behind NAT")
	fs.StringVar(&o.TLSCert, "tls-cert", "", "certificate file, to serve HTTPS")
	fs.StringVar(&o.TLSKey, "tls-key", "", "key file, to serve HTTPS")
	fs.IntVar(&o.MaxStreams, "max-streams", 10, "streams at once")
	fs.BoolVar(&o.Insecure, "insecure", false, "skip TLS certificate verification of the meetings' servers")
	fs.DurationVar(&o.WaitHost, "wait-host", 30*time.Second, "how long a stream waits for a host")
	fs.StringVar(&f.logLevel, "log-level", "info", "debug, info or warn")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	o.Secret = getenv("WHIP_API_SECRET")
	if secretFile != "" {
		b, err := os.ReadFile(secretFile)
		if err != nil {
			return nil, fmt.Errorf("--secret-file: %w", err)
		}
		o.Secret = strings.TrimSpace(string(b))
	}
	if o.Secret == "" {
		return nil, errors.New("a secret is required: set $WHIP_API_SECRET or --secret-file")
	}
	return &f, nil
}

// whipAddress is the address to give publishers, from the listening
// address: an unspecified host becomes the public IP, or localhost.
func whipAddress(addr, publicIP, scheme string) string {
	host, port, _ := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "localhost"
		if publicIP != "" {
			host = publicIP
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port) + "/whip/<server>/<room>?name=<name>"
}

func serve(f *serveFlags, stdout, stderr io.Writer) int {
	f.opts.Logger = newLogger(f.logLevel, stderr)
	g, err := jmt.NewGateway(f.opts)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	scheme := "http"
	if f.opts.TLSCert != "" {
		scheme = "https"
	}
	fmt.Fprintln(stdout, "whip", whipAddress(g.Addr(), f.opts.PublicIP, scheme))
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		for e := range g.Events() {
			printStream(stdout, e)
		}
	}()
	err = g.Serve(ctx)
	<-printed
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func printStream(w io.Writer, e jmt.GatewayEvent) {
	where := e.Stream.DisplayName + " " + strings.TrimPrefix(e.Stream.MeetingURL, "https://")
	switch e.Kind {
	case jmt.StreamStarted:
		fmt.Fprintln(w, "+", where)
	case jmt.StreamEnded:
		fmt.Fprintln(w, "-", where)
	case jmt.StreamFailed:
		fmt.Fprintf(w, "! %s: %v\n", where, e.Err)
	}
}
