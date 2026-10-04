# 0012. Template sandbox

- Status: Accepted
- Date: 2026-10-02

## Context

Users customize what Muster sends: Root message templates, the line template for each Alert, the text of ack timeout
notices, the URL templates of Link rules, and the URLs, bodies and headers of outgoing webhook requests. Alertmanager
users already know Go templates and often have templates they want to reuse. Templates are written by users who may
edit Routes, Link rules or Destinations — the requests of an outgoing webhook belong to its Destination and are edited
by whoever may edit that Destination — but they render data that comes from alerts: labels and annotations controlled
by anyone who can write a Prometheus rule. A template must therefore be unable to read the environment or files, run
forever or produce unbounded output, and a broken template must never let an alert go unnoticed. For a message that
means delivering it anyway. An outgoing webhook request is read by another system that expects its own format, so a
body Muster made up instead could be rejected or, worse, misread.

Go's template engine cannot be interrupted by a timeout while a template is executing. The common function library for
Go templates, sprig, includes functions such as `env` that read the process environment and protects only through a
denylist (`HermeticTxtFuncMap`); its last release was in August 2024. Its maintained successor, sprout (MIT), lets the
caller register groups of functions explicitly.

## Decision

**Language.** Every template in Muster — messages, Link rule URLs and outgoing webhook requests alike — is a Go
template, the language Alertmanager templates use, rendered in the same sandbox: **sprout** with **only explicitly
registered function registries**: strings, lists, maps, conversions and regular expressions (RE2). Anything not
registered does not exist, so there is no access to the environment, files or network. Alertmanager's own template
functions are provided **under their Alertmanager names** (`toUpper`, `join`, `reReplaceAll`, `safeHtml`, …), so
existing Alertmanager templates carry over. Jinja and other template languages are not supported.

**Limits.** `now` comes from Muster's injected clock. Functions that produce loops (`until`, `seq`, `repeat`) are capped,
and rendered output is capped at about 50,000 characters. Together these bound execution, since a running template
cannot be timed out.

**Data and markup.** A template writes the Destination's markup; alert data reaches it already escaped for that markup,
the way `html/template` escapes data. `@` in alert data is neutralized, and intended Mentions come only from the trusted
`{{ mention "…" }}` function. Links are `http(s)` only, and label and annotation values are truncated to 4 KB by one
shared function (ADR-0005). Links to other tools come from Link rules — data with conditions and Lookup tables — rather
than code inside templates.

**Checks on save.** When a template is saved, it is parsed and dry-run against the most recent Stored Snapshots; a
template that fails is not saved, and the UI shows the rendered preview.

**Failure at runtime.** If a message template still fails while running, Muster renders the built-in Fallback template
instead — always valid, guaranteed by tests, showing all labels and the buttons — so the Alert Group is delivered
anyway. Muster also increments `muster_template_errors_total`, marks the Route or Destination that owns the template
with a template error and raises the Internal alert `MusterTemplateError`, which shares its name and runbook page with
the chart rule for the same condition (ADR-0014). A Link rule whose template fails is left out of the message. An
outgoing webhook request whose template fails is not sent: that delivery ends as Not delivered (ADR-0005), with no
fallback body, since the receiving side expects its own format, and the Destination shows a template error. Both are
counted and raise `MusterTemplateError` the same way. There is no automatic rollback to an earlier version of a
template.

**The Group key is not a template** (ADR-0003), so grouping never depends on template execution.

## Consequences

- Alertmanager users can reuse most of their templates; remaining differences show up at save time.
- New functions must be registered on purpose; a missing helper is a feature request, not something a template can
  work around.
- The caps stop pathological templates; the output cap sits well above what messengers accept in one message.
- A broken message template degrades the message, never the delivery. A broken outgoing webhook template costs that
  delivery, visibly — Not delivered and a template error on the Destination — while the Alert Group still reaches its
  other Destinations. Either way people learn about it through a metric, a mark in the UI and an Internal alert.
- Link rules and outgoing webhooks get the same protection as messages; there is no second, less guarded template
  engine.
- Dry runs depend on Stored Snapshots (kept 14 days, ADR-0006).

## Alternatives considered

- **sprig.** Protection by denylist, and no release since August 2024.
- **Jinja or another engine.** Not what Alertmanager users know, and portability of their templates would need a second
  engine.
- **A timeout around rendering.** Cannot stop a running Go template; caps on loops and output are the bound that works.
- **Saving broken templates and failing at runtime.** Loses messages exactly when someone edits a template during an
  outage.
- **A fallback body for outgoing webhooks.** The receiving system expects its own format; a body Muster made up would
  be rejected or, worse, misread, and the failure would look like a successful delivery.
- **Automatic rollback to the last good template.** Hides the problem and surprises the person who edited it; the
  fallback template plus an alert is explicit.
- **A Group key rendered from a template.** See ADR-0003.
