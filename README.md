# Tracearr Connector for Silo

**Version 0.3.0**: everything is on the plugin's own settings page. The plugin makes the access secret and fills in the Tracearr server URL for you.

A [Silo](https://github.com/Silo-Server/silo-server) plugin that lets [Tracearr](https://github.com/connorgallopo/tracearr) monitor a Silo server.

Tracearr supports Plex, Jellyfin and Emby. Silo's Jellyfin-compatible port can't be used directly because it reports every account as a non-administrator, only lists the caller's own session, and only returns the caller's own user. This plugin serves a small, **read-only** Jellyfin-shaped API that fills those gaps from Silo's own admin API, so Tracearr sees all live streams (with client IPs), all users, and passes its admin check.

> **Status: 0.3.0, untested against a live server.** Built against Silo plugin SDK v0.31.0 and Silo's pre-release API. Unit tests cover routing, secret handling and field mapping against fake servers only. Please report what breaks.

## What Tracearr gets

| Tracearr needs | Source |
|---|---|
| Admin check (`/Users/Me`) | Always reports an administrator |
| Live streams, IPs, play method, bitrate | Silo `GET /api/v2/admin/sessions` |
| User list | Silo `GET /api/v2/admin/users` |
| Libraries, item info | Passed through (GET only) to Silo's Jellyfin listener |
| Artwork for streams | Served from Silo's poster for live sessions |

Not supported: terminating streams from Tracearr (read-only by design), push/websocket updates (Tracearr polls), per-user library access.

## Install

1. **Silo:** Admin → Settings → turn on **Allow Jellyfin apps to connect** (used for libraries/item info).
2. **Create an API key:** as an administrator, create an **unscoped** API key (starts `sa_`).
3. **Install the plugin:** Admin → Plugins → upload `tracearr-connector_0.3.0_linux_amd64` (or `_arm64` for ARM hosts).
4. **Fill in the plugin's settings page:** paste the API key and set **Address Tracearr uses to reach Silo** (for example `http://192.168.1.10:8090`), then **Save**. Leave the other addresses at their defaults for the standard Docker setup. If your Silo API address is not a loopback address, the address field can be left blank.
5. **Reload the page after a few seconds.** The plugin has made an access secret and filled in **Tracearr server URL**. Copy it.
6. **Tracearr:** Add server → **Jellyfin**, paste the URL. The API key field can hold any value.

To get a new secret, use **Clear saved secret** on the settings page; the plugin makes a new one and updates the URL a few seconds later. If you change the address, the URL updates the same way.

An optional **Tracearr setup** page in the plugin's menu shows the same URL hidden behind an eye icon, with a copy button.

## Security

- The plugin route is public to Silo and protected **only by the access secret in the URL**. Treat the full URL like a password.
- If Silo is behind a reverse proxy on the internet, **block `/api/v1/plugins/`** on the public hostname so the route is reachable only on your LAN. Only expose what you intend to.
- The admin API key grants full admin on Silo and is stored in the plugin's config. Plugins run fully trusted.
- The plugin never writes to Silo. Any non-GET request returns 405.

## Why a secret in the URL?

Silo only forwards a fixed set of headers to plugins (not `Authorization`) and only authenticates its own tokens, so a plugin can't validate Tracearr's key. A path secret is the available mechanism.

## Build

```
go mod tidy
make test
make build VERSION=0.3.0
```

Requires Go 1.26. Tagging `vX.Y.Z` runs the release workflow.

## Known risks

- Silo's settings form can't hide a text field, so the **Tracearr server URL** is visible in plain text on the plugin settings page to anyone who can open it (administrators). It contains the access secret.

- The plugin saves the secret and URL by calling Silo's plugin-config API with the admin key, then Silo restarts the plugin. If your Silo refuses that, the setup page shows the error.
- The setup page was exercised in a headless browser against a simulated host, not a real Silo.
- Silo's admin sessions field names (`file_duration` assumed to be seconds, `effective_play_method` values) were read from source, not observed live.
- Tracearr's `/Items` sync uses Silo's own Jellyfin ids, which differ from this plugin's session item ids. Library sync works; per-stream item linking by id may not.

License: AGPL-3.0-or-later.
