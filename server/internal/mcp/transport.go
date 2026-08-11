package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

type callOptions struct {
	protocolVersion string
	sessionID       string
	toolName        string
	modern          bool
	notification    bool
}

type responseMeta struct {
	sessionID string
}

func (c *Client) rpcCall(
	ctx context.Context,
	method string,
	params any,
	options callOptions,
) (json.RawMessage, responseMeta, error) {
	id := c.nextID.Add(1)
	body, contentType, meta, err := c.post(ctx, rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}, options)
	if err != nil {
		return nil, meta, err
	}
	response, err := decodeRPCResponse(body, contentType, id)
	if err != nil {
		return nil, meta, err
	}
	if response.Error != nil {
		return nil, meta, response.Error
	}
	if len(response.Result) == 0 {
		return nil, meta, errors.New("MCP JSON-RPC response has no result")
	}
	return response.Result, meta, nil
}

func (c *Client) rpcNotify(
	ctx context.Context,
	method string,
	params any,
	options callOptions,
) error {
	options.notification = true
	_, _, _, err := c.post(ctx, rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}, options)
	return err
}

func (c *Client) post(
	ctx context.Context,
	payload rpcRequest,
	options callOptions,
) ([]byte, string, responseMeta, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", responseMeta{}, fmt.Errorf("encode MCP JSON-RPC request: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", responseMeta{}, fmt.Errorf("create MCP HTTP request: %w", err)
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if options.protocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", options.protocolVersion)
	}
	if options.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", options.sessionID)
	}
	if options.modern {
		req.Header.Set("Mcp-Method", payload.Method)
		if options.toolName != "" {
			req.Header.Set("Mcp-Name", options.toolName)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", responseMeta{}, fmt.Errorf("send MCP HTTP request: %w", err)
	}
	defer resp.Body.Close()
	meta := responseMeta{sessionID: strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))}
	responseBody, err := readBounded(resp.Body, c.maxResponseBytes)
	if err != nil {
		return nil, "", meta, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", meta, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       responseExcerpt(responseBody),
		}
	}
	if options.notification {
		return responseBody, resp.Header.Get("Content-Type"), meta, nil
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return nil, "", meta, errors.New("MCP endpoint returned an empty response")
	}
	return responseBody, resp.Header.Get("Content-Type"), meta, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read MCP HTTP response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, &ResponseTooLargeError{Limit: limit}
	}
	return body, nil
}

func decodeRPCResponse(body []byte, contentType string, expectedID int64) (rpcResponse, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	}
	trimmed := bytes.TrimSpace(body)
	if strings.EqualFold(mediaType, "text/event-stream") || looksLikeSSE(trimmed) {
		return decodeSSEResponse(trimmed, expectedID)
	}
	return decodeJSONResponse(trimmed, expectedID)
}

func decodeJSONResponse(data []byte, expectedID int64) (rpcResponse, error) {
	var response rpcResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return rpcResponse{}, fmt.Errorf("decode MCP JSON-RPC response: %w", err)
	}
	if !responseIDMatches(response.ID, expectedID) {
		return rpcResponse{}, fmt.Errorf("MCP JSON-RPC response id %s does not match request id %d", response.ID, expectedID)
	}
	return response, nil
}

func decodeSSEResponse(data []byte, expectedID int64) (rpcResponse, error) {
	events, err := parseSSEData(data)
	if err != nil {
		return rpcResponse{}, err
	}
	var firstDecodeErr error
	for _, eventData := range events {
		if bytes.Equal(bytes.TrimSpace(eventData), []byte("[DONE]")) {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal(eventData, &response); err != nil {
			if firstDecodeErr == nil {
				firstDecodeErr = err
			}
			continue
		}
		if responseIDMatches(response.ID, expectedID) {
			return response, nil
		}
	}
	if firstDecodeErr != nil {
		return rpcResponse{}, fmt.Errorf("decode MCP SSE JSON-RPC response: %w", firstDecodeErr)
	}
	return rpcResponse{}, fmt.Errorf("MCP SSE stream contained no response for request id %d", expectedID)
}

func parseSSEData(data []byte) ([][]byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	maxToken := len(data) + 1
	if maxToken < 64*1024 {
		maxToken = 64 * 1024
	}
	scanner.Buffer(make([]byte, 1024), maxToken)

	events := make([][]byte, 0, 1)
	dataLines := make([]string, 0, 1)
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		events = append(events, []byte(strings.Join(dataLines, "\n")))
		dataLines = dataLines[:0]
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field, value = line, ""
		}
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		if field == "data" {
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse MCP SSE response: %w", err)
	}
	flush()
	if len(events) == 0 {
		return nil, errors.New("MCP SSE stream contained no data events")
	}
	return events, nil
}

func responseIDMatches(raw json.RawMessage, expected int64) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	if string(raw) == strconv.FormatInt(expected, 10) {
		return true
	}
	var asString string
	return json.Unmarshal(raw, &asString) == nil && asString == strconv.FormatInt(expected, 10)
}

func looksLikeSSE(data []byte) bool {
	return bytes.HasPrefix(data, []byte("data:")) || bytes.HasPrefix(data, []byte("event:")) ||
		bytes.HasPrefix(data, []byte(":"))
}

func responseExcerpt(body []byte) string {
	const maxExcerpt = 1024
	body = bytes.TrimSpace(body)
	if len(body) > maxExcerpt {
		body = append(append([]byte(nil), body[:maxExcerpt]...), []byte("...")...)
	}
	return strings.Join(strings.Fields(string(body)), " ")
}
