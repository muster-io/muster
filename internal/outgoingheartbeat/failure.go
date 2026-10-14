// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outgoingheartbeat

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/muster-io/muster/internal/outbound"
)

// ClassInternal is the class of a heartbeat that Muster could not prepare, such as one whose URL it cannot decrypt;
// the other classes of OutgoingHeartbeatResult.last_error_class are the outcomes and the network failures of
// internal/outbound.
const ClassInternal = "internal"

// The texts of a failure that send stores, which never name the URL.
const (
	failedURL      = "the URL cannot be decrypted"
	failedPassword = "the proxy password cannot be decrypted"
	failedProxy    = "the stored proxy cannot be read"
	failedClient   = "the outbound client cannot be built: "
	failedRequest  = "the request failed"
	answeredFormat = "answered %d (%s)"
	blockedPrefix  = "blocked by the outbound address policy: "
)

var (
	answered = regexp.MustCompile(`^answered ([0-9]{3}) \(([a-z_]+)\)$`)
	// answeredClasses are the outcomes of an answer that the API names; any other is unknown.
	answeredClasses = []outbound.Outcome{outbound.OutcomeRetryAfter, outbound.OutcomeTransient,
		outbound.OutcomeFatal, outbound.OutcomeRedirect, outbound.OutcomeUnknown}
	networks = []string{outbound.NetworkDNS, outbound.NetworkTLS, outbound.NetworkTimeout,
		outbound.NetworkProxyAuth, outbound.NetworkConnect}
	// outcomes are the outcomes that masked writes alone, without an answer or a network failure.
	outcomes = []outbound.Outcome{outbound.OutcomeRetryAfter, outbound.OutcomeTransient, outbound.OutcomeFatal,
		outbound.OutcomeRedirect, outbound.OutcomeUnknown, outbound.OutcomeBlocked}
	internal = []string{failedURL, failedPassword, failedProxy}
)

// Failure is why an outgoing heartbeat failed: its class (last_error_class) and the status code of the answer, 0
// without one.
type Failure struct {
	Class  string
	Status int
}

// FailureOf reads the class and the status back from the stored error text of a failed heartbeat, in the forms that
// send and masked write: "answered <status> (<outcome>)", the refusal of the outbound address policy,
// "<network>: <detail>", an outcome alone, or a request Muster could not prepare. Any other text is unknown.
func FailureOf(text string) Failure {
	if m := answered.FindStringSubmatch(text); m != nil {
		status, _ := strconv.Atoi(m[1])
		class := outbound.OutcomeUnknown
		if o := outbound.Outcome(m[2]); slices.Contains(answeredClasses, o) {
			class = o
		}
		return Failure{Class: string(class), Status: status}
	}
	if strings.HasPrefix(text, blockedPrefix) {
		return Failure{Class: string(outbound.OutcomeBlocked)}
	}
	if network, _, ok := strings.Cut(text, ": "); ok && slices.Contains(networks, network) {
		return Failure{Class: network}
	}
	if slices.Contains(outcomes, outbound.Outcome(text)) {
		return Failure{Class: text}
	}
	if slices.Contains(internal, text) || strings.HasPrefix(text, failedClient) {
		return Failure{Class: ClassInternal}
	}
	return Failure{Class: string(outbound.OutcomeUnknown)}
}
