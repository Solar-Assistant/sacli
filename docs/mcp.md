# Connecting an AI assistant over MCP

`sacli mcp` runs sacli as an [MCP](https://modelcontextprotocol.io) server, which gives an AI
assistant — Claude Code, Cursor, anything that speaks MCP — the SolarAssistant tools: search your
sites, read live and historical metrics, read logs, check and change settings.

## Is this the one you need?

There are three ways to give an assistant the same tools, and this is not the common one.

| | Web connector | Claude Desktop extension | `sacli mcp` |
|---|---|---|---|
| Works in | Claude web, desktop, mobile | Claude Desktop | any MCP client |
| Install | nothing | one `.mcpb` file | the sacli binary |
| Sign-in | SolarAssistant account | API key | API key (`sacli configure`) |
| Site calls go | through the cloud | direct to the site | direct to the site |

Most people should add **`https://solar-assistant.io/mcp`** to their assistant as a connector: nothing
to install, and it works everywhere. Every tool call then goes to the solar-assistant.io cloud, which
relays it to your site.

`sacli mcp` is for when that round trip matters, or your client only takes a local server. It runs on
your machine, so a call about a site goes straight to that site — the unit on your local network
when it is reachable, otherwise the site's regional proxy, otherwise the cloud. An assistant working
through a problem makes dozens of calls, and skipping the relay is the difference you notice.

For Claude Desktop the same direct routing is packaged as an extension you install with a click:
[claude_solar_assistant](https://github.com/Solar-Assistant/claude_solar_assistant).

## Getting started

Configure your API key once, then check the server can list its tools:

```bash
sacli configure
sacli mcp --list
```

Then register it with your client. Claude Code:

```bash
claude mcp add solar-assistant -- sacli mcp
```

Cursor (`~/.cursor/mcp.json`) and other clients that take a JSON server entry:

```json
{
  "mcpServers": {
    "solar-assistant": {
      "command": "sacli",
      "args": ["mcp"]
    }
  }
}
```

Some things worth asking once it is connected:

> My inverter just switched to grid, check my settings and figure out why that happened.

> Look at how my system has been running and tell me if there are inverter settings you would change
> to use less grid energy.

> Help me customize a custom solar monitoring portal for my business.

The last one pairs with [`sacli portal`](portal.md), which serves the portal on your machine while
the assistant edits it.

## What it does

On start it fetches the tool list from solar-assistant.io, using your API key, and serves that list
for as long as it runs — restart the server to pick up tools added since. The last list is kept
beside your config, so it still starts when the cloud is slow or unreachable.

A tool whose name starts with `site_` and that is called with a `site_id` is sent to that site
directly, with the same local-then-proxy-then-cloud order as every other sacli command and the same
cached site authorization. Everything else — sites search, orders, account — goes to the cloud. A
site needs SolarAssistant build 2026-03-24 or later to answer directly.

The tools act as you. One that changes a setting or starts monitoring changes it on the real site;
most clients ask before running a tool like that, and it is worth leaving that on.

## Choosing which tools are exposed

The cloud keeps its rarer tools out of the list so an assistant's context is not spent on them; the
assistant finds those through the `tool_search` tool and runs them with `tool_call`. `--tools` lets
you decide instead, by name or glob:

```bash
sacli mcp --list --tools 'whoami,site_metric_*'
```

Only matching tools are listed and callable, and a pattern that names a tool the cloud would have
held back brings it into the list. Give the same flag in the client's `args` to apply it there:
`["mcp", "--tools", "site_*"]`.

## Serving over HTTP

```bash
sacli mcp --http          # port 3005
sacli mcp --http 8080
```

and point the client at `http://localhost:3005`. `--stdio` alongside `--http` runs both at once.

The HTTP server does **not** authenticate its callers and listens on every network interface:
anyone who can reach the port uses the tools as you. Keep it behind a firewall or on a machine only
you can reach, and prefer stdio when the client supports it.
