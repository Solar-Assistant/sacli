# Building a custom customer portal

A solar business can replace the SolarAssistant customer portal with one of its own — its name, its
colours, its logo, its pages. The portal is a repository of static HTML that SolarAssistant serves
for you; `sacli portal` serves that same repository from your own machine while you work on it.

Without it the loop is commit, push, wait, then look at `staging-<your-org>.solar-power.live`. With
it you edit the template and see the result straight away.

## Getting started

`sacli portal` opens a web server on `127.0.0.1` and serves your checkout the way SolarAssistant
serves it in production. Starting from the minimal template:

```bash
git clone https://github.com/Solar-Assistant/portal-minimal.git
cd portal-minimal
sacli portal --org 42      # your organization id, needed once
```

Open the address it prints, edit a page, and the browser reloads. On later runs the organization is
remembered, so it's just:

```bash
sacli portal
```

## What it does

It reads `solar-assistant.json`, serves only the directory its `root` names, resolves clean URLs
(`/sign_in` → `sign_in.html`), substitutes your organization's name and brand colours into every
`.html` file, serves `/assets/logo.svg` from your organization's stored logo, and reloads the browser
whenever a file changes.

The organization must have **brand colours set** before it will serve. Without them every page
renders in the neutral palette, which looks exactly like a mistyped organization id — so it refuses
and tells you, rather than letting you style an afternoon's work against the wrong company.

The pages call the live API from the browser, exactly as they do in production, so signing in works.
Reads are your own organization's data; a page that invites or removes a user acts on real people.

`--port` moves it off the default `8912`, and an optional directory argument points at a repository
other than the current one.

## Running components from a checkout

A portal page is built from the SolarAssistant web components
([js_solar_assistant](https://github.com/Solar-Assistant/js_solar_assistant)), which the page loads
from a published bundle — so a change to a component cannot be seen until it ships. If you are
working on the components themselves, `--components` points the pages at your own copy instead:

```bash
git clone https://github.com/Solar-Assistant/js_solar_assistant.git
cd portal-minimal
sacli portal --components ../js_solar_assistant
```

Edit a component and the browser reloads, the same as editing a page. It also takes a built bundle
or a URL, for a bundle somebody produced elsewhere:

```bash
sacli portal --components ./dist/solar-assistant.js
sacli portal --components https://staging.example/sa.js
```

A checkout needs **no build step**: the workspace's own `package.json` files are read for each
package's name and entry point, and an import map is generated from them, so the browser runs the
source as it stands. The package named `@solar-assistant/components` is the one served as the page's
script. Edits to the checkout reload the browser just like edits to the portal.

The components source is printed at startup on every run, including the default — a flag that
silently changes which code is running is worth being reminded of.
