package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	URL  string
	HTTP *http.Client
}

func (c Client) Call(ctx context.Context, method string, params any, result any) error {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("RPC HTTP status: %s", response.Status)
	}
	const maxResponse = 8 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponse {
		return fmt.Errorf("RPC response too large")
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *Error          `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.JSONRPC != "2.0" || string(envelope.ID) != "1" {
		return fmt.Errorf("invalid RPC response envelope")
	}
	if envelope.Error != nil {
		if envelope.Result != nil {
			return fmt.Errorf("RPC response contains both result and error")
		}
		return envelope.Error
	}
	if envelope.Result == nil {
		return fmt.Errorf("missing RPC result")
	}
	return json.Unmarshal(envelope.Result, result)
}
