# newsletter-detect

Heuristic classifier that scores each email's "newsletter-ness" from headers + body. Returns `matched: true` when the score crosses a configurable threshold; optionally attaches a `move` (or any other) action to the verdict.

## Install

```bash
go install github.com/golang-mail/newsletter-detect@latest
```

## How it scores

| Signal | Points | Notes |
|---|---|---|
| `List-Unsubscribe` header | +3 | RFC 2369 — strongest signal |
| `List-Id` | +2 | Mailing-list identifier |
| `Precedence: bulk`/`list` | +2 | |
| `X-Mailer` matches a known bulk sender (Mailchimp/SendGrid/etc.) | +2 | |
| `List-Post`, `Mailing-List`, `Auto-Submitted`, `X-Campaign` | +1 each | |
| Body has "view in browser" / unsubscribe link / tracking pixel | +1 each | Only checked if header score ≥ `min_score_for_body` |

Default threshold is **3**, which catches most real newsletters while ignoring transactional mail that happens to mention the word "unsubscribe."

## Register

```yaml
plugins:
  - name: newsletter-detect
    command: ["/path/to/newsletter-detect"]
    transport: socket   # default; the plugin listens on $GO_MAIL_PLUGIN_SOCKET
    timeout: 5s
    config:
      threshold: 3
      min_score_for_body: 1
      verbose: false
      # If set, these actions are attached to the verdict whenever a match fires.
      # Filter authors can also leave this empty and put the action in the filter's
      # own `actions:` block instead.
      actions_on_match:
        - type: move
          value: "Newsletters"
        - type: mark_as_read
```

## Use it from filters

```yaml
filters:
  - name: route-newsletters
    mailboxes: ["INBOX"]
    conditions:
      - field: plugin:newsletter-detect
        operator: matches
        value: ""           # filter_config is unused; any value is fine
    actions: []             # actions come from the plugin's actions_on_match
```

If you'd rather drive actions from the filter file, leave `actions_on_match` empty and put the move in the filter:

```yaml
filters:
  - name: route-newsletters
    mailboxes: ["INBOX"]
    conditions:
      - field: plugin:newsletter-detect
        operator: matches
        value: ""
    actions:
      - type: move
        value: "Newsletters"
      - type: mark_as_read
```

## Tuning

Set `verbose: true` and watch the plugin's logs to see which signals are firing on which emails. If too much transactional mail is matching, raise the threshold or remove the body-signal contribution by setting `min_score_for_body` to a number above what your headers alone produce.

The host caches verdicts per `(uid, plugin_version, filter_config)`, so re-runs are free until you change the threshold (which is a config change, requiring a restart and cache invalidation).
