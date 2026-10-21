# Outgoing heartbeat

The metrics and the Internal alerts tell you about a Muster that runs. They cannot tell you that Muster, its node, its
cluster or its network is gone: a dead service raises nothing. For that, Muster calls a URL of your choice at a regular
interval, and a service outside Muster alerts you when the calls stop. This is a **dead man's switch**, and the call is
the **outgoing heartbeat**. It is the opposite direction of a [Heartbeat](../integrations/heartbeat.md), which watches the
path *to* Muster.

## Pick a service

Any service that expects a regular HTTP request and alerts when it misses, and that sits outside the cluster Muster runs
in, will do:

- **Uptime Kuma**: a monitor of the type *Push*; it gives a URL and alerts when no call arrives within the interval.
- Any dead man's switch service that accepts an HTTP `GET` and alerts when the calls stop.

Set its expected period a little above Muster's interval of 1 minute, such as 2 or 3 minutes, so that a single slow call
does not alert. Send its alert somewhere that does not depend on Muster.

## Set it up

Open **Organization → Outgoing heartbeat**, as an Admin.

1. Enter the **Heartbeat URL** of the service.
2. If Muster must reach it through a proxy, fill the proxy form; it is the same form as everywhere, see
   [Proxies](../messengers/proxies.md).
3. Save.

The URL is a **Secret**: such URLs carry a token. Muster encrypts it with the Keyring, never shows it again, and removes
it from every log line and error. The page shows whether it is set, and the Organization settings have no way to read it
back. To change it, enter the whole new URL.

Without a URL nothing is sent, and **System status** suggests setting one up: "Set up an outgoing heartbeat so that a
stopped Muster is noticed."

## What Muster sends

Interval
:   1 minute (`outgoing_heartbeat.interval`)

Method
:   `GET` (`outgoing_heartbeat.method`)

Timeout
:   10 seconds for one request (`outgoing_heartbeat.timeout`)

Retry
:   None: a failure is counted and logged, and the next tick is the retry.

Only the **Leader** sends it, so it stops when no replica leads, and it stops when Muster is down. Both are what you want
a dead man's switch to see. See [High availability](high-availability.md#the-leader). The call goes through the
[outbound address policy](../messengers/proxies.md#what-the-outbound-address-policy-checks) like every other outbound
request, so a service on a private network needs an allowed entry under **Organization → Outbound addresses** when the
policy is strict.

## What the System status page shows

**Organization → System status** has a section *Outgoing heartbeat* with the last result:

- *Last sent {time}: OK*
- *Last sent {time}: {reason}*, where the reason is, for example, that the host name does not resolve, that no answer came
  in time, that the TLS handshake failed or that the URL refused the request
- *Not sent yet*, right after a start or a change

The settings page shows the same as *Last result: OK, sent {time}*, *Last result: not sent yet* and *Last result: nothing
is sent without a URL*. Check it after you save: it proves that Muster reaches the service.
