// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/runtime"
)

const adminUsage = `Usage: muster admin reset-password --actor <name> <login>
       muster admin reset-totp --actor <name> <login>

Emergency access (C-03.FR-11). --actor names the person who runs the command and is required; flags come before the
login. Both record the change in the Audit log with the --actor name and the Transport cli.

reset-password reads the new password from standard input — without echo on a terminal, otherwise the first line —
sets it on the account with the login and ends the account's sessions.

reset-totp removes the TOTP and the recovery codes of the account with the login, for a lost second factor, and ends
the account's sessions; under a TOTP policy that covers the account, it enrols again at the next sign-in.
`

// maxPasswordInput bounds the line read from a pipe.
const maxPasswordInput = 4096

// The runtime entry points and standard input, replaced by tests.
var (
	runResetPassword           = runtime.ResetPassword
	runResetTOTP               = runtime.ResetTOTP
	stdin            io.Reader = os.Stdin
)

// runAdmin is `muster admin <command>`; development is set under `muster dev`, whose development clock the command
// then follows.
func runAdmin(args []string, stdout, stderr io.Writer, development bool) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return exitUsage
	}
	switch args[0] {
	case "reset-password":
		return runResetPasswordCommand(args[1:], stdout, stderr, development)
	case "reset-totp":
		return runResetTOTPCommand(args[1:], stdout, stderr, development)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, adminUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "muster admin: unknown command %q\n\n%s", args[0], adminUsage)
		return exitUsage
	}
}

// runResetPasswordCommand is `muster admin reset-password --actor <name> <login>`. Without --actor, or with anything
// but one login, it exits 2 before it reads the password or connects.
func runResetPasswordCommand(args []string, stdout, stderr io.Writer, development bool) int {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	actor := fs.String("actor", "", "")
	if err := fs.Parse(args); err != nil {
		return adminUsageError(stderr, "reset-password", err.Error())
	}
	name := strings.TrimSpace(*actor)
	switch {
	case name == "":
		return adminUsageError(stderr, "reset-password", "--actor is required")
	case fs.NArg() != 1 || fs.Arg(0) == "":
		return adminUsageError(stderr, "reset-password", "give exactly one login after the flags")
	}
	login := fs.Arg(0)
	password, err := readPassword(stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "muster admin reset-password: %v\n", err)
		return exitFailure
	}
	ctx, stop := signals()
	defer stop()
	context.AfterFunc(ctx, stop)
	id, err := runResetPassword(ctx, runtime.Options{Environ: environ(), Stdout: stdout, Development: development},
		runtime.PasswordReset{
			Actor: name, Login: login, Password: password,
		})
	if err != nil {
		fmt.Fprintf(stderr, "muster admin reset-password: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stderr, "muster admin reset-password: the password of %s (%s) is set and its sessions ended\n", login, id)
	return exitOK
}

// readPassword reads the new password: on a terminal after a prompt and without echo, otherwise the first line of r.
func readPassword(r io.Reader, prompt io.Writer) (logging.Secret, error) {
	if f, ok := r.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(prompt, "New password: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("read the password: %w", err)
		}
		return logging.Secret(b), nil
	}
	line, err := bufio.NewReader(io.LimitReader(r, maxPasswordInput)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the password: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("standard input carries no password")
	}
	return logging.Secret(line), nil
}

// runResetTOTPCommand is `muster admin reset-totp --actor <name> <login>`. Without --actor, or with anything but one
// login, it exits 2 before it connects.
func runResetTOTPCommand(args []string, stdout, stderr io.Writer, development bool) int {
	fs := flag.NewFlagSet("reset-totp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	actor := fs.String("actor", "", "")
	if err := fs.Parse(args); err != nil {
		return adminUsageError(stderr, "reset-totp", err.Error())
	}
	name := strings.TrimSpace(*actor)
	switch {
	case name == "":
		return adminUsageError(stderr, "reset-totp", "--actor is required")
	case fs.NArg() != 1 || fs.Arg(0) == "":
		return adminUsageError(stderr, "reset-totp", "give exactly one login after the flags")
	}
	login := fs.Arg(0)
	ctx, stop := signals()
	defer stop()
	context.AfterFunc(ctx, stop)
	id, removed, err := runResetTOTP(ctx, runtime.Options{Environ: environ(), Stdout: stdout, Development: development},
		runtime.TOTPReset{
			Actor: name, Login: login,
		})
	if err != nil {
		fmt.Fprintf(stderr, "muster admin reset-totp: %v\n", err)
		return exitFailure
	}
	if !removed {
		fmt.Fprintf(stderr, "muster admin reset-totp: %s (%s) has no TOTP; nothing changed\n", login, id)
		return exitOK
	}
	fmt.Fprintf(stderr, "muster admin reset-totp: the TOTP of %s (%s) is removed and its sessions ended\n", login, id)
	return exitOK
}

func adminUsageError(stderr io.Writer, command, msg string) int {
	fmt.Fprintf(stderr, "muster admin %s: %s\n\n%s", command, msg, adminUsage)
	return exitUsage
}
