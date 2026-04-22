# SolarAssistant CLI - sacli

Command-line tool for [SolarAssistant](https://solar-assistant.io). Can be used for local connections to SolarAssistant units and to query the [solar-assistant.io](https://solar-assistant.io) API. Single executable, no dependencies.

## Authentication

**Cloud API:** Generate an API key at [solar-assistant.io/user/edit#api](https://solar-assistant.io/user/edit#api), then run:

```bash
sacli configure
```

The cloud API key works for both cloud and local connections — no separate local credential needed per site.

**Local unit (direct connection):** To connect directly to a unit on your local network without a cloud API key, save the unit's web password:

```bash
sacli configure 192.168.0.100
```

## Usage

```
sacli <command> [arguments]

Commands:
  site        Connect to a site and run subcommands
  sites       List or search sites
  configure   Set credentials
  version     Print version
  help        Show this help
```

### Metric snapshot - REST

Returns the current value of all matching metrics. Attempts local network connection first, falls back to cloud.

```bash
sacli site 19489 metrics
sacli site 19489 metrics -t "battery*"
sacli site 19489 metrics -t "total/pv_power" --value
sacli site name:my-site metrics --json
sacli site 192.168.0.100 metrics -t "inverter_1/load_power"
```

For scripting:
```bash
SOC=$(sacli site 19489 metrics -t "total/battery_state_of_charge" --value)
```

For scripting on a SolarAssistant unit, the pre-authenticated `sitecli` is provided for convenience:
```bash
SOC=$(sitecli metrics -t "total/battery_state_of_charge" --value)
```

### Metric stream - WebSocket

Use `--watch` to stream metrics continuously as they update:

```bash
sacli site 19489 metrics --watch
sacli site 19489 metrics --watch -t "battery*"
sacli site 19489 metrics --watch -t "battery*" --max-freq 10
sacli site 19489 metrics --watch --json
```

### Metrics flags

| Flag | Description |
|------|-------------|
| `-t <pattern>` | Filter by topic glob, e.g. `battery*`, `total/pv_power`. Default returns a curated set of common metrics. Use `-t "*"` for all. |
| `-n <count>` | Stop after receiving N metrics |
| `--watch` | Stream continuously via WebSocket (default: REST snapshot) |
| `--value` | Output values only, no topic or unit — useful for scripting |
| `--json` | Machine-readable NDJSON output |
| `--max-freq <s>` | Minimum seconds between updates per topic (WebSocket only, server-side throttle) |

### List sites

```bash
sacli sites
sacli sites inverter:srne
sacli sites name:my-site --json
```

### Authorize a site

```bash
sacli site 19489 authorize
```

Returns the host, token, and a direct authenticated URL for the site — opening the URL logs you in without prompting for a username or password.

### Machine-readable output

All commands support `--json`:

```bash
sacli site 19489 metrics --json
sacli site 19489 metrics --watch --json
sacli sites --json
```

## Integration

This CLI is designed so that an LLM can use it to learn how to integrate SolarAssistant into your app. Running any command with `-v` outputs the exact HTTP and WebSocket calls being made:

```bash
sacli -v site my-site metrics -t "total/pv_power"
```
```
> GET https://solar-assistant.io/api/v1/sites?limit=1&q=name%3Amy-site
> Authorization: Bearer eyJ...
< 200 [{"id":123,"name":"my-site","proxy":"us-htz-1", ...}]
> GET http://192.168.0.100/api/v1/metrics?topic=total%2Fpv_power
> Authorization: Bearer dGt...
< 200 [{"group":"Status","name":"PV power","topic":"total/pv_power","unit":"W","value":2}]
total/pv_power 2 W
```

The verbose output shows the precise requests and responses, giving an LLM everything it needs to replicate the integration in any language.

To let an LLM explore the API, we suggest the following prompt:

> Hey Claude, use `sacli --help` and `sacli -v site my-site metrics -t "*" --watch -n 10` to explore the SolarAssistant API, then tell me how I can integrate it into my Python app. Use `-v` on any other commands you run to show the underlying HTTP calls.

For Go projects, [go_solar_assistant](https://github.com/Solar-Assistant/go_solar_assistant) can be pulled in directly as a library — it wraps the SolarAssistant API and WebSocket protocol and is what this CLI is built on.

## Installation

**Linux (amd64):**
```bash
sudo wget -O /usr/local/bin/sacli https://github.com/Solar-Assistant/sacli/releases/latest/download/sacli-linux-amd64
sudo chmod +x /usr/local/bin/sacli
```

**Linux (arm64 — Raspberry Pi 64-bit):**
```bash
sudo wget -O /usr/local/bin/sacli https://github.com/Solar-Assistant/sacli/releases/latest/download/sacli-linux-arm64
sudo chmod +x /usr/local/bin/sacli
```

**Linux (arm — Raspberry Pi 32-bit):**
```bash
sudo wget -O /usr/local/bin/sacli https://github.com/Solar-Assistant/sacli/releases/latest/download/sacli-linux-arm
sudo chmod +x /usr/local/bin/sacli
```

**macOS:**
```bash
sudo curl -Lo /usr/local/bin/sacli https://github.com/Solar-Assistant/sacli/releases/latest/download/sacli-mac
sudo chmod +x /usr/local/bin/sacli
```

**Windows:** Download `sacli.exe` from the [releases page](https://github.com/Solar-Assistant/sacli/releases/latest).

## License

[MIT](LICENSE)
