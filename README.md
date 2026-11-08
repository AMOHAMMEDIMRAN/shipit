# shipit

Put a project on your own server from your own computer. No CI service, no agent on the server.

shipit sends a git commit over SSH, builds it next to the live copy, switches over only when the
new version answers its health check, and puts the old version back by itself if it does not.

```
$ shipit            # first time: asks a few questions. After that: a short menu
$ shipit deploy     # send the latest version live
$ shipit rollback   # go back to the last version that worked
```

> Status: **0.2, early.** Linux and macOS servers over SSH. See "What works" below.

## Install

```
make build          # builds ./shipit (needs Go)
```

Put the `shipit` binary anywhere on your `PATH`. `make dist` builds Linux, macOS and Windows binaries.

## First time

Run `shipit` in your project folder and answer the questions. Press `?` at any of them for help.

shipit asks, in order: which server (and how to log in), what the project is called, how it should be
deployed (it lists what it found: compose files, a Dockerfile, a Node app, or "run my own commands"),
a few details, which folder on the server to use, and **which file holds the keys for production**.
Local files such as `.env.local` are never offered for production.

The answers are saved in `.shipit/<environment>.conf`. That file holds no secrets, so commit it.
Your server logins are saved once, on your computer only, in `~/.config/shipit/servers.conf`.

## Versions

A version is a git tag.

```
git tag v1.2.0 && git push origin main --tags
shipit deploy --tag v1.2.0
shipit history                # what was deployed
shipit rollback --to v1.1.0
```

Without `--tag`, shipit deploys the latest commit and names it from the nearest tag.

## Commits

If you deploy with changes you have not committed, shipit shows what changed, suggests a message
written from those changes, and commits only after you say yes. You can edit the message, deploy
the last commit without the changes, or stop.

```
shipit deploy                   # offers to commit first
shipit deploy --commit          # commits with a written message, then deploys, no questions
shipit commit                   # just the commit
shipit commit -m "Fix login"    # your own message
```

- Only the files it lists are committed. Anything that looks like a key or settings file
  (`.env*`, `*.pem`, `*.key`) is always left out, even if you staged it yourself.
- The built-in message comes from file names and change counts, so it is accurate but plain.
  For a message written from what the code does, set a command that reads the changes on
  standard input and prints a message (the changes are sent to that command, so choose one you trust):

```
commit_message_cmd = claude -p "Reply with only the commit message"
```

## Keys and settings

Keys live in a file such as `.env.production` and stay out of git.

- Deploys never upload `.env` files with the code.
- `shipit secrets push` sends the production file to the server (mode 600, the old copy is backed up).
- `shipit secrets check` checks the server's file. It prints names and verdicts, never values.
- Add rules so a test key can never reach production:

```
[rules]
RAZORPAY_KEY_ID = ^rzp_live_
```

## What works

| Runs as | Notes |
|---|---|
| `compose` (docker or podman) | Reads your compose file and never edits it. Built images get a version tag, so going back reuses the old image. |
| `container` (docker or podman) | One Dockerfile. Build target, platform and build args are supported. Built on the server. |
| `pm2` | Starts with `pm2`; your app reads its keys from the linked `.env` file. |
| `systemd` | Restarts a unit you already have. Needs passwordless `sudo systemctl restart`. |
| `script` | Your own build and start commands. |

Also: `shipit commit`, `shipit doctor` (checks the server and says how to fix problems), `shipit detect`, `shipit server add|list|test|remove`,
`shipit envs`, `shipit logs`, `shipit status`, `shipit help <topic>`.

**Not yet:** Windows servers (OpenSSH is the plan), building on your laptop or in a registry,
Quadlet and static sites, notifications. The tool itself runs on Linux, macOS and Windows.

## Develop

```
make test      # unit tests
make e2e       # deploys a demo app to this computer, including a broken version
```

## License

MIT. See [LICENSE](LICENSE). Contributions are welcome: open an issue first for anything large.
