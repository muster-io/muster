# Proxies

Muster has no proxy for the whole installation. Each client that talks to the outside has a proxy setting of its own, so
that traffic that must go through a proxy does, and traffic that must not, such as the identity provider's, goes
straight. `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are **never read**, in any process of Muster.

## Where you set a proxy

| Client | Where |
|---|---|
| a Mattermost, Telegram or Bridge Connection | the Connection's proxy section |
| an outgoing webhook Destination | the Destination's proxy section |
| the identity provider (OpenID Connect) | the OIDC settings |
| the Organization's outgoing heartbeat | its settings |

The setting is off by default. Every one of these has the same form:

- **Use a proxy**, a switch. Switching it off keeps the other fields, so that you can switch it on again.
- **Type**: `http`, `https` or `socks5`.
- **Address**, as `host:port` with a port from 1 to 65535.
- **Username** and **password**, when the proxy needs them. The username must not contain a colon or a line break and is
  at most 255 bytes long.

The three types differ in how Muster talks to the proxy:

| Type | Muster speaks to the proxy | Notes |
|---|---|---|
| HTTP | plain HTTP | for an HTTPS target, Muster asks the proxy for a tunnel with `CONNECT` |
| HTTPS | HTTP over TLS | the connection to the proxy is encrypted |
| SOCKS5 | the SOCKS5 protocol | the proxy resolves the name of the target |

## The password

The proxy password is a secret like the others: it is encrypted with the master key, write-only in the API and the web
UI (they show whether it is set, never the value), and replaced with `[redacted]` in every log line and error. A stored
password is used only with the proxy type and address it was saved with. When you change either, type the password
again.

## Checks take the same path

The connection check of a Connection, and the test of a Destination, go through the saved proxy just as real traffic
does. If the check works, delivery works by the same route. A check run from the form before saving uses the proxy in
the form instead. A token that is already stored goes only to the saved address of its Connection, through its saved
proxy: with a changed but unsaved address or proxy and no token typed, the check makes only a probe without the
token.

## What the outbound address policy checks

Every outbound connection of Muster is checked by the Organization's outbound address policy, **standard** or
**strict**, with its lists of allowed and denied networks, host names and domains. The Admins edit the policy under
Organization → Outbound addresses. Through a proxy, two things are checked:

1. **The proxy's own address.** It is checked like any other target, after name resolution. A proxy setting cannot reach
   a link-local or cloud metadata address, such as `169.254.169.254`, and a proxy on a loopback address works only when an
   allowed network contains it.
2. **The target.** The proxy resolves the target's name, so Muster resolves it too, and every address it gets must pass
   the policy; for a target given as an IP address, that address is checked. A request to a name that resolves to a
   blocked address is refused before it is sent, without retry, and the error names the rule.

In a closed network, only the proxy may be able to resolve outside names. When Muster cannot resolve the target's name
itself, it checks the name instead: a name that matches a denied entry is blocked, and in strict mode only a name that
matches an allowed entry passes. A request that passes is sent without an address check, and Muster logs that.

Muster cannot see where the proxy finally connects to. The Admin who sets the proxy chooses to trust it.

## Telegram

Telegram is often reached through a proxy where it is blocked. [Telegram in restricted
networks](telegram-restricted-networks.md) shows the proxy on the Connection, and the alternatives: a reverse proxy
under your own domain and a self-hosted Bot API server.
