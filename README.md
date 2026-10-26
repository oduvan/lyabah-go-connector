# Goban connector

The Goban connector runs KataGo on your own computer and connects it to
[Goban](https://go.lyabah.com), a web app to play Go against KataGo and
review the games. When you play on the website, KataGo's moves and analysis
come from your computer.

The connector makes one outbound WebSocket connection to Goban. It opens no
port, so you do not need to change your router or firewall.

The program is called `goban-connector`.

**Phase A.** This first version does not download KataGo for you. You need
KataGo and a network file on your computer already. A later version may
download them.

## What you need

- KataGo, installed and working on your computer (run `katago version` to
  check). A GPU or a good CPU makes it faster.
- A KataGo network file, like `kata1-b18c384nbt-s....bin.gz`. A human
  network file is optional.
- A Goban account.

## Install

With Go (version 1.26 or newer):

    go install github.com/oduvan/lyabah-go-connector/cmd/goban-connector@latest

Ready-made releases for Linux, macOS and Windows will come later. For now,
install it with `go install` as above.

## Set up

1. On the Goban website, open the Connectors page and press **Connect a
   computer**. A pairing code appears. It works once, for one hour.

2. On your computer, pair with the code:

        goban-connector pair XXXX-XXXX

   This saves a secret token in the config file.

3. Tell the connector how to run KataGo:

        goban-connector init \
          --katago /usr/local/bin/katago \
          --model /home/me/katago/kata1-b18c384nbt.bin.gz

   This writes two base config files for KataGo next to the config file.
   See "The two modes" below for the other way to run KataGo.

4. Start it:

        goban-connector run

   Keep it running while you play. The computer shows as "Online" on the
   Connectors page. Then pick it in the "KataGo" row when you start a new
   game.

Stop it with Ctrl-C. KataGo stops with it.

## The commands

### `goban-connector pair CODE [--server URL]`

Trades the pairing code for a token and saves it. It writes `server`,
`token`, `connectorId` and `name` into the config file. Other fields in the
file stay as they are.

- `--server URL` is Goban's address. The default is `https://go.lyabah.com`.

The connector's name is the one you typed on the website. `pair` prints it.

### `goban-connector init ...`

Writes the part of the config file that says how to run KataGo. It keeps the
pairing. There are two forms.

Native mode:

    goban-connector init --katago PATH --model FILE [--human-model FILE] \
      [--gtp-config FILE] [--analysis-config FILE] [--data-dir DIR] [--threads N]

Command mode:

    goban-connector init --command PATH [--env KEY=VALUE]...

In native mode, if you do not give `--gtp-config` or `--analysis-config`,
`init` writes a default file (`gtp.cfg` or `analysis.cfg`) next to the config
file. It is a short config of about ten lines that `init` writes itself:

- `logToStderr = true`, with `logAllGTPCommunication` and `logSearchInfo`
  turned off;
- `ponderingEnabled = false` and `reportAnalysisWinratesAs = BLACK`;
- `rules = tromp-taylor` (in `gtp.cfg` only);
- the number of search threads: `numSearchThreads` in `gtp.cfg`, and
  `numAnalysisThreads = 2` with `numSearchThreadsPerAnalysisThread` in
  `analysis.cfg`. `--threads` (default 4) sets them;
- the sizes of the network cache (`nnCacheSizePowerOfTwo = 20` and
  `nnMutexPoolSizePowerOfTwo = 16`).

Every other setting has KataGo's default value. Goban adds the settings of
each game after these lines. If the file is already there, `init` keeps it,
so your changes are safe.

`init` saves file paths as full paths, so they still work when the program
is started from another folder, for example as a service. Use the full path
of `katago` if you run it as a service, because a service may have a short
PATH.

### `goban-connector run`

Reads the config file, connects to Goban and serves KataGo until you stop it
with Ctrl-C or SIGTERM. If the connection drops, it connects again by itself
(after 1, 2, 5 and 10 seconds, and then every 10 seconds). When the
connection drops, all KataGo processes are stopped. Your games stay safe on
the server, and the bot moves when the computer is back.

Exit codes:

| Code | Meaning |
|------|---------|
| 0 | Stopped normally. |
| 1 | An error, for example no token or a broken config. |
| 2 | The command line is wrong. |
| 3 | The token is not valid any more. Pair again. |
| 4 | Another `goban-connector` with the same token connected, so this one stops. |

### `goban-connector version`

Prints the version of the program.

## The two modes

**Native mode** (`--katago`). The connector starts `katago` itself. It makes
the config for each game from your base config plus the game's settings, and
it keeps the players' opening files in the data folder. This is the mode
for most people.

**Command mode** (`--command`). The connector calls a program that works
like the `goban-katago` wrapper that Goban uses on its own server (in the
Goban repository, `jetson/goban-katago`). Use it if KataGo runs in Docker or
on another machine and you have such a wrapper. `--env` adds environment
variables for the command, for example `--env GOBAN_KATAGO_PREFIX=gobanconn-`.

## The config file

It is a JSON file. The place is:

- the path in the environment variable `GOBAN_CONNECTOR_CONFIG`, if it is set;
- else `goban-connector/config.json` in your user config folder
  (`~/.config` on Linux, `~/Library/Application Support` on macOS,
  `%AppData%` on Windows).

The program writes it with mode 0600 (and a new folder with 0700), because
it holds your token.

| Field | Meaning |
|-------|---------|
| `server` | Goban's address. Default `https://go.lyabah.com`. |
| `token` | The secret token. Set by `pair`. |
| `connectorId` | The connector's number on Goban. Set by `pair`. |
| `name` | The connector's name. Set by `pair`. |
| `mode` | `native` or `command`. Set by `init`. |
| `command` | Command mode: the wrapper-compatible command. |
| `env` | Command mode: a list of `KEY=VALUE` strings. |
| `katago` | Native mode: the `katago` program. |
| `model` | Native mode: the network file. |
| `humanModel` | Native mode: the human network file. Optional. |
| `gtpConfig` | Native mode: base config for games. |
| `analysisConfig` | Native mode: base config for analysis. |
| `dataDir` | Native mode: the folder for the players' files and the configs of running processes. Default: `data` next to the config file. |
| `maxGames` | How many games this computer is meant to play at once. Default 2. The connector tells Goban this number and Goban stores it, but it does not limit the games yet. |

You can edit the file by hand. Restart `goban-connector run` after a change.

## Security

- The connector only makes an outbound connection to Goban. It does not
  listen on any port.
- The token is a secret. Anyone with the token can connect as your
  computer. The config file is only readable by you. The program never
  prints the token or writes it to its log. If it leaks, remove the
  connector on the Connectors page and pair again.
- The connector runs only a short list of fixed commands (start a game,
  start analysis, store or read a game file, clean up). Goban sends only
  names, numbers and settings for them. The connector checks every one of
  them again on your side. Text from Goban never goes to a shell.
- The settings of a game are checked line by line. Only simple settings are
  allowed. Settings that name files or folders (log folders, `homeDataDir`,
  anything ending in `File`, `Dir`, `Path` and so on) are refused. The
  only exception is a player's own opening folders inside the data folder.
- A game file (SGF) may be at most 5242880 bytes. A message from Goban may
  be at most 16 MiB.
- Goban can only reach the games and files of the account that owns the
  token.
- When the connection drops, the connector stops all KataGo processes. KataGo
  never runs with nobody listening.
- Run only one connector per token. A second one with the same token makes
  the first one stop (exit code 4).

## Run it as a service

Run `goban-connector pair` and `goban-connector init` by hand first. Then
check that `goban-connector run` works. After that, let the system start it.
Use full paths in the service files.

Exit codes 3 and 4 mean that starting again does not help, so the examples
do not restart on them.

### Linux: a systemd user unit

Save this as `~/.config/systemd/user/goban-connector.service`. Change the
path of `goban-connector` if it is somewhere else.

    [Unit]
    Description=Goban connector
    After=network-online.target
    Wants=network-online.target

    [Service]
    ExecStart=%h/go/bin/goban-connector run
    Restart=on-failure
    RestartSec=5
    RestartPreventExitStatus=3 4
    # Use another config file, if you want:
    # Environment=GOBAN_CONNECTOR_CONFIG=%h/.config/goban-connector/config.json

    [Install]
    WantedBy=default.target

Then:

    systemctl --user daemon-reload
    systemctl --user enable --now goban-connector
    journalctl --user -u goban-connector -f

By default a user service stops when you log out. To keep it running, use
`loginctl enable-linger $USER`.

### Linux without lingering: cron and a restart loop

Lingering is a systemd setting for each user. When it is on, the user's own
systemd starts at boot and keeps running when nobody is logged in. When it
is off, a user unit starts only when you log in and stops when you log out.
Turning it on may need `sudo`. If you cannot or do not want to turn it on,
use cron and a small loop instead. This needs no `sudo`.

The loop starts `goban-connector run` again when it stops, and cron starts
the loop at boot.

1. Save this as `~/goban-connector/keep-running.sh`. Change the path of
   `goban-connector` if it is somewhere else.

        #!/bin/sh
        # Starts goban-connector again when it stops, except after exit
        # code 0 (stopped normally), 3 (the token is not valid any more)
        # or 4 (another connector with the same token took over).
        LOG="$HOME/goban-connector/connector.log"
        while true; do
          "$HOME/go/bin/goban-connector" run >> "$LOG" 2>&1
          status=$?
          case $status in
            0 | 3 | 4) echo "goban-connector stopped with exit code $status" >> "$LOG"; exit "$status" ;;
          esac
          sleep 5
        done

