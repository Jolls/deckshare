// Command run-app is the mechanical start/stop for the local deckshare dev stack, and the
// DB reset used when tests hit stale state from a prior run-app session (issue #95).
// See .claude/skills/run-app/SKILL.md for the one judgment call this doesn't automate
// (port-3000 conflict on start).
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	port       = 3000
	dbURL      = "postgres://root:mysecretpassword@localhost:5432/local"
	skillDir   = ".claude/skills/run-app"
	binName    = ".deckshare-server.exe"
	pidName    = ".server.pid"
	logName    = ".server.log"
	mediaName  = ".media"
	readyTries = 10
)

func main() {
	if len(os.Args) != 2 {
		usage()
	}

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		fatal(err)
	}

	switch os.Args[1] {
	case "start":
		err = start()
	case "stop":
		err = stop()
	case "status":
		err = status()
	case "reset-db":
		err = resetDB()
	default:
		usage()
	}
	if err != nil {
		fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: run-app {start|stop|status|reset-db}")
	os.Exit(1)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("finding repo root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// portPID returns the PID of the process listening on port, or "" if none is found.
// Parses `netstat -ano` directly rather than through a shell grep/awk pipeline, which
// is the flakiest part of the previous bash implementation on Windows/git-bash.
func portPID() (string, error) {
	out, err := exec.Command("netstat", "-ano").Output()
	if err != nil {
		return "", fmt.Errorf("netstat: %w", err)
	}
	suffix := fmt.Sprintf(":%d", port)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		proto, local, state, pid := fields[0], fields[1], fields[len(fields)-2], fields[len(fields)-1]
		if proto != "TCP" || state != "LISTENING" {
			continue
		}
		if strings.HasSuffix(local, suffix) {
			return pid, nil
		}
	}
	return "", nil
}

func waitForPostgres(cid string) bool {
	for i := 0; i < readyTries; i++ {
		if exec.Command("docker", "exec", cid, "pg_isready", "-U", "root", "-d", "local").Run() == nil {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

func dbContainerID() (string, error) {
	out, err := exec.Command("docker", "compose", "ps", "-q", "db").Output()
	if err != nil {
		return "", fmt.Errorf("docker compose ps: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func runVisible(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runWithDatabaseURL(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dbURL)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func start() error {
	if existing, err := portPID(); err != nil {
		return err
	} else if existing != "" {
		fmt.Printf("PORT_IN_USE pid=%s — inspect with PowerShell Get-Process before deciding to kill or reuse it.\n", existing)
		os.Exit(2)
	}

	if err := runVisible("docker", "compose", "up", "-d", "db"); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	cid, err := dbContainerID()
	if err != nil {
		return err
	}
	waitForPostgres(cid)

	if err := runWithDatabaseURL("goose", "-dir", "migrations", "postgres", dbURL, "up"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	bin := filepath.Join(skillDir, binName)
	if err := runVisible("go", "build", "-o", bin, "./cmd/deckshare"); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	mediaRoot := filepath.Join(skillDir, mediaName)
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		return err
	}

	logFile, err := os.Create(filepath.Join(skillDir, logName))
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	absBin, err := filepath.Abs(bin)
	if err != nil {
		return err
	}
	cmd := exec.Command(absBin)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ADDR=:%d", port),
		"DATABASE_URL="+dbURL,
		"MEDIA_ROOT="+mediaRoot,
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting server: %w", err)
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(filepath.Join(skillDir, pidName), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return err
	}
	// cmd.Process must be released, not waited on, so the server keeps running
	// independently of this short-lived process.
	if err := cmd.Process.Release(); err != nil {
		return err
	}

	time.Sleep(time.Second)
	code := probeStatus()
	fmt.Printf("started pid=%d http_status=%s (expect 303 -> /login)\n", pid, code)
	return nil
}

func probeStatus() string {
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Head(fmt.Sprintf("http://localhost:%d/", port))
	if err != nil {
		return "ERR:" + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	return strconv.Itoa(resp.StatusCode)
}

func stop() error {
	pidPath := filepath.Join(skillDir, pidName)
	if data, err := os.ReadFile(pidPath); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
		_ = os.Remove(pidPath)
	}

	remaining, err := portPID()
	if err != nil {
		return err
	}
	if remaining != "" {
		fmt.Printf("STILL_LISTENING pid=%s — not killed automatically, verify before Stop-Process.\n", remaining)
	}

	if err := runVisible("docker", "compose", "down"); err != nil {
		return fmt.Errorf("docker compose down: %w", err)
	}
	fmt.Println("stopped")
	return nil
}

func status() error {
	pid, err := portPID()
	if err != nil {
		return err
	}
	if pid != "" {
		fmt.Printf("port %d listening, pid=%s\n", port, pid)
	} else {
		fmt.Printf("port %d free\n", port)
	}
	return nil
}

func resetDB() error {
	if err := runVisible("docker", "compose", "down", "-v"); err != nil {
		return fmt.Errorf("docker compose down -v: %w", err)
	}
	if err := runVisible("docker", "compose", "up", "-d", "db"); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	cid, err := dbContainerID()
	if err != nil {
		return err
	}
	if !waitForPostgres(cid) {
		return fmt.Errorf("postgres did not become ready in time")
	}

	if err := runWithDatabaseURL("goose", "-dir", "migrations", "postgres", dbURL, "up"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	// Same MEDIA_ROOT the running server uses (start(), above): the seed's avatar step writes a
	// blob there, and a mismatched directory would leave GET /settings/avatar 404ing against the
	// server's actual media root.
	mediaRoot := filepath.Join(skillDir, mediaName)
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		return err
	}
	seedCmd := exec.Command("go", "run", "./cmd/seed")
	seedCmd.Env = append(os.Environ(), "DATABASE_URL="+dbURL, "MEDIA_ROOT="+mediaRoot)
	seedCmd.Stdout = os.Stdout
	seedCmd.Stderr = os.Stderr
	if err := seedCmd.Run(); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	fmt.Println("reset complete: fresh DB, migrations applied, test user/decks seeded")
	return nil
}
