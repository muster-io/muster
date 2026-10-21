# MusterTemplateError

## Meaning

A template failed to render. This alert has two sources with the same name, severity `warning` and this page:

- The **Internal alert**, raised by Muster as an Alert of the built-in "Muster" Integration when a template of a Route
  or of a Destination starts failing, and resolved when that template renders again or, for a Route, when a new
  template of that kind is saved; for a Destination, also when the Destination is deleted. Its labels are `route`
  and `route_name` for a template of a Route — the Root message, the Alert line or the Ack timeout notice (`template`
  is `root_message`, `line` or `ack_timeout_notice`) — or `destination` and `destination_name` for a request template
  of an outgoing webhook (`template` is `webhook_request`).
- The **chart rule**, on every failure counted in the last 15 minutes:

    ```promql
    sum by (route, destination, template) (increase(muster_template_errors_total{job="muster"}[15m])) > 0
    ```

    It fires at once (`for: 0m`) with the labels `route`, `destination` (one of them empty) and `template`. The
    counter also counts the URL templates of Link rules (`template="link_rule"`), which raise no Internal alert, so
    the chart rule can fire where the Internal alert does not.

## Impact

- A Route template that fails: the message is rendered with the **Fallback template** instead — every label under
  Muster's heading, with the footer and the buttons. People still see and act on the Alert Group, in a plainer
  format.
- A request template of an outgoing webhook that fails: that request is not sent; the delivery or the Thread reply
  ends as Not delivered, with no fallback body.
- A Link rule that fails: its link is left out of the Alert Group's links.
- Everything else keeps working.

## Diagnosis

1. Open **System status** (`/admin/system-status`): **Template errors** lists every Route and Destination whose
   template fails, with the kind — Root message, Alert line, Ack timeout notice, Link rule or Request template.
2. Open the Route page (`/routes/<id>`): the banner says "Template error since HH:MM: {error}." with the line and
   column that failed. The Route list marks the Route with "Template error".
3. Read the log lines:
    - `fallback_template_used` (`route`, `group`, `template`, `error`): a Route template failed and the message used
      the Fallback template.
    - `webhook_template_failed` (`destination`, `group`, `request`, `error`): a request template of an outgoing
      webhook failed.
    - `link_rule_failed` (`link_rule`, `route`, `group`, `error`): the URL template of a Link rule failed.
4. Check which labels the template reads: a template that works for most Alert Groups often fails on one whose
   Alerts lack a label or carry an unexpected value.

## Fix

- Correct the template so that it handles the missing or unexpected value, and check it with the preview before you
  save it. For outgoing webhooks, see [Template errors](../../outgoing-webhooks/templates.md#template-errors).
- For a Link rule, correct its URL template under Organization → **Link rules** (`/admin/organization/link-rules`).
- The Internal alert resolves when the next message of the Route renders with the template (logged as
  `template_recovered`) or when a new template of that kind is saved on the Route, and when the next request of the
  Destination renders or the Destination is deleted. Deleting a Route does not resolve it. The chart rule resolves
  15 minutes after the last failure.