2. Make it runnable and start it now, so it does not stop when you log
   out:

        chmod 755 ~/goban-connector/keep-running.sh
        setsid nohup ~/goban-connector/keep-running.sh >/dev/null 2>&1 < /dev/null &

3. Add a line to your crontab (`crontab -e`), so it starts at boot. Cron
   does not understand `~` here, so write the full path:

        @reboot /home/YOURNAME/goban-connector/keep-running.sh

Check that exactly one connector runs, and read its log:

    pgrep -af 'goban-connector run'
    tail -f ~/goban-connector/connector.log

To stop it, stop the loop first and then the program. Remove the crontab
line too if it should not start at boot:

    pkill -f keep-running.sh
    pkill -f 'goban-connector run'

The log grows without a limit. Empty it from time to time
(`: > ~/goban-connector/connector.log`).

Goban's Jetson runs its connector this way when lingering is off. There
the script `scripts/jetson-connector.sh` of the Goban repository
does all of the above: pairing, `init`, the loop, the crontab line, and a
size limit for the log.

### macOS: a launchd agent

Save this as `~/Library/LaunchAgents/com.lyabah.goban-connector.plist`.
Change `YOURNAME` and the path of `goban-connector`. launchd does not
understand `~`, so write full paths.

    <?xml version="1.0" encoding="UTF-8"?>
    <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
    <plist version="1.0">
    <dict>
      <key>Label</key>
      <string>com.lyabah.goban-connector</string>
      <key>ProgramArguments</key>
      <array>
        <string>/usr/local/bin/goban-connector</string>
        <string>run</string>
      </array>
      <key>RunAtLoad</key>
      <true/>
      <key>KeepAlive</key>
      <dict>
        <key>SuccessfulExit</key>
        <false/>
      </dict>
      <key>StandardErrorPath</key>
      <string>/Users/YOURNAME/Library/Logs/goban-connector.log</string>
      <key>StandardOutPath</key>
      <string>/Users/YOURNAME/Library/Logs/goban-connector.log</string>
    </dict>
    </plist>

Then:

    launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.lyabah.goban-connector.plist

To stop it:

    launchctl bootout gui/$(id -u)/com.lyabah.goban-connector

launchd cannot skip a restart for one exit code. If the token is not valid
any more, it will start the program again and again, slowly. Stop the agent
and pair again.

### Windows

Use Task Scheduler: a task that runs `goban-connector.exe run` at log on.

## Build and release

    go test -race ./... && go vet ./...
    go build -ldflags "-X main.version=1.0.0" ./cmd/goban-connector

A release is made by [GoReleaser](https://goreleaser.com) when a tag that
starts with `v` is pushed (for example `v0.1.0`). It builds Linux, macOS and
Windows programs for amd64 and arm64, with archives and a checksum file.

## License

MIT. See [LICENSE](LICENSE).
