package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"reflect"
	"strings"

	"github.com/nats-io/nats.go"
	"nhooyr.io/websocket"

	lnats "github.com/jw4/uniwatch/pkg/nats"
	"github.com/jw4/uniwatch/pkg/unifi"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"

	logger = slog.Default()
	writer io.Writer
)

func main() {
	self := os.Getenv("APP_NAME")

	if self == "" {
		self = path.Base(os.Args[0])
	}

	fmt.Printf("%s\n\tversion: v%s\n\t commit: %s\n\t  built: %s\n\n", self, version, commit, date)

	cn := &unifi.Connection{
		AppName:    self,
		AppVersion: version,
		BaseURL:    os.Getenv("UNIFI_ENDPOINT"),
		Username:   os.Getenv("UNIFI_USERNAME"),
		Password:   os.Getenv("UNIFI_PASSWORD"),
	}

	ctx, cancelFn := context.WithCancel(context.Background())

	nc, err := nats.Connect(os.Getenv("NATS_URL"))
	if err != nil {
		logger.ErrorContext(ctx, "connecting to NATS", "nats url", os.Getenv("NATS_URL"), "msg", err)
		os.Exit(-1)
	}

	writer = &lnats.Logger{Connection: nc, PublishSubject: fmt.Sprintf("log.%s", self)}
	logger = slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stderr, writer), nil))

	errch := make(chan error)

	if err := cn.Events(ctx, EventHandler(self, NewPublisher(nc)), errch); err != nil {
		logger.ErrorContext(ctx, "listening to events", "unifi url", os.Getenv("UNIFI_ENDPOINT"), "msg", err)
		os.Exit(-1)
	}

	for err := range errch {
		logger.ErrorContext(ctx, "from errch", "msg", err)

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

func EventHandler(baseSubject string, publisher func(context.Context, string, []byte) error) func(context.Context, websocket.MessageType, io.Reader) error {
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
			logger.WarnContext(ctx, "no meta key", "from", m)
			return nil
		}

		msg, ok := meta["message"].(string)
		if !ok {
			logger.WarnContext(ctx, "no message key", "from", meta)
			return nil
		}

		parts := strings.Split(msg, ":")
		subj := fmt.Sprintf("%s.%s", baseSubject, parts[0])
		switch msg {
		case "client:sync":
		case "critical-notifications:sync":
		case "device:sync":
		case "device:update":
		case "events":
		case "session-metadata:sync":
		case "speed-test:update":
		case "unifi-device:sync":
		case "user:sync":
		default:
			logger.WarnContext(ctx, "missing handler", "subject", msg)
		}

		dt, ok := m["data"]
		if !ok {
			logger.WarnContext(ctx, "no data key", "from", m)
			return nil
		}

		data, ok := dt.([]interface{})
		if !ok {
			logger.WarnContext(ctx, "data in unexpected type", "from", dt, "type", reflect.TypeOf(dt))
			return nil
		}

		raw, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshaling message: %w", err)
		}

		if err = publisher(ctx, subj, raw); err != nil {
			return err
		}

		logger.DebugContext(ctx, "published", "subject", subj)

		return nil
	}
}

func AllClientsHandler(ctx context.Context, msg string) error    { return nil }
func ActiveClientsHandler(ctx context.Context, msg string) error { return nil }
func ActiveDevices(ctx context.Context, msg string) error        { return nil }
func LatestEvents(ctx context.Context, msg string) error         { return nil }
