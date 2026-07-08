package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type client struct {
	baseURL    string
	extName    string
	apiKey     string
	httpClient *http.Client

	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

func newClient(baseURL, extName, apiKey string) *client {
	return &client{
		baseURL:    baseURL,
		extName:    extName,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// refreshToken exchanges the API key for a JWT.
func (c *client) refreshToken(ctx context.Context) error {
	url := fmt.Sprintf("%s/v1/extensions/%s/token", c.baseURL, c.extName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	c.mu.Lock()
	c.token = result.Token
	c.expiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	c.mu.Unlock()
	return nil
}

// keepAlive refreshes the token when 80% of its lifetime has elapsed.
func (c *client) keepAlive(ctx context.Context) {
	for {
		c.mu.RLock()
		remaining := time.Until(c.expiresAt)
		c.mu.RUnlock()

		// Sleep until 80% of the token lifetime has elapsed.
		sleep := remaining * 4 / 5
		if sleep < 30*time.Second {
			sleep = 30 * time.Second
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}

		if err := c.refreshToken(ctx); err != nil {
			// Retry quickly on failure.
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				continue
			}
		}
	}
}

func (c *client) bearer() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// Do executes an authenticated request against Tabibu. On 401 it re-exchanges
// the API key and retries once.
func (c *client) Do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.apiKey != "" {
		resp.Body.Close()
		if rerr := c.refreshToken(ctx); rerr != nil {
			return nil, rerr
		}
		return c.do(ctx, method, path, body)
	}
	return resp, nil
}

func (c *client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := c.bearer(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return c.httpClient.Do(req)
}

// Get sends an authenticated GET request.
func (c *client) Get(ctx context.Context, path string) (*http.Response, error) {
	return c.Do(ctx, http.MethodGet, path, nil)
}

// Post sends an authenticated POST request with a JSON body.
func (c *client) Post(ctx context.Context, path string, body any) (*http.Response, error) {
	return c.Do(ctx, http.MethodPost, path, body)
}

// Put sends an authenticated PUT request with a JSON body.
func (c *client) Put(ctx context.Context, path string, body any) (*http.Response, error) {
	return c.Do(ctx, http.MethodPut, path, body)
}
