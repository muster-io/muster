// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"unicode"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/logging"
)

const bridgeUsage = `Usage: muster bridge check <url> --token-file <file> --channel <id> [flags]

Runs the Bridge protocol against the bridge at <url> (C-22.FR-21): the steps info, channel_check, post, replayed_key,
buttons, edit, thread_reply, thread_ref, forbidden_channel, user_lookup, direct_message, delete, poll, press, push and
answer, in this order, each printed as one line PASS <step>, FAIL <step>: <reason> or SKIP <step>: <why> as soon as it
ends. A failed info ends the run. An interactive run against a bridge that offers push opens a push listener and prints
PUSH <url> <secret>, or PUSH <url> with --push-secret-file, to point the bridge at before anything is pressed; before
it waits for a person, up to five minutes, it prints WAIT <step>: <what to do>, message_id <id>. The check needs no
database, master key or configuration file, writes nothing and never changes the bridge's configuration.

Flags, before or after <url>:
  --token-file <file>        the bridge's token, alone in the file (required)
  --channel <id>             a channel the bot can post to (required)
  --forbidden-channel <id>   a channel the bot cannot use; without it forbidden_channel is skipped
  --handle <handle>          a person to look up and write to; without it user_lookup and direct_message are skipped
  --push-listen <addr>       where the push listener listens (default 127.0.0.1:0)
  --push-url <url>           where the bridge reaches the push listener (default: the listener's own address)
  --push-secret-file <file>  the push secret, whsec_ followed by base64, instead of a generated one
  --no-interactive           skip press and push, which need a person

Exits 0 when no step failed, 1 when one did, 2 on a usage error.
`

// maxSecretFile bounds what a token or push secret file holds.
const maxSecretFile = 64 << 10

// runBridge is `muster bridge <command>`.
func runBridge(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, bridgeUsage)
		return exitUsage
	}
	switch args[0] {
	case "check":
		ctx, stop := signals()
		defer stop()
		context.AfterFunc(ctx, stop)
		return BridgeCheckCommand(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, bridgeUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "muster bridge: unknown command %q\n\n%s", args[0], bridgeUsage)
		return exitUsage
	}
}

// BridgeCheckCommand is `muster bridge check` with the arguments after check; the secret-leak probe of lint 5 runs it.
// A broken invocation — no URL or one that is not a bridge's base URL, a missing required flag, or a token or push
// secret file that cannot be read or holds more than the secret — exits 2 before any request, so that it never looks
// like a broken bridge. The token and the push secret appear in no line it prints, only a push secret it generated.
func BridgeCheckCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tokenFile := fs.String("token-file", "", "")
	channel := fs.String("channel", "", "")
	forbidden := fs.String("forbidden-channel", "", "")
	handle := fs.String("handle", "", "")
	pushListen := fs.String("push-listen", bridge.DefaultPushListen, "")
	pushURL := fs.String("push-url", "", "")
	pushSecretFile := fs.String("push-secret-file", "", "")
	noInteractive := fs.Bool("no-interactive", false, "")
	var target string
	for rest := args; ; {
		if err := fs.Parse(rest); errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, bridgeUsage)
			return exitOK
		} else if err != nil {
			return bridgeUsageError(stderr, err.Error())
		}
		if fs.NArg() == 0 {
			break
		}
		if target != "" {
			return bridgeUsageError(stderr, "give exactly one bridge URL")
		}
		target, rest = fs.Arg(0), fs.Args()[1:]
	}
	base, err := bridge.ParseBaseURL(target)
	switch {
	case target == "":
		return bridgeUsageError(stderr, "the bridge URL is required")
	case err != nil:
		return bridgeUsageError(stderr, "the bridge URL is not an absolute http or https URL without user "+
			"information, query or fragment")
	case *tokenFile == "":
		return bridgeUsageError(stderr, "--token-file is required")
	case strings.TrimSpace(*channel) == "":
		return bridgeUsageError(stderr, "--channel is required")
	}
	if _, _, err := net.SplitHostPort(*pushListen); err != nil {
		return bridgeUsageError(stderr, "--push-listen is not a host and port such as 127.0.0.1:0")
	}
	if *pushURL != "" {
		if u, err := url.Parse(*pushURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return bridgeUsageError(stderr, "--push-url is not an absolute http or https URL")
		}
	}
	token, err := readSecretFile("--token-file", *tokenFile)
	if err != nil {
		return bridgeUsageError(stderr, err.Error())
	}
	var pushSecret logging.Secret
	if *pushSecretFile != "" {
		if pushSecret, err = readSecretFile("--push-secret-file", *pushSecretFile); err != nil {
			return bridgeUsageError(stderr, err.Error())
		}
		if !bridge.ValidPushSecret(pushSecret) {
			return bridgeUsageError(stderr, "--push-secret-file does not hold whsec_ followed by base64")
		}
	}
	failed, err := bridge.RunConformance(ctx, bridge.ConformanceConfig{BaseURL: base, Token: token,
		PushSecret: pushSecret, Channel: strings.TrimSpace(*channel), ForbiddenChannel: strings.TrimSpace(*forbidden),
		Handle: strings.TrimSpace(*handle), PushListen: *pushListen, PushURL: *pushURL, Interactive: !*noInteractive,
		Out: stdout, Log: logging.New(stderr, logging.LevelInfo)})
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(stderr, "muster bridge check: interrupted")
		return exitFailure
	case err != nil:
		fmt.Fprintf(stderr, "muster bridge check: %v\n", err)
		return exitFailure
	case failed:
		return exitFailure
	}
	return exitOK
}

// readSecretFile reads the secret alone in the file path, given by the flag name: the error names the flag and the
// path, never what the file holds.
func readSecretFile(name, path string) (logging.Secret, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxSecretFile+1))
	switch {
	case err != nil:
		return "", fmt.Errorf("%s: %w", name, err)
	case len(b) > maxSecretFile:
		return "", fmt.Errorf("%s: the file is larger than 64 KiB", name)
	}
	s := strings.TrimSpace(string(b))
	switch {
	case s == "":
		return "", fmt.Errorf("%s: the file is empty", name)
	case strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return "", fmt.Errorf("%s: the file holds more than the secret alone on one line", name)
	}
	return logging.Secret(s), nil
}

func bridgeUsageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "muster bridge check: %s\n\n%s", msg, bridgeUsage)
	return exitUsage
}
