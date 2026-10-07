// Command run-app is the mechanical start/stop for the local deckshare dev stack -- the compose
// db and app services (#274) -- and the DB reset used when tests hit stale state from a prior
// run-app session (issue #95). The app runs as a container built from this checkout, applying its
// own migrations at startup; reset-db still prepares the host-side database with the goose CLI so
// cmd/seed has a schema. See .claude/skills/run-app/SKILL.md for the one judgment call this doesn't
// automate (port-3000 conflict on start).
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
	mediaName  = ".media"
	readyTries = 30
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

// composeAppRunning reports whether this checkout's own app container is up, in which case a bound
// port 3000 is expected and `start` should rebuild it in place rather than refuse.
func composeAppRunning() bool {
	out, err := exec.Command("docker", "compose", "ps", "-q", "--status", "running", "app").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func start() error {
	if existing, err := portPID(); err != nil {
		return err
	} else if existing != "" && !composeAppRunning() {
		fmt.Printf("PORT_IN_USE pid=%s — inspect with PowerShell Get-Process before deciding to kill or reuse it.\n", existing)
		os.Exit(2)
	}

	// compose bind-mounts this directory into the app container; left to Docker, a missing source
	// is created root-owned.
	if err := os.MkdirAll(filepath.Join(skillDir, mediaName), 0o755); err != nil {
		return err
	}

	// --build rebuilds the image from this checkout, so a code change is never served by a stale
	// container; --wait returns once db is healthy and app is running. The app applies its own
	// migrations before it listens.
	if err := runVisible("docker", "compose", "up", "-d", "--build", "--wait"); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	var code string
	for i := 0; i < readyTries; i++ {
		if code = probeStatus(); code == "303" {
			fmt.Printf("started http_status=%s (expect 303 -> /login)\n", code)
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("app answered %s, want 303 on :%d - see 'docker compose logs app'", code, port)
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
	if err := runVisible("docker", "compose", "down"); err != nil {
		return fmt.Errorf("docker compose down: %w", err)
	}

	// Checked after compose is down, so what is still bound is not our container -- typically a
	// host binary left over from before the app moved into compose (#274).
	remaining, err := portPID()
	if err != nil {
		return err
	}
	if remaining != "" {
		fmt.Printf("STILL_LISTENING pid=%s — not killed automatically, verify before Stop-Process.\n", remaining)
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
	// Only db: the seed below needs the schema before the app exists, so the CLI applies it here
	// and the app is left down for start() to bring back up.
	if err := runVisible("docker", "compose", "up", "-d", "--wait", "db"); err != nil {
		return fmt.Errorf("docker compose up db: %w", err)
	}

	if err := runWithDatabaseURL("goose", "-dir", "migrations", "postgres", dbURL, "up"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	// The directory compose bind-mounts into the app container (compose.yaml): the seed's avatar
	// step writes a blob there, and a mismatched directory would leave GET /settings/avatar 404ing
	// against the app's actual media root.
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

	fmt.Println("reset complete: fresh DB, migrations applied, test user/decks seeded - run 'start' to bring the app back up")
	return nil
}
