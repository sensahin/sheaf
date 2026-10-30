package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
)

// Client calls a Sheaf daemon over its Unix socket.
type Client struct {
	Token string
	http  *http.Client
}

// NewClient returns a client for the daemon listening at socket.
func NewClient(socket, token string) *Client {
	return &Client{
		Token: token,
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		}},
	}
}

// Error is an error returned by the daemon.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Call invokes method with in as the body and decodes the result into out.
func (c *Client) Call(ctx context.Context, method string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://sheaf/v1/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the Sheaf daemon (is `sheaf daemon` running?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e errorBody
		json.NewDecoder(resp.Body).Decode(&e)
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
