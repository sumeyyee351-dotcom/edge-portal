
# Minimal Edge Device Portal

A small, self-contained operator portal meant to run on an embedded Linux
device (e.g. a Raspberry Pi at the edge of a factory network). It listens
for JSON telemetry over MQTT and shows the latest values on a plain,
auto-refreshing HTML page — no JavaScript, no database, no cloud
dependency.

```
┌────────────────────┐        MQTT (1883)        ┌────────────────────────┐
│  Any MQTT publisher │ ───────────────────────▶  │   Mosquitto broker      │
│  (sensor, script,   │   devices/edge01/telemetry │  (eclipse-mosquitto:2)  │
│   mosquitto_pub...)  │                           └──────────┬──────────────┘
└────────────────────┘                                        │ subscribe
                                                                ▼
                                                     ┌──────────────────────┐
                                                     │   Go portal service   │
                                                     │  - MQTT subscriber    │
                                                     │  - in-memory state    │
                                                     │  - net/http server    │
                                                     └──────────┬────────────┘
                                                                │ :8080
                                                                ▼
                                                     ┌──────────────────────┐
                                                     │  Browser on the LAN   │
                                                     │  plain HTML + CSS,    │
                                                     │  refreshes every 5s   │
                                                     └──────────────────────┘
```

Both services run as containers, orchestrated with `docker-compose.yml`,
so the whole stack starts with one command.

## Project structure

```
edge-portal/
├── app/
│   ├── main.go              # HTTP server + MQTT subscriber
│   ├── go.mod
│   ├── Dockerfile           # multi-stage build (golang:alpine → alpine)
│   └── templates/
│       └── index.html       # single page, CSS embedded, meta-refresh 5s
├── mosquitto/
│   └── config/
│       └── mosquitto.conf   # broker config (listener, persistence, logs)
├── scripts/
│   └── publish-test.sh      # convenience wrapper around mosquitto_pub
├── docker-compose.yml       # wires mosquitto + edge-portal together
├── setup.sh                 # checks Docker, builds, starts the stack
└── README.md
```

## Prerequisites

- **Docker Desktop** (includes Docker Engine + Compose v2). That's the
  only thing you need installed — Go itself is only used *inside* the
  build container, so you don't need a local Go toolchain to run this.
- A terminal. On macOS, the built-in Terminal or iTerm2 both work fine.

## Quickstart

```bash
git clone <this-repo-url>
cd edge-portal
./setup.sh
```

`setup.sh` will:
1. Verify the `docker` CLI is installed.
2. Verify the Docker daemon is actually running (not just installed).
3. Detect whether to use `docker compose` (v2) or `docker-compose` (v1).
4. Build the Go image and start both containers in the background.
5. Print the URL and a ready-to-run `mosquitto_pub` test command.

Once it's up, open **http://localhost:8080** in a browser. You'll see:

- The device name and an MQTT connection status pill (green/red).
- "Waiting for the first MQTT message…" until something is published.
- A raw-payload panel once a message arrives.

To stop everything:

```bash
./setup.sh down
```

## Testing: publishing a message

Mosquitto's own CLI tools are installed inside the broker container, so
the simplest test is to exec into it:

```bash
docker exec -it edge-mosquitto mosquitto_pub \
  -t "devices/edge01/telemetry" \
  -m '{"temperature": 23.5, "humidity": 41, "status": "ok"}'
```

or use the bundled helper, which also generates a small random reading
if you don't pass one:

```bash
./scripts/publish-test.sh
./scripts/publish-test.sh '{"temperature": 30, "pressure_kpa": 101.3}'
```

