# Tracearr Connector for Silo

**Version 0.2.0**: adds a Tracearr setup page that generates the access secret and builds the URL for you.

A [Silo](https://github.com/Silo-Server/silo-server) plugin that lets [Tracearr](https://github.com/connorgallopo/tracearr) monitor a Silo server.

Tracearr supports Plex, Jellyfin and Emby. Silo's Jellyfin-compatible port can't be used directly because it reports every account as a non-administrator, only lists the caller's own session, and only returns the caller's own user. This plugin serves a small, **read-only** Jellyfin-shaped API that fills those gaps from Silo's own admin API, so Tracearr sees all live streams (with client IPs), all users, and passes its admin check.

> **Status: 0.2.0, tested by the author on a live Silo + Tracearr setup** (streams, users and polling work; real-time push is not possible). Built against Silo plugin SDK v0.31.0 and Silo's pre-release API. Unit tests cover routing, secret handling and field mapping against fake servers only. Please report what breaks.

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
3. **Install the plugin:** Admin → Plugins → upload `tracearr-connector_0.2.0_linux_amd64` (or `_arm64` for ARM hosts). In its settings paste the API key and save. Leave the two addresses at their defaults for the standard Docker setup; change them if Silo's ports differ inside its container.
4. **Open Tracearr setup** from the plugin's menu (or the plugin's admin links). Click **Generate access secret**. The page works out the installation id, saves the secret, and builds the full URL.
5. The URL stays hidden until you click the eye icon. The copy button copies it without showing it.
6. **Tracearr:** Add server → **Jellyfin**, paste the URL. The API key field can hold any value.

The setup page fills the address from your browser. If you opened Silo through a public address, change it to your local one (for example `http://192.168.1.10:8090`) before copying.

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
make build VERSION=0.2.0
```

Requires Go 1.26. Tagging `vX.Y.Z` runs the release workflow.

## Known risks

- The setup page saves the secret by calling Silo's plugin-config API with the admin key, then Silo restarts the plugin. If your Silo refuses that, the page shows the secret and the error so you can paste it into the plugin settings.
- The setup page was exercised in a headless browser against a simulated host, not a real Silo.
- Silo's admin sessions field names (`file_duration` assumed to be seconds, `effective_play_method` values) were read from source, not observed live.
- Tracearr's `/Items` sync uses Silo's own Jellyfin ids, which differ from this plugin's session item ids. Library sync works; per-stream item linking by id may not.

License: AGPL-3.0-or-later.
