package unifi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"nhooyr.io/websocket"
)

type Connection struct {
	BaseURL  string
	Username string
	Password string

	client *http.Client
	agent  string
}

func UserAgent(userAgent string) func(*Connection) {
	return func(c *Connection) {
		c.agent = userAgent
	}
}

func NewConnection(baseURL, username, password string, opts ...func(*Connection)) *Connection {
	conn := &Connection{
		BaseURL:  baseURL,
		Username: username,
		Password: password,
	}

	for _, opt := range opts {
		opt(conn)
	}

	return conn
}

func (c *Connection) Events(ctx context.Context, handler func(websocket.MessageType, io.Reader) error, errch chan<- error) error {
	if err := c.checkLogin(ctx); err != nil {
		return err
	}

	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("unable to parse base url %q: %w", c.BaseURL, err)
	}

	q := u.Query()
	q.Set("clients", "v2")
	q.Set("critical_notifications", "true")
	u.RawQuery = q.Encode()

	u.Scheme = "wss"
	u.Path = "/proxy/network/wss/s/default/events"

	ws, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.client})
	if err != nil {
		return fmt.Errorf("dialing websocket %s: %w", u, err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer ws.CloseNow()

		d, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response body on failed websocket dial (status: %d %s): %w", resp.StatusCode, resp.Status, err)
		}

		defer resp.Body.Close()

		return fmt.Errorf("unexpected status (%d %s): %s", resp.StatusCode, resp.Status, string(d))
	}

	ws.SetReadLimit(-1)

	go func(ctx context.Context, ws *websocket.Conn, errch chan<- error) {
		defer ws.CloseNow()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				t, rdr, err := ws.Reader(ctx)
				if err != nil {
					errch <- err
					continue
				}

				if err = handler(t, rdr); err != nil {
					errch <- err
					continue
				}
			}
		}
	}(ctx, ws, errch)

	return nil
}

func (c *Connection) init() error {
	if c.BaseURL == "" {
		c.BaseURL = "https://127.0.0.1:6443"
	}

	if c.client == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return err
		}

		c.client = &http.Client{
			Jar:     jar,
			Timeout: time.Second * 10,
		}
	}

	if c.agent == "" {
		c.agent = "uniwatch dev"
	}

	return nil
}

func (c *Connection) checkLogin(ctx context.Context) error {
	if err := c.init(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/users/self", c.BaseURL), nil)
	if err != nil {
		return fmt.Errorf("creating check login request: %w", err)
	}

	req.Header.Set("User-Agent", c.agent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", c.BaseURL)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing check login request: %w", err)
	}

	switch res.StatusCode {
	case http.StatusUnauthorized:
		return c.login(ctx)
	case http.StatusOK:
		return nil
	}

	d, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("reading response body on failed check login request (status: %d %s): %w", res.StatusCode, res.Status, err)
	}

	defer res.Body.Close()

	return fmt.Errorf("unexpected status (%d %s): %v\n%s\n", res.StatusCode, res.Status, res.Header, string(d))
}

func (c *Connection) login(ctx context.Context) error {
	if err := c.init(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, fmt.Sprintf("%s/api/auth/login", c.BaseURL),
		bytes.NewBufferString(fmt.Sprintf(`{"username":%q,"password":%q,"strict":"true","remember":"true"}`, c.Username, c.Password)))
	if err != nil {
		return fmt.Errorf("creating login request: %w", err)
	}

	req.Header.Set("User-Agent", c.agent)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.BaseURL)

	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing login request: %w", err)
	}

	if res.StatusCode >= http.StatusBadRequest {
		d, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("reading response body on failed login request (status: %d %s): %w", res.StatusCode, res.Status, err)
		}

		defer res.Body.Close()

		return fmt.Errorf("failed to login (%s): %w", string(d), err)
	}

	return nil
}
