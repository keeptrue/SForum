package http

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/keeptrue/sforum/apps/api/config"
)

func TestNewAppStartupBannerUsesSForumBrand(t *testing.T) {
	cfg := config.Config{
		AppName:          "SForum",
		AppEnv:           "test",
		AppLocale:        "zh-CN",
		SupportedLocales: []string{"zh-CN", "en-US"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := NewApp(cfg, logger, Dependencies{})

	ln, err := net.Listen(fiber.NetworkTCP4, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on local test port: %v", err)
	}

	output := captureStartupOutput(t, app, ln)
	if !strings.Contains(output, "SForum API") {
		t.Fatalf("expected SForum banner, got:\n%s", output)
	}
	if strings.Contains(output, "/ ____(_) /_") {
		t.Fatalf("expected Fiber banner to be replaced, got:\n%s", output)
	}
}

func captureStartupOutput(t *testing.T, app *fiber.App, ln net.Listener) string {
	t.Helper()

	oldStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	defer readPipe.Close()

	os.Stdout = writePipe
	defer func() {
		os.Stdout = oldStdout
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Listener(ln)
	}()

	waitForTestListener(t, ln.Addr().String())

	if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("close test listener: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "use of closed network connection") {
			t.Fatalf("test app listener stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("test app listener did not stop")
	}

	if err := writePipe.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}

	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatalf("read startup output: %v", err)
	}
	return string(output)
}

func waitForTestListener(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout(fiber.NetworkTCP4, addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("test listener did not become ready at %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
