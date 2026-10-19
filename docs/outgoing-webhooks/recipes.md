# Recipes

Four ways to use an outgoing webhook, built only from what it has: the [events mode](events.md) sends one signed event per
lifecycle event; the [template mode](templates.md) sends the requests you write and keeps their answers. Each recipe says
which mode it uses and why.

!!! note "These are sketches"
    The recipes show the shape of a setup, not a tested integration. The endpoints, fields and header names of the other
    system are examples, taken from the public documentation of an open tool. They change between versions and settings,
    so check them against the documentation of the version you run, and use **Send test message** and **Preview** on the
    Destination before you rely on it. Everything about Muster in them is real: the modes, the fields the templates read,
    the extraction rules, the Secrets and the signature.

Common to all four:

- **Use a Route of its own.** Create a Route whose Matchers pick only the Alerts you want to reach this system, for
  example `severity="critical"`, and give it the webhook as its Destination. The other Routes are unaffected. See [Routes
  and Group keys](../concepts/routes-and-group-keys.md).
- **Keep credentials in Secrets**, never in the URL or in a header as text. See [Secrets](events.md#secrets).
- **Template mode follows Storms.** While a Storm lasts, a template-mode Destination gets one Storm summary through its
  `create` request, and templates that read `.AlertGroup` fail for it. On a dedicated, narrow Route this rarely matters;
  otherwise branch on `.Storm` as [Recipe: Storm summaries](templates.md#recipe-storm-summaries) shows.
- **Look at the Request log** when something does not work: it shows the requests Muster sent and the answers, with
  secrets masked. See [Request log](../operations/request-log.md).

In the examples, `MUSTER_PUBLIC_URL` is `https://muster.example.org`.

## A phone call or an SMS

**Mode: events.** A provider with an HTTP API starts a call or sends a text when you post a small request to it. You want
that only for the changes that need a human, and you want to send it once. The events mode fits, because every event
carries `notify`, which is true exactly when the change is [Loud](../concepts/loud-and-quiet.md), and a `webhook-id` that
stays the same across retries.

Template mode does not fit here. It always has an `update` request, which sends again whenever the Alert Group changes,
and a call provider has nothing to update. It cannot decide to skip an event either.

Muster cannot know your provider's request, so put a small **relay** between them. The relay checks the signature, drops
duplicates, picks the events worth a call, and calls the provider.

1. Create a Destination of type `webhook`, mode `events`, with the relay's URL and a Secret `token` for the relay's own
   credential, as in [Create the Destination](events.md#create-the-destination). Keep the Signing secret that the answer
   shows once, and give it to the relay.
2. Create a Route that matches what should ring a phone, with the Destination, and an On-call profile.
3. Run the relay. This sketch is Python; the `verify` function is the one on [Verify the
   signature](events.md#verify-the-signature):

   ```python
   import json, os, urllib.request
   from http.server import BaseHTTPRequestHandler, HTTPServer

   CALL_ON = {"created", "ack_timeout", "reopened"}   # the events worth a call
   seen = set()                                        # webhook-ids; use a store with an expiry in real life

   class Relay(BaseHTTPRequestHandler):
       def do_POST(self):
           body = self.rfile.read(int(self.headers["Content-Length"]))
           if not verify(os.environ["SIGNING_SECRET"], self.headers["webhook-id"],
                         self.headers["webhook-timestamp"], body, self.headers["webhook-signature"]):
               self.send_response(401); self.end_headers(); return
           event = json.loads(body)
           wid = self.headers["webhook-id"]
           if wid not in seen and event["notify"] and event["event"] in CALL_ON:
               group = event["alert_group"]
               call = urllib.request.Request(
                   os.environ["PROVIDER_URL"],          # your provider's HTTP API
                   json.dumps({"to": os.environ["ON_CALL_NUMBER"],
                               "text": f"Muster #{group['number']}: {group['title']}"}).encode(),
                   {"Authorization": "Bearer " + os.environ["PROVIDER_TOKEN"],
                    "Content-Type": "application/json"})
               urllib.request.urlopen(call, timeout=10)  # an error here fails the request, and Muster retries the same event
               seen.add(wid)
           self.send_response(204); self.end_headers()

   HTTPServer(("", 8080), Relay).serve_forever()
   ```

What to know:

- **Which events.** `created` is a new Alert Group, `ack_timeout` a notice that nobody took it, and `reopened` a problem
  that returned. Leave out `alerts_added` and `reminder`, which are Loud too but only worth a message. The relay can
  also skip an Alert Group that is already acknowledged: every event carries its current `status`.
- **Answer honestly.** `2xx` marks the event delivered. A `5xx` makes Muster send the same event again with the same
  `webhook-id`. A `400` or `422` drops that one event. `401`, `403`, `404` and `410` make the Destination
  [Broken](../concepts/broken-destinations.md), which is right for a relay that is misconfigured. See [How the answer
  counts](events.md#how-the-answer-counts).
- **Test it.** **Send test message** sends the event `test`, which is not in `CALL_ON`. Add it for the first run, then
  remove it.

## A status page

**Mode: template.** A status page that has an HTTP API takes one incident per problem and lets you update it. That is the
shape of the template mode: `create` opens the incident and extracts its id from the answer, and `update` changes it to
the current state.

This sketch uses the incident API of the open status page Cachet 2.x as the example: a `POST` to `/api/v1/incidents`
answers `{"data": {"id": 7, …}}`, and a `PUT` to `/api/v1/incidents/7` changes it. Its `status` is 1 for investigating, 2
for identified and 4 for fixed. Create a Secret `token` with the page's API token.

`create`:

```
POST https://status.example.org/api/v1/incidents
X-Cachet-Token: {{ .Secrets.token }}
Content-Type: application/json
```

```
{"name": {{ .AlertGroup.Title | toJson }},
 "message": {{ .AlertGroup.Summary | toJson }},
 "status": 1,
 "visible": 1}
```

Extraction rule: the name `id`, the path `$.data.id`.

`update`:

```
PUT https://status.example.org/api/v1/incidents/{{ .Response.id }}
X-Cachet-Token: {{ .Secrets.token }}
Content-Type: application/json
```

```
{"name": {{ .AlertGroup.Title | toJson }},
 "message": {{ .AlertGroup.Summary | toJson }},
 "status": {{ if eq .AlertGroup.Status "resolved" }}4{{ else if eq .AlertGroup.Status "firing" }}1{{ else }}2{{ end }},
 "visible": 1}
```

The incident opens as investigating when an Alert Group appears, becomes identified while somebody has it or it is
snoozed, and becomes fixed when the Alert Group resolves. If the Alert Group reopens, the next `update` says investigating
again.

Think before you publish: the title and summary of an Alert Group come from your alert rules and may show internal names.
Use a Route with Matchers for the alerts that are meant for your users, and write the summary annotations for them.

## A second chat

**Mode: template.** A chat with an open REST API and incoming messages is a Destination of its own: the same Alert Groups,
in a second place. The template mode keeps one message per Alert Group in it, edits it where the chat's API allows, and
posts the replies of the Thread.

This sketch uses the REST API of Rocket.Chat as the example. A `POST` to `chat.postMessage` answers with the message under
`message`, with its `_id` and its room id `rid`; `chat.update` edits it; a message with a `tmid` is a reply in its thread.
Create the Secrets `token` and `user_id` for the bot user's token and id. Zulip works the same way, with a form-encoded
body: set the header `Content-Type: application/x-www-form-urlencoded` and build the body with `urlquery`.

`create`:

```
POST https://chat.example.org/api/v1/chat.postMessage
X-Auth-Token: {{ .Secrets.token }}
X-User-Id: {{ .Secrets.user_id }}
Content-Type: application/json
```

```
{"channel": "#alerts",
 "text": {{ printf "#%d %s (%s)\n%s" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status .AlertGroup.URL | toJson }}}
```

Extraction rules: `id` with `$.message._id`, and `room` with `$.message.rid`.

`update`:

```
POST https://chat.example.org/api/v1/chat.update
X-Auth-Token: {{ .Secrets.token }}
X-User-Id: {{ .Secrets.user_id }}
Content-Type: application/json
```

```
{"roomId": {{ .Response.room | toJson }},
 "msgId": {{ .Response.id | toJson }},
 "text": {{ printf "#%d %s (%s)\n%s" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status .AlertGroup.URL | toJson }}}
```

`reply_in_thread`:

```
POST https://chat.example.org/api/v1/chat.postMessage
X-Auth-Token: {{ .Secrets.token }}
X-User-Id: {{ .Secrets.user_id }}
Content-Type: application/json
```

```
{"roomId": {{ .Response.room | toJson }},
 "tmid": {{ .Response.id | toJson }},
 "text": {{ printf "%s on #%d" .Event .AlertGroup.Number | toJson }}}
```

What to know:

- **Edits may be limited by the chat.** Many chats let a user edit a message only for a while, or not at all. If yours does,
  the `update` of an old message is refused, and by the chat's answer that ends as Not delivered or makes the Destination
  Broken. Check the chat's setting for editing messages. If you
  cannot allow edits, make `update` a second `chat.postMessage` call: each change is then a new message instead of an
  edit.
- **Loud and Quiet.** A chat decides for itself what rings. Muster tells the template through `.Notify`, and through
  `.Mentions` whom to mention; wrap them in the chat's syntax with the `mention` function, as [Mentions](templates.md#mentions)
  shows.
- **Two-step threads.** A chat that needs a thread created first uses `open_thread`; see [Recipe: a chat with two-step
  threads](templates.md#recipe-a-chat-with-two-step-threads).

## An issue tracker

**Mode: template.** Opening an issue when a problem starts, commenting on it as it develops, and closing it when the
problem resolves needs one thing: the id of the issue that the first request created, in every later request. That is what
the extraction rules are for. The `create` request opens the issue and extracts its number, `update` changes its state,
and `reply_in_thread` adds comments.

This sketch uses the issue API of the open forges Gitea and Forgejo, which share it. A `POST` to
`/repos/{owner}/{repo}/issues` answers with the issue, including its `number`; a `PATCH` to
`/repos/{owner}/{repo}/issues/{number}` changes its `state` to `open` or `closed`; a `POST` to
`/repos/{owner}/{repo}/issues/{number}/comments` adds a comment. Create a Secret `token` with an access token of a bot user
that may write issues in the repository.

`create`:

```
POST https://git.example.org/api/v1/repos/ops/alerts/issues
Authorization: token {{ .Secrets.token }}
Content-Type: application/json
```

```
{"title": {{ printf "#%d %s" .AlertGroup.Number .AlertGroup.Title | toJson }},
 "body": {{ printf "%s\n\n%s" .AlertGroup.Summary .AlertGroup.URL | toJson }}}
```

Extraction rule: the name `number`, the path `$.number`.

`update`:

```
PATCH https://git.example.org/api/v1/repos/ops/alerts/issues/{{ .Response.number }}
Authorization: token {{ .Secrets.token }}
Content-Type: application/json
```

```
{"title": {{ printf "#%d %s (%s)" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status | toJson }},
 "state": "{{ if eq .AlertGroup.Status "resolved" }}closed{{ else }}open{{ end }}"}
```

`reply_in_thread`:

```
POST https://git.example.org/api/v1/repos/ops/alerts/issues/{{ .Response.number }}/comments
Authorization: token {{ .Secrets.token }}
Content-Type: application/json
```

```
{"body": {{ printf "%s: %s" .Event .AlertGroup.URL | toJson }}}
```

The issue opens with the Alert Group, gets a comment for each Thread reply, such as new Alerts, a Takeover or a Reminder,
closes when the Alert Group is resolved, and opens again if the Alert Group returns. The title shows the current status.

What to know:

- **One issue per Alert Group.** A Route that takes many Alert Groups opens many issues. Narrow its Matchers, or choose a
  Group key that groups more.
- **If the extraction finds nothing,** the `create` still counts as delivered, the Timeline records the missing value, and
  every later request that reads it fails as a template error. Check the answer in the **Send test message** result.
- **A tracker whose answer carries no id** cannot be driven this way. Use the events mode with a relay that remembers the
  id of the issue for each `alert_group.id`, and does what the recipe above does with the events `created`, `resolved` and
  `reopened`.