Within 5 seconds (the page's auto-refresh interval) the browser tab
should show:
- Status pill switches to **CONNECTED** as soon as the portal reaches the
  broker (this happens automatically at startup, independent of any
  message being published).
- A value card per JSON key (`temperature`, `humidity`, `status`, …),
  sorted alphabetically.
- "Messages Received" counter incrementing.
- The raw JSON payload at the bottom, for debugging.

You can publish any flat JSON object — the portal doesn't hard-code
field names, it renders whatever keys arrive.

## Configuration

The portal is configured entirely through environment variables (set in
`docker-compose.yml`):

| Variable         | Default                        | Meaning                              |
|------------------|---------------------------------|---------------------------------------|
| `MQTT_BROKER`    | `tcp://mosquitto:1883`         | Broker URL the portal connects to     |
| `MQTT_TOPIC`     | `devices/edge01/telemetry`     | Topic it subscribes to                |
| `MQTT_CLIENT_ID` | `edge-portal`                  | MQTT client ID                        |
| `DEVICE_NAME`    | `Edge Device 01`               | Name shown in the page header         |
| `HTTP_PORT`      | `8080`                         | Port the HTTP server listens on       |

## Design decisions

- **No JavaScript, `<meta http-equiv="refresh">` instead.** The spec
  calls for plain HTML/CSS. A meta-refresh tag is the simplest way to get
  "auto-updating" behaviour without any client-side scripting, which also
  means the page still works with JS disabled or on very old browsers —
  a reasonable assumption for a panel mounted next to industrial
  equipment.

- **In-memory state, one struct behind a `sync.RWMutex`.** The task asks
  for "latest received values", not history, so a database would be
  over-engineering for a device that might not even have persistent
  storage. The state is intentionally lost on restart — MQTT will simply
  deliver the next message.

- **Generic JSON handling (`map[string]interface{}`), not a fixed
  struct.** The portal doesn't assume the payload shape, so it works for
  temperature/humidity sensors, GPIO status messages, or anything else
  that publishes flat JSON — the page just renders whatever keys it
  receives, sorted for a stable layout.

- **Reconnect-by-default MQTT client.** `SetAutoReconnect` plus a manual
  retry loop on the *initial* connect mean the portal doesn't crash or
  need a restart if it comes up before the broker container is ready, or
  if the network blips — both realistic scenarios on embedded hardware.

- **`go:embed` for the HTML template.** The template is compiled into
  the binary, so the final container image is just one static executable
  plus `ca-certificates` — nothing to accidentally leave behind when
  copying the image to a device.

- **Multi-stage Dockerfile, non-root runtime user.** The build stage
  (full `golang:alpine` toolchain) never ships; the runtime image is
  plain `alpine` running the static binary as an unprivileged user,
  keeping the final image small and reducing attack surface on a device
  that may sit on a factory network.

- **Mosquitto with `allow_anonymous true`.** Kept simple on purpose for
  a local/demo network. See "Security notes" below for what to change
  before using this on anything but a trusted LAN.

## Running on real embedded hardware (e.g. Raspberry Pi)

The images built by plain `docker compose build` target your host's
architecture. To build an image for a Raspberry Pi (ARM) from an Intel/
Apple Silicon machine, use `buildx`:

```bash
docker buildx build --platform linux/arm64 -t edge-portal:arm64 ./app
```

Then copy `docker-compose.yml`, `mosquitto/`, and the built image over to
the device (or point the device's Docker at your registry) and run
`docker compose up -d` there. Apple Silicon Macs already build `arm64`
images natively, which happens to match a Raspberry Pi 4/5's
architecture directly.

## Security notes (for anything beyond local testing)

This setup optimizes for "runs in five minutes on a laptop or a Pi",
not for production hardening. Before exposing it beyond a trusted LAN:

- Set `allow_anonymous false` in `mosquitto/config/mosquitto.conf` and
  configure a password file (`mosquitto_passwd`) or certificate-based
  auth.
- Put the Go portal behind TLS (e.g. a reverse proxy like Caddy/nginx)
  if it will be reached over an untrusted network.
- Restrict the exposed ports in `docker-compose.yml` to the specific
  network interface the device should be reachable on, rather than all
  interfaces.

## Tools used

Go (HTTP server + MQTT client via `eclipse/paho.mqtt.golang`), Eclipse
Mosquitto, Docker & Docker Compose, plain HTML/CSS, Bash.
