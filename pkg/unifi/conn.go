package unifi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"nhooyr.io/websocket"
)

type Connection struct {
	AppName    string
	AppVersion string
	BaseURL    string
	Site       string
	Username   string
	Password   string

	client *http.Client
	agent  string

	eventsWS *websocket.Conn
}

func UserAgent(userAgent string) func(*Connection) {
	return func(c *Connection) {
		c.agent = userAgent
	}
}

func HTTPClient(cl *http.Client) func(*Connection) {
	return func(c *Connection) {
		c.client = cl
	}
}

func (c *Connection) Apply(opts ...func(*Connection)) {
	for _, opt := range opts {
		opt(c)
	}
}

func (c *Connection) Events(ctx context.Context, handler func(context.Context, websocket.MessageType, io.Reader) error, errch chan<- error) error {
	if err := c.openEventsWS(ctx); err != nil {
		return err
	}

	go func(ctx context.Context, c *Connection, errch chan<- error) {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				t, rdr, err := c.eventsWS.Reader(ctx)
				if err != nil {
					errch <- err
					if errors.Is(err, net.ErrClosed) {
						if err := c.openEventsWS(ctx); err != nil {
							errch <- err
							return
						}
					}
					continue
				}

				if err = handler(ctx, t, rdr); err != nil {
					errch <- err
					continue
				}
			}
		}
	}(ctx, c, errch)

	return nil
}

func (c *Connection) AllClients(ctx context.Context) (string, error) {
	d, err := c.apiGet(ctx, "/rest/user")
	return string(d), err
}

func (c *Connection) ActiveClients(ctx context.Context) (string, error) {
	d, err := c.apiGet(ctx, "/stat/sta")
	return string(d), err
}

func (c *Connection) ActiveDevices(ctx context.Context) (string, error) {
	d, err := c.apiGet(ctx, "/stat/device")
	return string(d), err
}

func (c *Connection) LatestEvents(ctx context.Context) (string, error) {
	d, err := c.apiGet(ctx, "/stat/event")
	return string(d), err
}

func (c *Connection) init() error {
	if c.AppName == "" {
		c.AppName = "uniwatch"
	}

	if c.AppVersion == "" {
		c.AppVersion = "v0.0.1-dev"
	}

	if c.BaseURL == "" {
		c.BaseURL = "https://127.0.0.1:6443"
	}

	if c.Site == "" {
		c.Site = "default"
	}

	if c.client == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return fmt.Errorf("creating new cookiejar: %w", err)
		}

		c.client = &http.Client{
			Jar:     jar,
			Timeout: time.Second * 10,
		}
	}

	if c.agent == "" {
		c.agent = fmt.Sprintf("%s %s", c.AppName, c.AppVersion)
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

		return fmt.Errorf("failed to login (%s)", string(d))
	}

	return nil
}

func (c *Connection) openEventsWS(ctx context.Context) error {
	if c.eventsWS != nil {
		if err := c.eventsWS.Ping(ctx); err == nil {
			return nil
		}

		c.eventsWS.CloseNow()
		c.eventsWS = nil
	}

	if err := c.checkLogin(ctx); err != nil {
		return err
	}

	u, err := c.buildURL(
		"/wss/s/default/events",
		map[string]string{
			"clients":                "v2",
			"critical_notifications": "true",
		})
	if err != nil {
		return err
	}

	u.Scheme = "wss"

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

	c.eventsWS = ws

	return nil
}

func (c *Connection) buildURL(leaf string, query map[string]string) (*url.URL, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return u, fmt.Errorf("unable to parse base url %q: %w", c.BaseURL, err)
	}

	u.Path = "/proxy/network/"

	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	return u.JoinPath(leaf), err
}

func (c *Connection) apiGet(ctx context.Context, endpoint string) ([]byte, error) {
	if err := c.checkLogin(ctx); err != nil {
		return nil, err
	}

	u, err := c.buildURL(fmt.Sprintf("/api/s/%s/%s", c.Site, endpoint), nil)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating api request: %w", err)
	}

	req.Header.Set("User-Agent", c.agent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", c.BaseURL)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing api request: %w", err)
	}

	d, err := io.ReadAll(res.Body)
	if err != nil {
		return d, fmt.Errorf("reading response body on api request (status: %d %s): %w", res.StatusCode, res.Status, err)
	}

	defer res.Body.Close()

	if res.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("failed to call api (%s): %s", endpoint, string(d))
	}

	return d, nil
}
