// Package ytm starts the YouTube Music player installed on the speaker and controls it.
package ytm

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	root   = "/data/opt/ytm-alice"
	socket = "/tmp/ytm-alice-run/control.sock"
)

type Client struct {
	Root   string
	Socket string
}

func New() *Client { return &Client{Root: root, Socket: socket} }

func (c *Client) binary() string { return filepath.Join(c.Root, "bin", "ytm-alice") }

// Available reports whether the player was installed on this speaker.
func (c *Client) Available() bool {
	info, err := os.Stat(c.binary())
	return err == nil && info.Mode().IsRegular()
}

// Supervise keeps the player running. The manager installs it at any time, so the
// file is looked for again instead of once at boot.
func (c *Client) Supervise(ctx context.Context) {
	for ctx.Err() == nil {
		if c.Available() {
			command := exec.CommandContext(ctx, c.binary(), "serve")
			command.Env = append(os.Environ(), "YAM_ROOT="+c.Root, "YTM_ALICE_DATA="+filepath.Join(c.Root, "var"),
				"YTM_ALICE_VOICE=0", "YTM_ALICE_CAST=0", "YTM_ALICE_WEB_ADDR=off", "GOMEMLIMIT=48MiB")
			if err := command.Run(); err != nil && ctx.Err() == nil {
				slog.Warn("ytm_stopped", "error", err)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

type state struct {
	Status string `json:"status"`
}
type reply struct {
	State *state `json:"state"`
	Error string `json:"error"`
}

func (c *Client) call(ctx context.Context, wait time.Duration, request map[string]string) (reply, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return reply{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return reply{}, err
	}
	var answer reply
	if err := json.NewDecoder(conn).Decode(&answer); err != nil {
		return reply{}, err
	}
	if answer.Error != "" {
		return answer, errors.New(answer.Error)
	}
	return answer, nil
}

// Active is true while YouTube Music owns this speaker.
func (c *Client) Active(ctx context.Context) bool {
	answer, err := c.call(ctx, time.Second, map[string]string{"action": "status"})
	if err != nil || answer.State == nil {
		return false
	}
	switch answer.State.Status {
	case "playing", "paused", "buffering":
		return true
	}
	return false
}

// Play searches and starts. An empty query asks for the account's recommendations.
func (c *Client) Play(ctx context.Context, query string) error {
	request := map[string]string{"action": "play", "query": query}
	if query == "" {
		request = map[string]string{"action": "recommendations"}
	}
	_, err := c.call(ctx, 20*time.Second, request)
	return err
}

func (c *Client) Control(ctx context.Context, action string) (string, error) {
	if action == "restart" {
		action = "previous"
	}
	_, err := c.call(ctx, 3*time.Second, map[string]string{"action": action})
	return "", err
}
