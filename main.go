package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kardianos/service"
)

var version = "dev"

type program struct {
	config Config
	lib    *Library
	server *http.Server
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *program) Start(service.Service) error {
	l, err := newLibrary(p.config)
	if err != nil {
		return err
	}
	l.isolateRendering = true
	listener, err := net.Listen("tcp", net.JoinHostPort(p.config.Host, fmt.Sprint(p.config.Port)))
	if err != nil {
		l.Close()
		return err
	}
	p.lib = l
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	p.server = &http.Server{Handler: l.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		defer close(p.done)
		l.Run(ctx)
	}()
	go func() {
		log.Printf("Home Images %s listening on %s", version, listener.Addr())
		if err := p.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server failed: %v", err)
			os.Exit(1) // Let the service manager restart a failed server.
		}
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	if p.cancel == nil {
		return nil
	}
	p.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := p.server.Shutdown(ctx)
	if err != nil {
		_ = p.server.Close()
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.lib.Close()
	return err
}

func serviceConfig(configPath, user string, userService bool) *service.Config {
	return &service.Config{Name: "HomeImages", DisplayName: "Home Images", Description: "Read-only image and metadata API",
		UserName: user, Arguments: []string{"--config", configPath},
		Option: service.KeyValue{"DelayedAutoStart": true, "OnFailure": "restart", "OnFailureDelayDuration": "10s", "OnFailureResetPeriod": 86400,
			"KeepAlive": true, "RunAtLoad": true, "Restart": "on-failure", "UserService": userService}}
}

func main() {
	configPath := flag.String("config", "config.json", "Configuration file (absolute paths inside)")
	control := flag.String("service", "", "install, uninstall, start, stop, restart, or status")
	user := flag.String("user", "", "OS account used when installing the daemon")
	userService := flag.Bool("user-service", false, "Install a per-user Linux/macOS daemon")
	showVersion := flag.Bool("version", false, "Print version")
	importIndex := flag.String("import-python-index", "", "One-time read-only import of a previous Python SQLite index")
	aiPreview := flag.String("ai-preview", "", "Internal: generate an indexing preview using config on stdin")
	flag.Parse()
	if *aiPreview != "" {
		if err := runAIPreview(*aiPreview); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *showVersion {
		fmt.Println(version)
		return
	}
	absolute, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	c, err := loadConfig(absolute)
	if err != nil {
		log.Fatal(err)
	}
	if *importIndex != "" {
		l, err := newLibrary(c)
		if err != nil {
			log.Fatal(err)
		}
		count, err := l.ImportPython(context.Background(), *importIndex)
		l.Close()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Imported %d images\n", count)
		return
	}
	if runtime.GOOS == "windows" && *user == "" {
		*user = `NT AUTHORITY\LocalService`
	}
	p := &program{config: c}
	svc, err := service.New(p, serviceConfig(absolute, *user, *userService))
	if err != nil {
		log.Fatal(err)
	}
	if *control != "" {
		if *control == "status" {
			status, err := svc.Status()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(status)
			return
		}
		if err := service.Control(svc, *control); err != nil {
			log.Fatal(err)
		}
		return
	}
	// A bounded file log also works when Windows SCM has no stdout/stderr handles.
	if err := os.MkdirAll(c.DataDir, 0750); err != nil {
		log.Fatal(err)
	}
	writer, err := newRotatingLog(filepath.Join(c.DataDir, "home-images.log"), 10<<20)
	if err != nil {
		log.Fatal(err)
	}
	defer writer.Close()
	log.SetOutput(writer)
	if err := svc.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
