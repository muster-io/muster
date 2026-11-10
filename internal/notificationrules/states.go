// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package notificationrules

import "fmt"

// WithCall is the Set of the state call (C-31.FR-2, FR-5): the Organization's rules org with a call through the gateway
// gatewayID at the start of the important rule, together with its first action, so that both happen at the same
// moment — unless that rule already calls through that gateway, which keeps it as it is. The normal rule stays as it
// is. An important rule that would pass MaxActions is out_of_range at /state.
func WithCall(org Set, gatewayID string) (Set, *FieldError) {
	out := Set{Normal: clone(org.Normal), Important: clone(org.Important)}
	for _, a := range org.Important {
		if a.Kind == KindCall && a.GatewayID == gatewayID {
			return out, nil
		}
	}
	if len(org.Important)+1 > MaxActions {
		return Set{}, &FieldError{Pointer: "/state", Code: CodeOutOfRange,
			Detail: fmt.Sprintf("The Organization's important rule has %d actions; a call would pass the limit of %d.",
				len(org.Important), MaxActions)}
	}
	out.Important = append([]Action{{Kind: KindCall, GatewayID: gatewayID}}, out.Important...)
	return out, nil
}

// WithoutCalls is the Set of the state no_call (C-31.FR-2): the Organization's rules org without any call. A rule left
// without a Mention gets one in place of its first call and keeps its waits, so that every person can opt out of
// calls and every rule keeps an action that Pages (Open question 1 of S-152, settled by the maintainer).
func WithoutCalls(org Set) Set {
	return Set{Normal: withoutCalls(org.Normal), Important: withoutCalls(org.Important)}
}

func withoutCalls(r []Action) []Action {
	mentions := false
	for _, a := range r {
		if a.Kind == KindMention {
			mentions = true
		}
	}
	out := make([]Action, 0, len(r))
	for _, a := range r {
		if a.Kind != KindCall {
			out = append(out, a)
			continue
		}
		if !mentions {
			out = append(out, Action{Kind: KindMention})
			mentions = true
		}
	}
	return out
}

// switchGateway is the gateway of the state call, at /gateway_id: the one id names, which must be a Notification
// gateway at which the User has a confirmed contact, or without id the only gateway at which they have one.
func switchGateway(id *string, gws Gateways) (Gateway, *FieldError) {
	const at = "/gateway_id"
	live := gws.live()
	if len(live) == 0 {
		return Gateway{}, gatewayRequired(at)
	}
	if id == nil {
		var withContact []Gateway
		for _, gw := range live {
			if gw.Contact {
				withContact = append(withContact, gw)
			}
		}
		switch len(withContact) {
		case 0:
			return Gateway{}, contactOf(live[0], at, Check{Contacts: true})
		case 1:
			return withContact[0], nil
		}
		return Gateway{}, &FieldError{Pointer: at, Code: CodeRequired,
			Detail: "You have confirmed contacts at several Notification gateways; name the one the call goes through."}
	}
	gw, fe := callGateway(id, at, Check{Gateways: gws, Contacts: true})
	if fe != nil {
		// callGateway puts the errors of the gateway itself at at+"/gateway_id"; the switch has them at /gateway_id.
		fe.Pointer = at
		return Gateway{}, fe
	}
	out, _ := gws.byPublicID(gw)
	return out, nil
}

func clone(r []Action) []Action {
	return append([]Action{}, r...)
}
