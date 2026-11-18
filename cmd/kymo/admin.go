package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/term"

	"github.com/emirhanusta/kymo/internal/auth"
	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/repository/timescale"
)

// runCLI handles `kymo <subcommand>`; the server itself runs with no arguments.
func runCLI(args []string) int {
	switch args[0] {
	case "admin":
		return runAdmin(args[1:], os.Stdin, os.Stdout, os.Stderr)
	case "help", "-h", "--help":
		fmt.Println("usage: kymo                                  start the server\n       kymo admin reset-password (--username <name> | --email <email>) [--password-stdin]")
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown command %q (run kymo with no arguments to start the server)\n", args[0])
	return 2
}

func runAdmin(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "reset-password" {
		fmt.Fprintln(stderr, "usage: kymo admin reset-password (--username <name> | --email <email>) [--password-stdin]")
		return 2
	}
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	username := fs.String("username", "", "username of the account")
	email := fs.String("email", "", "email of the account (alternative to --username)")
	fromStdin := fs.Bool("password-stdin", false, "read the new password from stdin (one line) without prompting")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *username == "" && *email == "" {
		fmt.Fprintln(stderr, "--username or --email is required")
		return 2
	}
	password, err := readNewPassword(stdin, stdout, *fromStdin)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "failed to load configuration:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := timescale.New(ctx, cfg.Database.DSN, 2, zap.NewNop())
	if err != nil {
		fmt.Fprintln(stderr, "database connection failed:", err)
		return 1
	}
	defer db.Close()
	svc, err := auth.New(db.Auth(), auth.Options{Enabled: true}, zap.NewNop())
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if err := auth.ResetPassword(ctx, db.Auth(), *username, *email, password, svc); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintln(stdout, "password updated; all sessions of that user were signed out")
	return 0
}

// readNewPassword reads the password from a single stdin line (--password-stdin
// or a non-terminal stdin) or prompts twice on a terminal.
func readNewPassword(stdin *os.File, out io.Writer, fromStdin bool) (string, error) {
	if !fromStdin && term.IsTerminal(int(stdin.Fd())) {
		fmt.Fprint(out, "New password: ")
		a, err := term.ReadPassword(int(stdin.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		fmt.Fprint(out, "Repeat password: ")
		b, err := term.ReadPassword(int(stdin.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		if string(a) != string(b) {
			return "", errors.New("passwords do not match")
		}
		return string(a), nil
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no password on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
