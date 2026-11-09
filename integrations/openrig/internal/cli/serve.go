package cli

// serve: the sidecar, in the foreground. launchd (or the owner's shell) supervises it; it never daemonises.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/gw"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/ipc"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/rigcli"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/sidecar"
)

// readCredential reads the one-line credential file: a regular file, owned by the user, mode 0600.
func readCredential(cfg *config.Config) (string, error) {
	info, err := os.Lstat(cfg.CredentialPath)
	switch {
	case err != nil:
		return "", errors.New("there is no credential: run `ratatoskr-openrig pair` first")
	case !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0:
		return "", errors.New("the credential file must be a regular file with mode 0600")
	case info.Size() > maxSecretBytes:
		return "", errors.New("the credential file is larger than 4096 bytes")
	case info.Size() == 0:
		return "", errors.New("the credential file is empty: run `ratatoskr-openrig pair` again")
	}
	raw, err := os.ReadFile(cfg.CredentialPath)
	if err != nil {
		return "", errors.New("cannot read the credential file")
	}
	credential := string(raw)
	for len(credential) > 0 && (credential[len(credential)-1] == '\n' || credential[len(credential)-1] == '\r' || credential[len(credential)-1] == ' ') {
		credential = credential[:len(credential)-1]
	}
	if credential == "" {
		return "", errors.New("the credential file is empty")
	}
	return credential, nil
}

func runServe(args []string, io IO) int {
	fs := newFlags("serve")
	configPath := fs.String("config", "", "")
	insecure := fs.Bool("insecure-http", false, "")
	resume := fs.Bool("resume", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fail(io, ExitUsage, "usage: serve [--config PATH] [--insecure-http] [--resume]")
	}
	// Started from inside a seat, rig send would carry that seat's identity.
	if os.Getenv("OPENRIG_SESSION_NAME") != "" {
		return fail(io, ExitUsage, "OPENRIG_SESSION_NAME is set: serve must not run inside an OpenRig seat; start it from the service manager or a plain shell")
	}
	cfg, code := loadConfig(*configPath, *insecure, io)
	if cfg == nil {
		return code
	}
	if err := cfg.CheckStateDir(true); err != nil {
		return fail(io, ExitUsage, "%v", err)
	}

	if stopped, ok := sidecar.ReadStopped(cfg.StoppedPath()); ok {
		if !*resume {
			fmt.Fprintf(io.Stderr, "ratatoskr-openrig: not starting: this bridge was stopped (%s at %s). Pair again, or run serve --resume.\n",
				stopped.Reason, terminalText(stopped.At))
			return ExitOK
		}
		_ = os.Remove(cfg.StoppedPath())
	}

	// One sidecar at a time: a second poll would replace the first.
	lock, err := os.OpenFile(cfg.LockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fail(io, ExitUsage, "cannot open the lock file")
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(io, ExitUsage, "a sidecar is already running (the lock is held)")
	}

	credential, err := readCredential(cfg)
	if err != nil {
		return fail(io, ExitUsage, "%v", err)
	}
	if cfg.Scheme == "http" {
		fmt.Fprintln(io.Stderr, "WARNING: plain HTTP: the credential and every message are readable by anything on the network path.")
	}

	logf := func(format string, args ...any) {
		fmt.Fprintf(io.Stderr, "%s %s\n", time.Now().UTC().Format(time.RFC3339), terminalText(fmt.Sprintf(format, args...)))
	}
	client := gw.New(gw.Options{Scheme: cfg.Scheme, Host: cfg.Host, Pin: cfg.Pin, Credential: credential})
	defer client.Close()
	rig := &rigcli.Rig{
		Path: cfg.RigPath, EnvPath: cfg.RigEnvPath, Home: cfg.Home, OpenRigHome: cfg.OpenRigHome, OpenRigURL: cfg.OpenRigURL,
		Seat: cfg.OperatorSeat, ReadyTimeout: time.Duration(cfg.ReadyCheckTimeoutMS) * time.Millisecond,
		Runner: rigcli.ExecRunner{},
	}
	sc := &sidecar.Sidecar{Cfg: cfg, GW: client, Seat: rig, RigPath: cfg.RigPath, Version: Version, Logf: logf}
	defer sc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logf("ratatoskr-openrig %s starting (UNTESTED); operator seat %s", Version, cfg.OperatorSeat)
	if exit, err := sc.Start(ctx); err != nil || exit != sidecar.ExitOK {
		if err != nil {
			logf("not starting: %v", err)
		}
		return exit
	}
	if ctx.Err() != nil {
		return ExitOK
	}
	listener, err := ipc.Listen(cfg.SocketPath())
	if err != nil {
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			return fail(io, ExitUsage, "a sidecar is already running (the socket answers)")
		}
		return fail(io, ExitUsage, "%v", err)
	}
	defer func() { _ = os.Remove(cfg.SocketPath()) }()
	logf("listening on the local socket; polling the gateway")
	exit := sc.Run(ctx, listener)
	logf("stopped (exit %d)", exit)
	return exit
}
