# Notification gateways

A **Notification gateway** reaches people outside messengers — by phone call or SMS, for example — through a provider
of its own. It is a bridge in the role `gateway`: a service that speaks the [Bridge protocol](../reference/bridge-protocol.md)
with Muster and calls or texts people through your provider. Muster ships no gateway and never calls or texts anyone
by itself: you run a gateway written for your provider, and Muster sends it the person's contact and a plain text.

A gateway does not depend on a messenger, so it still reaches a person whose messenger is down or silent at night.

In the examples, `MUSTER_PUBLIC_URL` is `https://muster.example.org`, and the contacts are numbers such as
`+10000000001`, which reach nobody.

## Connect a Notification gateway

A Notification gateway is a [Bridge Connection](bridge.md) whose bridge declares the role `gateway` in its `info`:

1. Run the gateway bridge. To write one for your provider, read [Write a bridge](write-a-bridge.md): a gateway
   implements `info`, `sendNotification`, `answerEvent` and events in at least one delivery mode.
2. Create a Bridge Connection for it, with its base URL and the token of its bot, as
   [Connect a messenger through a bridge](bridge.md#create-the-connection) describes.
3. Check the Connection. The check reads the roles from the bridge's `info` and shows **Notification gateway**; the
   read of the Connection has `"roles": ["gateway"]`, and `GET /api/v1/connections?role=gateway` lists it.

A Bridge Connection whose only role is `gateway` cannot be a Destination and is not offered for Account links
([Roles](bridge.md#roles)). A bridge may have both roles, `messenger` and `gateway`.

`muster dev` runs the demo Connection "Example Gateway" on its fake bridge, whose `info` declares only the role
`gateway`; the fake bridge lists what it was sent at `/_fake/notifications`.

## Contact for calls and SMS

A gateway calls or texts a person at their **contact**: a phone number or another address the gateway understands, as
text. Muster does not check phone numbers. Each person has at most one confirmed contact per Notification gateway, and
only a confirmed contact is called.

### Add and confirm a contact

A person adds their own contact from their signed-in web session, and confirms it with a code that the gateway
delivers:

1. Add the contact for the gateway (`createMyGatewayContact`, `POST /api/v1/me/gateway-contacts`, with
   `connection_id` and `contact`). The contact is trimmed and holds 1 to 200 characters. Muster stores it
   unconfirmed and sends a code through the gateway with `sendNotification`, without action data, in the language of
   the person's profile: "Your Muster code: 123456. It is valid for 10 minutes. Do not share it."
2. Type the code back, in the same web session (`confirmMyGatewayContact`, `POST
   /api/v1/me/gateway-contacts/{id}/confirmation`, with `code`). The contact is confirmed.
3. To get another code, ask for one (`sendMyGatewayContactCode`, `POST /api/v1/me/gateway-contacts/{id}/code`); once
   the gateway accepted it, it voids the earlier codes of the contact. A code the gateway did not take leaves the
   earlier one valid.

The code (`gateway.contact_code`):

Length
:   6 digits.

Validity
:   10 minutes after it was asked for.

Attempts
:   At most 5. A wrong code answers `422` `contact_code_invalid` with `attempts_remaining`; after the fifth wrong code,
    once the code expired or was used, and once a newer code was sent, the code is void.

Session
:   A code is checked only in the web session that asked for it. The right code typed in another session is refused
    and counts as an attempt; a session that ends voids its codes.

Code requests — adding a contact and asking for a new code — are limited (`gateway.contact_code_requests`):

Per person
:   5 within an hour.

Per contact
:   3 within an hour for one contact, as typed and trimmed, whoever asks.

A request beyond them answers `429` `contact_code_requests_exceeded` with `Retry-After`; a request whose code the
gateway could not deliver still counts. The code is sent on the interactive path, since the person waits for it: when
no rate-limit token of the gateway is free within `delivery.interactive_budget`, the answer is `503` with
`Retry-After`, and when the gateway refuses the notification, `502` `gateway_refused` with its `gateway_class`. The
contact stays, unconfirmed, and a new code can be asked for.

### Change a contact

Add the new contact for the same gateway. Until it is confirmed, the confirmed contact stays the one called, and both
are listed, the new one naming the confirmed one in `replaces`; confirming the new contact replaces the old one, so
that changing a number never leaves a person unreachable. Adding another contact before the new one is confirmed
replaces the unconfirmed one. A person removes a contact with `deleteMyGatewayContact`.

### Admins

An Admin sees the contacts of any User (`listUserGatewayContacts`, `GET /api/v1/users/{id}/gateway-contacts`) and
removes one (`deleteUserGatewayContact`), but never sets one: `createUserGatewayContact` always answers `403`, "Only the
person sets their contact, from their own web session.", so that nobody can tie a contact to another person's User.
The operations that change a person's own contacts take the web session only; an API token, an Admin's included, gets
`403` `session_required`.

### Privacy

A contact is personal data, not a secret: the person, Admins and the gateway read it. Muster does not log it, and the
[Request log](../operations/request-log.md#masking) shows the contact and the text of every request to
`sendNotification` as `[redacted]`, the code included. Adding, confirming and removing a contact are recorded in the
Audit log. Deleting a User erases their contacts and codes, and the Audit log then shows their earlier contacts as
`[erased]`.
