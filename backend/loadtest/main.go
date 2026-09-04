package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ws "github.com/gorilla/websocket"
)

const headerSize = 16

func chunkFrame(index uint64, payload []byte) []byte {
	frame := make([]byte, headerSize+len(payload))
	binary.BigEndian.PutUint64(frame[0:8], index)
	binary.BigEndian.PutUint64(frame[8:16], uint64(len(payload)))
	copy(frame[headerSize:], payload)
	return frame
}

type createResponse struct {
	ID          string `json:"id"`
	SenderToken string `json:"senderToken"`
	URL         string `json:"url"`
	ExpiresAt   string `json:"expiresAt"`
}

type control struct {
	Type       string `json:"type"`
	TransferID string `json:"transferId,omitempty"`
	Token      string `json:"token,omitempty"`
	ChunkIndex uint64 `json:"chunkIndex"`
	Message    string `json:"message,omitempty"`
}

func createSession(api string, fileSize int64, chunkSize int) (*createResponse, error) {
	body, _ := json.Marshal(map[string]interface{}{"fileName": "load.bin", "fileSize": fileSize, "mimeType": "application/octet-stream", "chunkSize": chunkSize, "transport": "relay"})
	request, err := http.NewRequest(http.MethodPost, api+"/api/transfers", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("create failed: %d %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	var result createResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func wsURL(api, id string) string {
	u, _ := url.Parse(api)
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s/ws/%s", scheme, u.Host, id)
}

func readOffer(connection *ws.Conn) (uint64, error) {
	_, data, err := connection.ReadMessage()
	if err != nil {
		return 0, err
	}
	var message control
	if err := json.Unmarshal(data, &message); err != nil {
		return 0, err
	}
	return message.ChunkIndex, nil
}

func waitForControl(connection *ws.Conn, expected string) error {
	for {
		_, data, err := connection.ReadMessage()
		if err != nil {
			return err
		}
		if len(data) == 0 {
			continue
		}
		var message control
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		if message.Type == expected {
			return nil
		}
		if message.Type == "error" {
			return fmt.Errorf("server error: %s", message.Message)
		}
		if message.Type == "transfer_cancelled" {
			return fmt.Errorf("transfer cancelled")
		}
	}
}

func dial(api, id, token, role string) (*ws.Conn, error) {
	connection, _, err := ws.DefaultDialer.Dial(wsURL(api, id), nil)
	if err != nil {
		return nil, err
	}
	if err := connection.WriteJSON(control{Type: role + "_join", TransferID: id, Token: token}); err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

func runPair(api string, fileSize int64, chunkSize int, window int, errs *atomic.Int64, ok *atomic.Int64) {
	result, err := createSession(api, fileSize, chunkSize)
	if err != nil {
		errs.Add(1)
		return
	}
	sender, err := dial(api, result.ID, result.SenderToken, "sender")
	if err != nil {
		errs.Add(1)
		return
	}
	defer sender.Close()
	if _, err := readOffer(sender); err != nil {
		errs.Add(1)
		return
	}

	receiverToken := receiverTokenFromURL(result.URL)
	receiver, err := dial(api, result.ID, receiverToken, "receiver")
	if err != nil {
		errs.Add(1)
		return
	}
	defer receiver.Close()
	if _, err := readOffer(receiver); err != nil {
		errs.Add(1)
		return
	}

	done := make(chan error, 1)
	go func() {
		totalChunks := int((fileSize + int64(chunkSize) - 1) / int64(chunkSize))
		count := 0
		for {
			messageType, _, readErr := receiver.ReadMessage()
			if readErr != nil {
				done <- readErr
				return
			}
			if messageType == ws.BinaryMessage {
				count++
			} else {
				done <- fmt.Errorf("unexpected text on receiver")
				return
			}
			if count == totalChunks {
				// wait for transfer_complete text after the final binary frame
				for {
					messageType, _, readErr := receiver.ReadMessage()
					if readErr != nil {
						done <- readErr
						return
					}
					if messageType == ws.TextMessage {
						done <- nil
						return
					}
				}
			}
		}
	}()

	// Receiver consent.
	if err := receiver.WriteJSON(control{Type: "accept_transfer"}); err != nil {
		errs.Add(1)
		return
	}
	if err := waitForControl(sender, "transfer_accepted"); err != nil {
		errs.Add(1)
		return
	}

	payload := make([]byte, chunkSize)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	totalChunks := int((fileSize + int64(chunkSize) - 1) / int64(chunkSize))
	inFlight := 0
	acks := make(chan uint64, 16)
	ackErr := make(chan error, 1)
	go func() {
		for {
			_, data, readErr := sender.ReadMessage()
			if readErr != nil {
				ackErr <- readErr
				return
			}
			var message control
			if json.Unmarshal(data, &message) == nil && message.Type == "chunk_ack" {
				acks <- message.ChunkIndex
			}
		}
	}()

	sendIndex := func(index int) bool {
		size := chunkSize
		if int64(index+1)*int64(chunkSize) > fileSize {
			size = int(fileSize - int64(index)*int64(chunkSize))
		}
		frame := chunkFrame(uint64(index), payload[:size])
		if err := sender.WriteMessage(ws.BinaryMessage, frame); err != nil {
			return false
		}
		inFlight++
		if inFlight >= window {
			select {
			case <-acks:
				inFlight--
			case err := <-ackErr:
				_ = err
				return false
			case <-time.After(30 * time.Second):
				return false
			}
		}
		return true
	}

	for index := 0; index < totalChunks; index++ {
		if !sendIndex(index) {
			errs.Add(1)
			return
		}
	}
	// Drain remaining ACKs so the server-side window clears before completion.
	for inFlight > 0 {
		select {
		case <-acks:
			inFlight--
		case <-ackErr:
			errs.Add(1)
			return
		case <-time.After(30 * time.Second):
			errs.Add(1)
			return
		}
	}

	if err := sender.WriteJSON(control{Type: "transfer_complete"}); err != nil {
		errs.Add(1)
		return
	}
	select {
	case err := <-done:
		if err != nil {
			errs.Add(1)
		} else {
			ok.Add(1)
		}
	case <-time.After(60 * time.Second):
		errs.Add(1)
	}
}

func receiverTokenFromURL(raw string) string {
	if index := strings.Index(raw, "#token="); index >= 0 {
		return raw[index+len("#token="):]
	}
	return ""
}

func main() {
	api := flag.String("url", "http://localhost:8080", "base URL of the relay backend")
	mode := flag.String("mode", "stream", "scenario: stream | idle | reconnect")
	count := flag.Int("count", 10, "number of operations (concurrent transfers / idle sessions / reconnect cycles)")
	size := flag.Int64("size", 4<<20, "payload bytes per transfer")
	chunk := flag.Int("chunk", 64<<10, "chunk size bytes")
	window := flag.Int("window", 4, "sender in-flight window")
	keepalive := flag.Duration("keepalive", 2*time.Second, "how long idle sessions stay attached")
	flag.Parse()

	start := time.Now()
	var errs, ok atomic.Int64

	switch *mode {
	case "idle":
		var wg sync.WaitGroup
		for i := 0; i < *count; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, err := createSession(*api, 1, *chunk)
				if err != nil {
					errs.Add(1)
					return
				}
				sender, err := dial(*api, result.ID, result.SenderToken, "sender")
				if err != nil {
					errs.Add(1)
					return
				}
				defer sender.Close()
				if _, err := readOffer(sender); err != nil {
					errs.Add(1)
					return
				}
				receiver, err := dial(*api, result.ID, receiverTokenFromURL(result.URL), "receiver")
				if err != nil {
					errs.Add(1)
					return
				}
				defer receiver.Close()
				if _, err := readOffer(receiver); err != nil {
					errs.Add(1)
					return
				}
				ok.Add(1)
			}()
		}
		wg.Wait()
		// Hold connections briefly to measure steady-state active counts.
		time.Sleep(*keepalive)
	case "reconnect":
		for i := 0; i < *count; i++ {
			result, err := createSession(*api, 1, *chunk)
			if err != nil {
				errs.Add(1)
				continue
			}
			sender, err := dial(*api, result.ID, result.SenderToken, "sender")
			if err != nil {
				errs.Add(1)
				continue
			}
			if _, err := readOffer(sender); err != nil {
				sender.Close()
				errs.Add(1)
				continue
			}
			sender.Close()
			ok.Add(1)
		}
	case "stream":
		var wg sync.WaitGroup
		for i := 0; i < *count; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				runPair(*api, *size, *chunk, *window, &errs, &ok)
			}()
		}
		wg.Wait()
	default:
		log.Fatalf("unknown mode %q", *mode)
	}

	elapsed := time.Since(start)
	bytes := float64(ok.Load()) * float64(*size)
	fmt.Printf("mode=%s count=%d ok=%d errors=%d elapsed=%s throughput=%.1f MB/s\n", *mode, *count, ok.Load(), errs.Load(), elapsed.Round(time.Millisecond), bytes/elapsed.Seconds()/1024/1024)
}
