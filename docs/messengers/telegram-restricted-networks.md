# Telegram in restricted networks

Muster talks to Telegram only by calling out to the Bot API, `https://api.telegram.org` by default. Where the network
of your installation cannot reach it — Telegram is blocked, or the cluster has no direct way out — there are three
ways to reach it. Use them in this order:

1. [A proxy on the Connection](#1-a-proxy-on-the-connection), when a proxy can reach Telegram.
2. [A reverse proxy under your own domain](#2-a-reverse-proxy-under-your-own-domain), when you run a host that can
   reach Telegram.
3. [A self-hosted Bot API server](#3-a-self-hosted-bot-api-server), only when neither of the first two works. It has
   sharp edges that the first two avoid.

All three keep the bot in Telegram's cloud or move it to one server you control; none needs a change to the
Destinations.

## 1. A proxy on the Connection

Each Connection may send its requests through its own proxy, while the rest of Muster — other Connections, the
identity provider, outgoing webhooks — keeps its own path. On the Connection page, under **Proxy**, turn on
**Use a proxy** and set:

- **Type**: HTTP, HTTPS or SOCKS5;
- **Address**: `host:port` of the proxy;
- **Username** and **Password**, if the proxy asks for them. The password is stored encrypted and never shown again.

Leave the **Bot API base URL** at `https://api.telegram.org`: the HTTPS connection to Telegram runs through the proxy
end to end, so the proxy never sees the bot token in the request path. Press **Check connection**: every step shows
its latency and whether it went "through the proxy" or "direct", and a proxy that refuses Muster's credentials is
named as such.

The proxy's own address is checked by the Organization's outbound address policy (**Outbound addresses**,
`/admin/organization/outbound`) like any other target: the standard policy allows private addresses and blocks
loopback; the strict policy blocks a proxy on a private address unless an allowed network covers it. `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are never
read; the proxy is set only on the Connection.

## 2. A reverse proxy under your own domain

When a host that you run can reach `api.telegram.org` — for example a small virtual machine in another network — put
a reverse proxy on it, under a name of your installation's own domain such as `tg.muster.example.org`, in front of
`api.telegram.org`. Muster then calls your domain, and only that host talks to Telegram.

The reverse proxy must:

- connect to `api.telegram.org` over HTTPS with the upstream **SNI** and the **`Host`** header set to
  `api.telegram.org`;
- allow the upstream to answer for longer than the long-polling timeout. In the long polling update mode, Muster asks
  Telegram to hold each `getUpdates` request open for up to 25 seconds and waits up to 35 seconds for the answer; give
  the proxy timeouts of 60 seconds;
- limit the request **body size**. Muster sends no files, only messages of at most 4,096 characters and their
  buttons, so 1 MB is plenty;
- turn the **access log** off, or write it without the request URI: every request path carries the bot token, as in
  `/bot123456789:AA…/sendMessage`;
- answer only Muster: put everything under a **secret path prefix**, a long random string that is never logged, and
  refuse every other path, or allow only Muster's egress addresses with an address **allowlist**, or both.

Then set the Connection's **Bot API base URL** to the reverse proxy, with the prefix:
`https://tg.muster.example.org/8f1c2e…`. Muster sends its requests to `<base>/bot<token>/<method>`. A new base URL
needs the bot token again, so that the stored token never reaches an address you did not mean; enter it, save, and
press **Check connection**. Pressed before you save, the check makes only the dry probe against the new address. Its
first step is a dry probe, `GET <base>/bot0:x/getMe` without your token, which
`api.telegram.org` answers with `401` and JSON: it tells a wrong host, a wrong path prefix or a server that is not a
Bot API apart before your token goes anywhere. Use an `https` base URL; with `http`, Muster warns that the token
travels in clear text.

### nginx

```nginx
server {
    listen 443 ssl;
    server_name tg.muster.example.org;
    ssl_certificate     /etc/nginx/tls/tg.muster.example.org.crt;
    ssl_certificate_key /etc/nginx/tls/tg.muster.example.org.key;

    # Every request path carries the bot token.
    access_log off;
    client_max_body_size 1m;

    # The secret path prefix; replace it with your own long random string.
    location /8f1c2e…/ {
        # An address allowlist instead of, or besides, the prefix:
        # allow 203.0.113.0/28;
        # deny all;

        proxy_pass https://api.telegram.org/;
        proxy_ssl_server_name on;
        proxy_ssl_name api.telegram.org;
        proxy_ssl_verify on;
        proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;
        proxy_set_header Host api.telegram.org;
        proxy_http_version 1.1;

        # Above the long-polling timeout of getUpdates.
        proxy_connect_timeout 10s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
    }

    location / {
        return 404;
    }
}
```

`proxy_pass` with a trailing `/` strips the prefix, so Telegram receives `/bot<token>/<method>`. nginx's error log
names the request of a failed upstream call too; keep it where only administrators can read it.

### Caddy

```caddy
tg.muster.example.org {
    # Caddy writes no access log unless the site has a log directive: leave it out.
    request_body {
        max_size 1MB
    }

    # The secret path prefix; replace it with your own long random string.
    handle_path /8f1c2e…/* {
        # An address allowlist instead of, or besides, the prefix:
        # @outside not remote_ip 203.0.113.0/28
        # respond @outside 404

        reverse_proxy https://api.telegram.org {
            header_up Host api.telegram.org
            transport http {
                tls_server_name api.telegram.org
                # Above the long-polling timeout of getUpdates.
                dial_timeout 10s
                response_header_timeout 60s
            }
        }
    }

    handle {
        respond 404
    }
}
```

`handle_path` strips the prefix before the request goes upstream. Caddy gets and renews the certificate of
`tg.muster.example.org` itself. Caddy's error log entries carry the request URI, with the token, when an upstream
call fails; keep that log where only administrators can read it.

## 3. A self-hosted Bot API server

Telegram publishes the source of its Bot API server
([tdlib/telegram-bot-api](https://github.com/tdlib/telegram-bot-api)), and a bot can be moved to a server you run.
That server connects to Telegram's data centres itself, over Telegram's own protocol, not through `api.telegram.org`;
it needs a direct route to them (its `--proxy` option applies only to the webhook requests it sends, not to its
connection to Telegram). Use it only when neither a proxy nor a reverse proxy works: it changes
where the bot lives, and every warning below applies.

- **It needs an `api_id` and an `api_hash`.** The server starts only with `--api-id` and `--api-hash`, which you
  register at [my.telegram.org](https://my.telegram.org) with a personal Telegram account. Use a **dedicated** account
  for it, not a person's own: the server's access depends on that account.
- **A bot lives on one server at a time.** While it is on your server, Telegram's cloud does not serve it, and the
  other way round. Every installation and every script that uses the token must use the same server.
- **`logOut` drops unread updates, and the way back to the cloud is closed for 10 minutes.** Moving a bot off the
  cloud starts with `logOut` on `api.telegram.org`. Updates that Muster has not fetched by then — button presses and
  messages of the discussion group — are lost, and after `logOut` the bot cannot log in to the cloud again for
  10 minutes.
- **Any request with the bot's token to `api.telegram.org` moves the bot back to the cloud, silently.** Even a `getMe`
  from a script or a health check on another host is enough. Muster keeps sending to your server, which no longer
  receives the bot's updates: button presses and messages of the discussion group go missing without an error. Never call
  `api.telegram.org` with this token from anywhere while the bot is on your server.
- **The server has no authentication of its own.** Anyone who reaches it can use it with any bot token. Never expose
  it to the internet or to networks other than Muster's; put it on a private address that only the replicas reach.
- **Tokens are visible on its statistics port.** `--http-stat-port` serves statistics that include the bots' tokens.
  Leave it off, or bind it to loopback with `--http-stat-ip-address 127.0.0.1` so that it stays local.
- **Do not use `--local`.** In local mode the server accepts local paths and `file://` URIs in requests and answers
  with absolute paths of its own disk, so anyone who reaches it can make it serve the server's files. Muster sends
  and receives no files and needs none of it.
- **There are no releases.** The project publishes no releases or tags, and container images built from it carry no
  version you can rely on. Build your own image, or pin a third-party image by **digest** (`image@sha256:…`), never by
  a tag such as `latest`.
- **Route `MusterDestinationBroken` of Telegram Destinations elsewhere.** When the server stops, every Telegram
  Destination of that bot becomes Broken at once. Route the Internal alert
  [`MusterDestinationBroken`](../operations/runbooks/MusterDestinationBroken.md) of Telegram Destinations to a
  Destination that is not in Telegram — Mattermost or an outgoing webhook — and its chart rule around Muster.

The server's address is an ordinary target of the outbound address policy: the standard policy allows a private
address and blocks loopback.

### Move a bot between servers

Muster has no wizard for this; the move is a **manual** procedure. Plan for a short gap in which presses and messages
of the discussion group are lost.

What Muster does with `deleteWebhook`: it calls `deleteWebhook` only when a Connection leaves the webhook update mode
— saved from Webhook to Long polling — and calls it at the base URL saved before, the server the bot is leaving. When
that call fails, the save fails ("deleteWebhook failed: …") and nothing changes. Changing only the base URL while the
Connection stays in the webhook mode calls `setWebhook` at the new base URL and never `deleteWebhook` at the old one.
Deleting a Connection in the webhook mode also calls `deleteWebhook`, best effort, at its saved base URL: the outcome
is logged (`telegram_webhook_deleted` or `telegram_webhook_delete_failed`) and never fails the deletion. Muster never
calls `logOut` or `close`.

From Telegram's cloud to your server:

1. Start the Bot API server as above and check from a replica that it answers:
   `curl -s http://bot-api.internal.example.org:8081/bot0:x/getMe` returns `401` and JSON. The server listens on
   plain HTTP (port 8081 by default); with an `http` base URL Muster warns that the token travels in clear text,
   which is acceptable only on a private network or through a tunnel.
2. If the Connection uses the webhook update mode, switch it to **Long polling** and save **while the old server
   still answers**, so that Muster's own `deleteWebhook` reaches it. If the save fails, fix the path to the old server
   first.
3. Stop every other process that uses the bot's token.
4. Optionally call `deleteWebhook` yourself on the old server, then `logOut`:

    ```sh
    curl -s "https://api.telegram.org/bot$TOKEN/deleteWebhook"
    curl -s "https://api.telegram.org/bot$TOKEN/logOut"
    ```

    The `deleteWebhook` before `logOut` is a best-effort step: if it fails, go on with `logOut`; it does not stop the
    move.
5. On the Connection, set the **Bot API base URL** to your server, enter the bot token again, and save.
6. Press **Check connection**: the dry probe, then `getMe` and `getWebhookInfo` with your token against the saved
   base URL. Run `muster doctor` too; it checks the saved Connections the same way.
7. Switch back to the webhook update mode if you used it: Muster calls `setWebhook` on your server.

From one server you run to another, use the same steps on the old server, as Telegram documents in
[Moving a bot from one local server to another](https://github.com/tdlib/telegram-bot-api#moving), in one of two ways:

- call `logOut` on the old server instead of on `api.telegram.org`; updates not yet fetched are lost;
- or call `deleteWebhook` and then `close` on the old server, and move the bot's subdirectory — named by the bot's
  user id, the number before the colon in its token — from the old server's working directory to the new server's;
  no updates are lost. `close` answers `429` during the first 10 minutes after the bot started on that server.

Back to Telegram's cloud: the same steps in the other direction — switch to long polling while your server still
answers, `logOut` on your server, then set the base URL back to `https://api.telegram.org` with the token. The cloud
takes the bot back only when 10 minutes have passed since its `logOut` from the cloud.

See also Telegram's [`logOut`](https://core.telegram.org/bots/api#logout),
[`close`](https://core.telegram.org/bots/api#close) and
[Using a Local Bot API Server](https://core.telegram.org/bots/api#using-a-local-bot-api-server).
