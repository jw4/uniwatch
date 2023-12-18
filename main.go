package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"

	"github.com/nats-io/nats.go"
	"nhooyr.io/websocket"

	lnats "github.com/jw4/uniwatch/pkg/nats"
	"github.com/jw4/uniwatch/pkg/unifi"
)

var (
	errlg  *log.Logger = log.Default()
	infolg *log.Logger = log.Default()
)

func main() {
	self := os.Getenv("APP_NAME")

	if self == "" {
		self = path.Base(os.Args[0])
	}

	cn := &unifi.Connection{
		AppName:  self,
		BaseURL:  os.Getenv("UNIFI_ENDPOINT"),
		Username: os.Getenv("UNIFI_USERNAME"),
		Password: os.Getenv("UNIFI_PASSWORD"),
	}

	ctx, cancelFn := context.WithCancel(context.Background())

	nc, err := nats.Connect(os.Getenv("NATS_URL"))
	if err != nil {
		errlg.Fatalf("failed to connect to NATS: %v", err)
	}

	errlg = lnats.NewStdLogger(&lnats.Logger{Connection: nc, PublishSubject: fmt.Sprintf("log.error.%s", self), LogFlags: log.LstdFlags})
	infolg = lnats.NewStdLogger(&lnats.Logger{Connection: nc, PublishSubject: fmt.Sprintf("log.info.%s", self), LogFlags: log.LstdFlags})

	errch := make(chan error)

	if err := cn.Events(ctx, NewHandler(self, NewPublisher(nc)), errch); err != nil {
		errlg.Fatalf("failed to listen to events: %v", err)
	}

	for err := range errch {
		errlg.Printf("error: %v\n", err)

		// TODO: handle certain errors, and cancel context as needed
		if errors.Is(err, io.EOF) {
			cancelFn()
			close(errch)
		}
	}

	<-ctx.Done()
}

func NewPublisher(nc *nats.Conn) func(context.Context, string, []byte) error {
	return func(ctx context.Context, subject string, message []byte) error {
		if err := nc.Publish(subject, message); err != nil {
			return fmt.Errorf("publishing message: %w", err)
		}

		return nil
	}
}

func NewHandler(baseSubject string, publisher func(context.Context, string, []byte) error) func(context.Context, websocket.MessageType, io.Reader) error {
	return func(ctx context.Context, t websocket.MessageType, rdr io.Reader) error {
		d, err := io.ReadAll(rdr)
		if err != nil {
			return fmt.Errorf("reading from rdr: %w", err)
		}

		var m map[string]any
		if err := json.Unmarshal(d, &m); err != nil {
			return fmt.Errorf("unmarshaling message: %w", err)
		}

		meta, ok := m["meta"].(map[string]any)
		if !ok {
			infolg.Printf("no meta key: %+v", m)
			return nil
		}

		msg, ok := meta["message"].(string)
		if !ok {
			infolg.Printf("no message key: %+v", meta)
			return nil
		}

		parts := strings.Split(msg, ":")
		subj := fmt.Sprintf("%s.%s", baseSubject, parts[0])
		switch msg {
		case "client:sync":
		case "device:sync":
			mac, ok := meta["mac"].(string)
			if !ok {
				infolg.Printf("no mac key: %+v", meta)
				return nil
			}
			subj = fmt.Sprintf("%s.%s", subj, mac)
		case "events":
		case "session-metadata:sync":
		case "unifi-device:sync":
		case "user:sync":
		default:
			errlg.Printf("missing handler for subject %q", msg)
			return nil
		}

		dt, ok := m["data"]
		if !ok {
			infolg.Printf("no data key: %+v", m)
			return nil
		}

		data, ok := dt.([]interface{})
		if !ok {
			infolg.Printf("data unknown type: %T", dt)
			return nil
		}

		raw, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshaling message: %w", err)
		}

		if err = publisher(ctx, subj, raw); err != nil {
			return err
		}

		infolg.Printf("published %q", subj)
		return nil
	}
}
