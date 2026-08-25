# Agent Recipes

## Remote agent sends one artifact

```bash
CODE=$(curl -fsS \
  --netrc \
  -F "file=@/work/result.json" \
  "$SSS_URL/s")
printf '%s\n' "$CODE"
```

Pass only the code to the receiving agent.

## Remote agent sends files plus instructions

```bash
CODE=$(curl -fsS \
  --netrc \
  -F "file=@report.pdf" \
  -F "file=@evidence.zip" \
  -F "note=Audit the evidence and return a verdict." \
  -F "ttl=180" \
  "$SSS_URL/s")
```

## Receiving agent inspects before download

```bash
curl -fsS --netrc \
  -H "Accept: application/json" \
  "$SSS_URL/v1/transfers/$CODE"
```

## Receiving agent downloads into a directory

```bash
mkdir -p incoming
curl -fsS --netrc \
  "$SSS_URL/r/$CODE?format=tar" |
  tar -xf - -C incoming
```

## CLI automation

```bash
CODE=$(sssend ./artifact --note-file TASK.md --ttl 2h)
RESULT_PATH=$(ssrecv "$CODE")
```

No parsing is required.

## VPS-local receiving agent

```bash
PAYLOAD_PATH=$(curl -fsS \
  --unix-socket /run/sss/sssd.sock \
  "http://localhost/local/r/$CODE")
```

Treat `PAYLOAD_PATH` as read-only. Copy it into the workspace before modifying:

```bash
cp -a "$PAYLOAD_PATH" ./input
```

The CLI form:

```bash
ssrecv "$CODE" --to ./input
```

## Handoff note pattern

A useful short note answers:

- what is included;
- what the receiving agent should do;
- expected output;
- important constraints.

Do not force a note when the filenames and surrounding conversation are sufficient.
