package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"nhooyr.io/websocket"

	"github.com/jw4/uniwatch/pkg/unifi"
)

func main() {
	baseURL := os.Getenv("UNIFI_ENDPOINT")
	username := os.Getenv("UNIFI_USERNAME")
	password := os.Getenv("UNIFI_PASSWORD")

	cn := unifi.NewConnection(baseURL, username, password)

	ctx, cancelFn := context.WithCancel(context.Background())

	errch := make(chan error)

	if err := cn.Events(ctx, NewHandler(), errch); err != nil {
		fmt.Fprintf(os.Stderr, "failed to listen to events: %v", err)
		return
	}

	for err := range errch {
		fmt.Fprintf(os.Stderr, "error: %v", err)
		// TODO: handle certain errors, and cancel context as needed
		if errors.Is(err, io.EOF) {
			cancelFn()
			close(errch)
		}
	}
}

func NewHandler() func(websocket.MessageType, io.Reader) error {
	em := json.NewEncoder(os.Stdout)

	return func(t websocket.MessageType, rdr io.Reader) error {
		d, err := io.ReadAll(rdr)
		if err != nil {
			return err
		}

		var m map[string]any
		if err := json.Unmarshal(d, &m); err != nil {
			return err
		}

		meta, ok := m["meta"].(map[string]any)
		if !ok {
			return nil
		}

		msg, ok := meta["message"].(string)
		if !ok {
			return nil
		}

		parts := strings.Split(msg, ":")
		subj := parts[0]
		switch msg {
		case "client:sync":
		case "device:sync":
			mac, ok := meta["mac"].(string)
			if !ok {
				return nil
			}
			subj = fmt.Sprintf("%s.%s", subj, mac)
		case "session-metadata:sync":
		case "unifi-device:sync":
		default:
			return nil
		}

		dt, ok := m["data"]
		if !ok {
			return nil
		}

		data, ok := dt.([]interface{})
		if !ok {
			return nil
		}

		if err := em.Encode(data); err != nil {
			return err
		}

		return nil
	}
}
