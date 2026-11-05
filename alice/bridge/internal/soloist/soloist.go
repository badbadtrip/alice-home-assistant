// Package soloist controls the Spotify client that runs on the speaker itself.
package soloist

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const timeout = 3 * time.Second

type Client struct{ StateDir string }

// Available reports whether this speaker has a running Soloist service.
func (c *Client) Available() bool {
	_, err := c.endpoint()
	return err == nil
}

func (c *Client) endpoint() (string, error) {
	addr, err := os.ReadFile(filepath.Join(c.StateDir, "ws.addr"))
	if err != nil {
		return "", err
	}
	port, err := os.ReadFile(filepath.Join(c.StateDir, "ws.port"))
	if err != nil {
		return "", err
	}
	host, number := strings.TrimSpace(string(addr)), strings.TrimSpace(string(port))
	// The API has no authentication, so it must stay on this speaker.
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() || number == "" {
		return "", errors.New("soloist endpoint is not local")
	}
	return "ws://" + net.JoinHostPort(host, number), nil
}

func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	url, err := c.endpoint()
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, url, nil)
	return conn, err
}

func send(ctx context.Context, conn *websocket.Conn, command string) error {
	data, err := json.Marshal(map[string]string{"type": "command", "command": command})
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func (c *Client) status(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.CloseNow()
	if err := send(ctx, conn, "get_state"); err != nil {
		return "", err
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return "", err
		}
		var event struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		}
		if json.Unmarshal(data, &event) == nil && event.Type == "playback_state" {
			return strings.ToLower(event.Status), nil
		}
	}
}

// Active is true while Spotify owns this speaker, so Yandex must not get the control.
func (c *Client) Active(ctx context.Context) bool {
	status, err := c.status(ctx)
	return err == nil && (status == "playing" || status == "paused")
}

func (c *Client) run(ctx context.Context, commands ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	for _, command := range commands {
		if err := send(ctx, conn, command); err != nil {
			return err
		}
	}
	return conn.Close(websocket.StatusNormalClosure, "")
}

func (c *Client) Control(ctx context.Context, action string) (string, error) {
	commands := map[string]string{"next": "skip_next", "previous": "skip_prev", "pause": "pause", "stop": "pause", "resume": "play"}
	command, ok := commands[action]
	if !ok {
		return "", errors.New("unknown soloist control")
	}
	if err := c.run(ctx, command); err != nil {
		return "", err
	}
	return "Хорошо.", nil
}

// StartPlayback claims the speaker and resumes the last Spotify context.
func (c *Client) StartPlayback(ctx context.Context) (string, error) {
	if err := c.run(ctx, "activate", "play"); err != nil {
		return "", err
	}
	return "Включаю.", nil
}
