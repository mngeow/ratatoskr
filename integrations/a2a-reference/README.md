This adapter cannot tell the Ratatoskr gateway from anyone else on its network. The origin it receives
in a message is unverifiable, so it proves nothing about who sent it. Bind it to loopback, or to your
tailnet address, and set any other `--listen` address deliberately.

The gateway authenticates with a bearer secret that this adapter checks on every request, so a request
that gets through is from whoever holds that secret; the origin it carries is only as trustworthy as the
secret and the network.

# A2A reference adapter

A tiny, model-free A2A v1.0 server: send it text and it counts lines, words and characters; if you do
not say whether you want JSON or plain text, it asks once and waits (`TASK_STATE_INPUT_REQUIRED`) for a
follow-up on the same task. It is a reference endpoint for exercising A2A participants, not a bridge,
not a platform, and not part of Ratatoskr: it imports nothing from this repository.

## Authenticated (the normal path)

The gateway sends `Authorization: Bearer <secret>` on every call to a registered service. The adapter
holds the same secret and answers any other call with `401` and `WWW-Authenticate: Bearer`, before the
JSON-RPC handler. The token file must be a regular file (a symlink is refused).

1. Write any placeholder text into a token file, readable only by you:

       printf 'placeholder\n' > ~/a2a-token && chmod 0600 ~/a2a-token

   The adapter starts as soon as the file is non-empty; the real secret is pasted later. The card is
   public either way, so the gateway can read it before the secret exists.

2. Start the adapter with that file:

       go build ./... && ./a2a-reference --listen 127.0.0.1:7790 --gateway-token-file ~/a2a-token

3. Enrol the participant name with the gateway:

       rtk gateway participant enrol NAME

4. Register it, which prints the outbound secret once:

       rtk gateway participant register NAME --url http://127.0.0.1:7790/

5. Paste that secret into the token file:

       printf '%s\n' 'THE-SECRET-REGISTER-PRINTS' > ~/a2a-token

   No restart is needed: the file is re-read on every request, so the next call already uses it. Running
   `register` again rotates the secret, so paste the new one the same way; until you do, the gateway is
   refused.

Plain http is accepted only on loopback or the tailnet, or on a LAN address with `--insecure-http`;
https needs `--fingerprint`.

- `--listen ADDR`: default `127.0.0.1:7790`, loopback; any other address must be given deliberately.
- `--url URL`: the URL written into the card. Defaults to `http://<listen>/`, which is wrong behind a
  wildcard address such as `0.0.0.0:7790`: set `--url` to the address callers actually use.
- `--gateway-token-file PATH`: the file holding the gateway's bearer secret. It wins over
  `A2A_GATEWAY_TOKEN` when both are set, is trimmed of surrounding whitespace, may not exceed 4 KiB, and
  is re-read on every request.
- `A2A_GATEWAY_TOKEN`: the same secret in the environment, read once at start. Changing it needs a
  restart, which is why the file is the better source.
- `--no-auth`: demo only, see below.

The adapter refuses to start (exit status 2, nothing listening) when there is no usable secret: a
missing, unreadable, empty or over-4 KiB file, or an unset or empty variable. A missing or wrong bearer
on a request is `401` with `WWW-Authenticate: Bearer` and a body of `unauthorized`; a token file that
becomes unreadable at request time fails closed the same way. Neither the secret nor the header is ever
logged, and refusals are logged at most once per second.

The card declares the scheme the standard way, so a caller is told a secret is required:

    "securitySchemes": {"bearer": {"httpAuthSecurityScheme": {"scheme": "Bearer"}}},
    "securityRequirements": [{"schemes": {"bearer": {}}}]

## Demo mode: no authentication

`--no-auth` turns the check off, removes the scheme from the card, and prints a warning at start. It is
for demonstrations on a machine nobody else can reach, and the gateway will only register such an
adapter with its own `--no-auth`:

    ./a2a-reference --listen 127.0.0.1:7790 --no-auth
    rtk gateway participant register NAME --url http://127.0.0.1:7790/ --no-auth

Combining `--no-auth` with a token source is an error (exit status 2): it would be unclear which one was
meant.

Card: `GET /.well-known/agent-card.json`. Requests: `POST /` (JSON-RPC, A2A v1.0). Streaming, push,
extended cards and cancellation are refused with the spec's own errors (`CancelTask` says "task not
cancelable"); bodies over 64 KiB are refused; there is no model, no network access and no state outside
the in-memory task store.

Status: not run; conformance is unverified.
