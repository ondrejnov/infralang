package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const maxMessageSize = 32 << 20

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type framedReader struct {
	reader *bufio.Reader
}

func newFramedReader(reader io.Reader) *framedReader {
	return &framedReader{reader: bufio.NewReader(reader)}
}

func (reader *framedReader) Read() ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("invalid LSP header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 {
				return nil, fmt.Errorf("invalid Content-Length %q", strings.TrimSpace(value))
			}
			contentLength = length
		}
	}
	if contentLength < 0 {
		return nil, errors.New("missing Content-Length header")
	}
	if contentLength > maxMessageSize {
		return nil, fmt.Errorf("LSP message exceeds %d bytes", maxMessageSize)
	}
	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(reader.reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

type framedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func newFramedWriter(writer io.Writer) *framedWriter {
	return &framedWriter{writer: writer}
}

func (writer *framedWriter) Write(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var message bytes.Buffer
	fmt.Fprintf(&message, "Content-Length: %d\r\n\r\n", len(payload))
	message.Write(payload)
	writer.mu.Lock()
	defer writer.mu.Unlock()
	_, err = writer.writer.Write(message.Bytes())
	return err
}

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     string `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

type CompletionItem struct {
	Label         string `json:"label"`
	Kind          int    `json:"kind,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Documentation string `json:"documentation,omitempty"`
	InsertText    string `json:"insertText,omitempty"`
	SortText      string `json:"sortText,omitempty"`
}
