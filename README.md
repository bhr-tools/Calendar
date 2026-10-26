# Calendar

A minimalist, self-hosted calendar built for **time blocking**, in the spirit of Get Done and Focus:

- **Time blocking:** draw a block on the grid to create it, drag it to move it, drag its bottom edge to resize it. **Hold Ctrl while dragging to copy it.** Everything snaps to 15 minutes.
- **Live sync with other apps (CalDAV):** your phone's calendar app (Apple Calendar, DAVx⁵ on Android, Thunderbird…) reads and edits the same calendars, live. No Google account involved: the database lives on your server.
- **Subscriptions:** add any iCalendar feed (a university timetable, holidays…). It is downloaded every 30 minutes and shown read-only, in the web app and in the connected apps.
- **Export / import:** any calendar exports to a standard `.ics` file, and `.ics` files can be imported. A calendar can also be published read-only at a secret address, for Google Calendar or Outlook.
- **Full Vim navigation:** fully operable from the keyboard.
- **Quick add:** one line, in English or French: `Sport demain 18h-19h30 @Gym`, `Standup every weekday 9:30`, `Holidays 24 dec`.
- **Rosé Pine palette:** Moon (dark) and Dawn (light) themes, following the system setting.
- **Zero bloat:** no AI, no invitations, no scheduling links, no notifications.
- **One binary:** a single Go binary with embedded SQLite and web app, packaged with Nix Flakes (multi-instance NixOS module).

## Keyboard shortcuts

Press `?` in the app to show the cheatsheet.

| Key | Action |
|---|---|
| `h` / `l` (or `←` / `→`) | Previous / next day, week or month |
| `t` | Today |
| `d` / `w` / `m` | Day / week / month view |
| `g` | Go to a date (`15 nov`, `friday`, `2027-01-04`) |
| `j` / `k` (or `↓` / `↑`) | Next / previous event |
| `Enter` or `e` | Open the selected event |
| `a` | New event on the focused day |
| `q` | Quick add in one line |
| `x` (or `Delete`) | Delete (with Undo) |
| `y` / `p` | Copy / paste on the focused day, at the same time |
| `Shift+J` / `Shift+K` | Move 15 minutes later / earlier |
| `Shift+L` / `Shift+H` | Move one day later / earlier |
| `+` / `-` | Lengthen / shorten by 15 minutes |
| `/` | Search titles, places and notes |
| `Esc` | Close, leave a field, clear the selection |

With the mouse: drag on empty space to create, drag an event to move it, drag its bottom edge to resize it, Ctrl+drag to copy it. On a phone, press and hold before dragging.

A recurring event asks whether an edit applies to **this event** or **all events**. Keyboard moves (`Shift+J/K/H/L`) only move the selected occurrence.

## Connecting other apps (CalDAV)

Open **Connect a calendar app** in the sidebar for the exact addresses. In short:

- **iPhone / iPad:** Settings › Apps › Calendar › Calendar Accounts › Add Account › Other › Add CalDAV Account. Server: `calendar.example.com`, user name: anything, password: your Calendar password.
- **macOS:** Calendar › Settings › Accounts › + › Other CalDAV Account (manual).
- **Android:** DAVx⁵ › Login with URL and user name: `https://calendar.example.com/dav/`.
- **Thunderbird:** New calendar › On the network: `https://calendar.example.com/dav/`.
- **Google Calendar, Outlook:** they cannot log in to a CalDAV server. Give them a read-only **secret address** instead (calendar menu › Secret address), then in Google Calendar: Other calendars › + › From URL. They refresh it on their own schedule (Google: every few hours). Anyone who knows the address can read the calendar; making a new address revokes the old one.

The server implements discovery (`/.well-known/caldav`), `calendar-multiget`, `calendar-query`, `sync-collection` and the `getctag` extension, so clients only download what changed. Events are stored exactly as clients send them: alarms and client-specific fields survive edits made in the web app.

Serve it over HTTPS (behind Caddy, for instance): CalDAV clients send the password with every request.

## Installation (NixOS)

```nix
# flake.nix
inputs.calendar.url = "github:bhr-tools/Calendar";

# configuration.nix
imports = [ inputs.calendar.nixosModules.default ];

services.calendar = {
  enable = true;
  instances.ben = {
    port = 9880;              # behind a reverse proxy
    timezone = "Europe/Paris"; # for events written without a time zone
  };
};

services.caddy.virtualHosts."calendar.example.com".extraConfig = "reverse_proxy localhost:9880";
```

Each instance has its own database (`/var/lib/calendar-<name>/calendar.db`) and password, chosen on first visit.

### Options

| Flag | Environment | Default | |
|---|---|---|---|
| `-host` | `HOST` | `127.0.0.1` | Listen address |
| `-port` | `PORT` | `8080` | Listen port |
| `-data-dir` | `DATA_DIR` | `./data` | Directory of the database |
| `-db` | `DB_PATH` | | Database file, overrides `-data-dir` |
| `-timezone` | `TIMEZONE` | `Europe/Paris` | Time zone of events without one |
| `-refresh` | `REFRESH` | `30m` | Interval between subscription downloads |
| `-no-auth` | `NO_AUTH` | `false` | Disable the password (local use only) |

## Development

```sh
nix develop
go run ./cmd/server -data-dir ./data      # http://localhost:8080
go test ./...                             # store, iCalendar, CalDAV, API
node --test tests/js/*.test.mjs           # quick add parser
sh tests/e2e/run.sh                       # browser tests (Playwright, from nixpkgs)
```

Layout: `internal/cal` reads, expands and edits iCalendar objects; `internal/caldav` is the CalDAV server; `internal/api` the JSON API of the web app; `internal/feeds` downloads subscriptions; `web/` is the web app (plain ES modules, no build step).
