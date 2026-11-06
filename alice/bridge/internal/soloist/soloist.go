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

const (
	timeout   = 3 * time.Second
	queueWait = 700 * time.Millisecond
	maxQueued = 5
	skipGap   = 300 * time.Millisecond
)

type Client struct{ StateDir string }

// Available reports whether Soloist is installed. The bridge boots first, so the
// endpoint files may not exist yet; commands read them when they run.
func (c *Client) Available() bool {
	info, err := os.Stat(c.StateDir)
	return err == nil && info.IsDir()
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

// Playing is true only while sound is actually coming out.
func (c *Client) Playing(ctx context.Context) bool {
	status, err := c.status(ctx)
	return err == nil && status == "playing"
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
	return "", nil
}

// queued counts tracks added to the queue that are still waiting. No answer within
// the wait means none are known.
func (c *Client) queued(ctx context.Context) int {
	ctx, cancel := context.WithTimeout(ctx, queueWait)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return 0
	}
	defer conn.CloseNow()
	if send(ctx, conn, "get_queue") != nil {
		return 0
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return 0
		}
		var event struct {
			Type     string `json:"type"`
			Upcoming []struct {
				Source string `json:"source"`
			} `json:"upcoming"`
		}
		if json.Unmarshal(data, &event) != nil || event.Type != "queue_changed" {
			continue
		}
		count := 0
		for _, item := range event.Upcoming {
			if item.Source == "queue" {
				count++
			}
		}
		return min(count, maxQueued)
	}
}

// Queue adds tracks after the current one.
func (c *Client) Queue(ctx context.Context, uris []string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	for _, uri := range uris {
		data, err := json.Marshal(map[string]string{"type": "command", "command": "add_to_queue", "uri": uri})
		if err != nil {
			return err
		}
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			return err
		}
	}
	return conn.Close(websocket.StatusNormalClosure, "")
}

// PlayURI claims the speaker and starts a Spotify source from its beginning. Queued
// tracks outlive a new play and would cut in, and Soloist can only skip them.
func (c *Client) PlayURI(ctx context.Context, uri string) (string, error) {
	leftover := c.queued(ctx)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.CloseNow()
	if err := send(ctx, conn, "activate"); err != nil {
		return "", err
	}
	for range leftover {
		if err := send(ctx, conn, "skip_next"); err != nil {
			return "", err
		}
		// Soloist drops a skip that arrives before the last one has finished.
		time.Sleep(skipGap)
	}
	data, err := json.Marshal(map[string]string{"type": "command", "command": "play", "uri": uri})
	if err != nil {
		return "", err
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return "", err
	}
	return "", conn.Close(websocket.StatusNormalClosure, "")
}

// StartPlayback claims the speaker and resumes the last Spotify context.
func (c *Client) StartPlayback(ctx context.Context) (string, error) {
	if err := c.run(ctx, "activate", "play"); err != nil {
		return "", err
	}
	return "", nil
}
