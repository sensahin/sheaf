// Package desktop is the part of the Sheaf desktop app that needs no native
// code: it finds or starts the daemon, pairs the app as a device, serves the
// web queue with /v1 proxied to the daemon's socket, and turns events into
// desktop notifications.
//
// The window itself lives in the separate desktop module, which links the
// system webview (WebKit on macOS and Linux, WebView2 on Windows) with cgo.
// Keeping everything else here lets it build and test with the main module.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/config"
)

// DeviceName is the name the app pairs under, as `sheaf device list` shows it.
const DeviceName = "desktop"

// Handler serves the web queue and proxies POST /v1/* to the daemon at
// socket. The app serves it inside the webview, never on a network port. The
// proxy adds no credentials: every call still carries the page's own bearer
// token.
func Handler(socket string) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = "sheaf"
			r.Out.Host = "sheaf"
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "{\"error\":%q}\n", "cannot reach the Sheaf daemon: "+err.Error())
		},
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/", proxy)
	mux.Handle("GET /", api.WebHandler())
	return mux
}

// FindSheaf returns the sheaf binary: the one shipped next to the app, else
// the one on PATH.
func FindSheaf() (string, error) {
	name := "sheaf"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	p, err := exec.LookPath("sheaf")
	if err != nil {
		return "", errors.New("cannot find the sheaf binary next to the app or on PATH")
	}
	return p, nil
}

// Running reports whether a daemon answers on the socket.
func Running(socket string) bool {
	c, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// EnsureDaemon starts `sheaf daemon` in the background if none is running,
// logging to $SHEAF_HOME/daemon.log, and waits for its socket. The daemon
// outlives the app, because agents keep using it after the window closes.
func EnsureDaemon(ctx context.Context, paths config.Paths, bin string) (started bool, err error) {
	if Running(paths.Socket) {
		return false, nil
	}
	if err := paths.Ensure(); err != nil {
		return false, err
	}
	logPath := filepath.Join(paths.Home, "daemon.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer log.Close()
	cmd := exec.Command(bin, "daemon")
	cmd.Env = append(os.Environ(), "SHEAF_HOME="+paths.Home)
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return false, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case err := <-exited:
			return true, fmt.Errorf("the daemon exited (%v); see %s", err, logPath)
		case <-deadline.C:
			return true, fmt.Errorf("the daemon did not start; see %s", logPath)
		case <-tick.C:
			if Running(paths.Socket) {
				cmd.Process.Release()
				return true, nil
			}
		}
	}
}

func tokenPath(paths config.Paths) string { return filepath.Join(paths.Home, "desktop.token") }

// DeviceToken returns the app's device token, pairing the app on first run
// or when its device was revoked. Revoke it with `sheaf device revoke` like
// any other device.
func DeviceToken(ctx context.Context, paths config.Paths) (string, error) {
	if b, err := os.ReadFile(tokenPath(paths)); err == nil {
		tok := strings.TrimSpace(string(b))
		var who struct {
			Name string `json:"name"`
		}
		err := api.NewClient(paths.Socket, tok).Call(ctx, "whoami", nil, &who)
		var apiErr *api.Error
		switch {
		case err == nil && who.Name == "human":
			return tok, nil
		case err != nil && !errors.As(err, &apiErr):
			return "", err // the daemon is unreachable; keep the token
		}
	}
	owner, err := os.ReadFile(paths.OwnerToken)
	if err != nil {
		return "", fmt.Errorf("cannot read the owner token to pair the app: %w", err)
	}
	var r api.PairResp
	c := api.NewClient(paths.Socket, strings.TrimSpace(string(owner)))
	if err := c.Call(ctx, "device.pair", api.PairReq{Name: DeviceName}, &r); err != nil {
		return "", err
	}
	if err := os.WriteFile(tokenPath(paths), []byte(r.Token+"\n"), 0o600); err != nil {
		return "", err
	}
	return r.Token, nil
}
