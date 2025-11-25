package mllmcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client wraps API calls.
type Client struct {
	BaseURL string
	Token   string
	Timeout time.Duration
}

func (c *Client) newRequest(path string) (*http.Request, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (c *Client) do(req *http.Request, target interface{}) error {
	httpClient := &http.Client{Timeout: c.Timeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed: %s", req.Method, req.URL.Path, resp.Status)
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (c *Client) GetJSON(path string, target interface{}) error {
	req, err := c.newRequest(path)
	if err != nil {
		return err
	}
	return c.do(req, target)
}

func (c *Client) post(path string, body io.Reader, target interface{}) error {
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequest(http.MethodPost, base+path, body)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, target)
}

func (c *Client) PostJSON(path string, payload interface{}, target interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.post(path, bytes.NewReader(data), target)
}

func (c *Client) PostRawJSON(path string, payload []byte, target interface{}) error {
	return c.post(path, bytes.NewReader(payload), target)
}

func (c *Client) DeleteJSON(path string, payload interface{}, target interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequest(http.MethodDelete, base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, target)
}

// StreamEvents opens the SSE feed and invokes handler for each event. Returning false stops the stream.
func (c *Client) StreamEvents(ctx context.Context, handler func(EventEnvelope) bool) error {
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s failed: %s", "/events", resp.Status)
	}

	reader := bufio.NewReader(resp.Body)
	var (
		eventType string
		eventID   string
		dataLines []string
	)

	dispatch := func() bool {
		if len(dataLines) == 0 {
			eventType = ""
			eventID = ""
			return true
		}
		raw := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]

		var envelope EventEnvelope
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			return true
		}
		if envelope.Type == "" {
			envelope.Type = eventType
		}
		if envelope.ID == "" {
			envelope.ID = eventID
		}
		if handler != nil {
			return handler(envelope)
		}
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if !dispatch() {
				return nil
			}
			eventType = ""
			eventID = ""
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "id:"):
			eventID = strings.TrimSpace(line[len("id:"):])
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(line[len("data:"):]))
		}
	}
}
