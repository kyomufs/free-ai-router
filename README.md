# free-ai-router

A gateway that puts several free AI providers behind one OpenAI-compatible
endpoint.

Each service is spoken to the way its own client speaks to it. Adding a service
is a section in the configuration, not a patch to the code. The gateway is a
single static binary with a built-in terminal monitor, and it tells you what it
is doing: which credential is in play, whether keys are being rotated, which
address the traffic left from.

```
client ──▶ free-ai-router ──▶ provider
           (request shape, keys, transport)
```

## Contents

- [Install on NixOS](#install-on-nixos)
- [Install on any other distribution](#install-on-any-other-distribution)
- [Configure](#configure)
- [Debugging without a rebuild](#debugging-without-a-rebuild)
- [Monitoring](#monitoring)
- [Client configuration](#client-configuration)
- [Reference](#reference)

## Install on NixOS

The module owns the binary, the configuration and the service. It needs nothing
beyond nixpkgs: no flakes of its own, no secret-management integration, no host
abstraction. Settings live in the declaration, so they end up in your
configuration's git history — and, for the same reason, the daemon's
`config.json` is rewritten from the options on every activation.

The module is `nix/module.nix` in this repository. A complete setup is two files.

### `flake.nix`

```nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        ./configuration.nix
        # The module is fetched as a flake input of its own repository, so a
        # `nix flake update` moves it together with nixpkgs.
        ./free-ai-router.nix
      ];
    };
  };
}
```

If you would rather not vendor the file, import it straight from the flake and
add that flake as an input:

```nix
{
  inputs.free-ai-router.url = "github:kyomufs/free-ai-router";
  # ...
  modules = [
    "${inputs.free-ai-router}/nix/module.nix"
    # ...
  ];
}
```

### `configuration.nix` — the minimal setup

```nix
{ ... }:

{
  services.free-ai-router = {
    enable = true;
    # Everything below is a default, shown so that you know what is in force.
    # It is a complete configuration as it stands.
    providers.opencode.type = "opencode";
  };
}
```

```sh
sudo nixos-rebuild switch --flake .#myhost
```

That gives you a gateway on `127.0.0.1:8787`, reachable only from this machine,
presenting no credential to the provider and therefore spending the free tier's
allowance against your address:

```sh
curl http://127.0.0.1:8787/v1/models
```

### What the module sets up

| Piece | What it does |
|---|---|
| `environment.systemPackages` | puts the binary on PATH. No wrapper and no extra packages — the monitor is inside the binary |
| `system.activationScripts.free-ai-router-config` | writes `~/.config/free-ai-router/config.json` from your options on every activation |
| `systemd.user.services.free-ai-router` | a **user** unit, `Restart=on-failure`, XDG roots pointed at your home |

Two details are worth knowing before they surprise you.

`config.json` is an ordinary file, not a symlink into `/nix/store`. The CLI and
the daemon **write** to it — `free-ai-router cred account` rewrites it — and a
symlink into the read-only store would break on the first write. Editing it by
hand is still pointless on a machine that uses this module: the next `switch`
overwrites the edit. To change a setting, change the declaration; to try one
without a rebuild, see [Debugging](#debugging-without-a-rebuild).

The unit is a **user** unit, so it runs with your session:

```sh
systemctl --user status free-ai-router
systemctl --user restart free-ai-router
journalctl --user -u free-ai-router -f
```

It starts when you log in. To have it up before that, enable lingering for the
service user:

```nix
users.users.myuser.linger = true;
```

### Reaching it from other machines

`local` mode is loopback-only and checks no token. To serve other clients,
switch to `server` — which makes the token **mandatory**, because a tokenless
listener on a reachable address spends your quota on behalf of anyone who finds
the port:

```nix
services.free-ai-router = {
  mode = "server";
  listen = "0.0.0.0:8787";
  tokenFile = "/run/secrets/free-ai-router-token";
  providers.opencode.type = "opencode";
};
```

The file named by `tokenFile` must exist when the service starts and contain:

```
FREE_AI_ROUTER_TOKEN=<your token>
```

It is read through systemd's `EnvironmentFile` and outranks the `token` option,
so the secret never lands in `/nix/store`. A missing file makes systemd refuse the
start rather than run the gateway without a token.

Do not put a real token in the `token` option: that value is rendered into the
world-readable store. It is fine for a throwaway token on a test machine.

If you already manage secrets with sops-nix, point `tokenFile` at the decrypted
path and declare the secret in the usual way:

```nix
sops.secrets.free-ai-router-token = {
  sopsFile = ./secrets.yaml;
  owner = "myuser";
  mode = "0400";
};

services.free-ai-router.tokenFile =
  config.sops.secrets.free-ai-router-token.path;
```

The module does not depend on sops-nix in any way; this is only where the two
meet.

### Using a key or a pool of keys

`credential` decides **whether** a key is presented, and the provider section
supplies it:

```nix
services.free-ai-router = {
  enable = true;
  credential = "account";           # a key is presented on every request

  providers.opencode = {
    type = "opencode";
    # A variable NAME is safer than the key itself: apiKey is rendered into
    # /nix/store, which is world-readable.
    apiKeyEnv = "OPENCODE_API_KEY";
    # or several, for rotation:
    # settings.keys = [ "sk-one" "sk-two" ];
  };
};
```

`account` without a key is refused — at startup, not at the first request, and
the message names the fix. The variable in `apiKeyEnv` counts only when it is
actually set in the service's environment.

`keyRotation` decides what happens on a refusal that names the key: `on` takes
another key from the pool, `off` passes the refusal to the client. Under
`credential = "anonymous"` it is ignored — no key is presented there, so there is
nothing to replace. See [How `credential` decides](#how-credential-decides) for
the full table.

### Checking the deployment

```sh
systemctl --user status free-ai-router     # the unit
free-ai-router config                      # what is in force, and from where
free-ai-router status                      # providers, addresses, counters
curl http://127.0.0.1:8787/_control/status # the same, for the monitor
```

`free-ai-router config` is the quickest sanity check after a switch: it prints
every axis with its source, so a value that reached the daemon and one that did
not are told apart immediately.

## Install on any other distribution

No Nix, no module, no declaration: `install.sh` in this repository builds the
binary, puts it on PATH, and optionally writes a systemd **user** unit.

```sh
git clone https://github.com/kyomufs/free-ai-router
cd free-ai-router
./install.sh                 # build and install into ~/.local/bin
./install.sh --unit          # also write the systemd user unit
```

Other forms:

```sh
PREFIX=/opt/far ./install.sh            # a different prefix, still no sudo
./install.sh --no-build                 # install an existing ./free-ai-router
```

What it deliberately does not do:

- it does not touch `/usr` and does not install a system-wide unit;
- it does not write into a Nix profile, and **refuses** to install over a binary
  Nix already owns (it checks the *resolved* path, so a profile symlink is
  caught too) — on a NixOS machine, use the module;
- it does not edit an existing `config.json`: settings belong to you, and the CLI
  is how you change them.

If you built with `--no-build`, the binary must sit next to the script:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o free-ai-router ./cmd/free-ai-router
./install.sh --no-build
```

Enable the service after `--unit`, which writes the file but enables nothing (a
decision about this machine's boot belongs to its owner):

```sh
systemctl --user daemon-reload
systemctl --user enable --now free-ai-router
```

### By hand

`install.sh` is a convenience, not magic. The whole install is:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ~/.local/bin/free-ai-router ./cmd/free-ai-router
mkdir -p ~/.config/systemd/user
free-ai-router systemd --install          # writes the unit, prints the next two commands
```

`CGO_ENABLED=0` is not optional: the result is a static binary with no runtime
dependencies, which is why the unit needs neither `LD_LIBRARY_PATH` nor a
wrapper.

## Configure

Three levels, each overriding the one below:

```
flags  →  environment variables  →  config.json
```

Every setting has all three. `axes` prints the complete table, and it is
generated from the same source the validation uses, so it cannot go stale:

```sh
free-ai-router axes            # human-readable, with the environment variable name
free-ai-router axes --json     # machine-readable
```

### Two ways to change something

```sh
# Adopt a value: writes config.json, survives a restart.
free-ai-router cred anonymous
free-ai-router rotate off
free-ai-router family v4

# Try a value: writes nothing, applies to this run.
free-ai-router up --rotate off --family v4
```

The one-axis commands write the value even when the result could not run, and
that is deliberate: a configuration is allowed to be incomplete while it is being
assembled, and a command that refused would seal the file shut against the very
step that can repair it. `credential account` on a keyless configuration is the
case to know about — it is accepted, it is written, and the **daemon refuses to
start** until a key appears:

```sh
$ free-ai-router cred account && free-ai-router up
credential: account
written to ~/.config/free-ai-router/config.json
error: config: provider "opencode": credential "account" requires a key, and none
is configured: set api_key, api_key_env, or settings.keys — or switch the
credential to "anonymous"
```

Add the key — through the control surface, which is the only way in while the
daemon runs — or put it in the section and start again:

```sh
# While a daemon is up (local mode needs no token):
curl -X POST http://127.0.0.1:8787/_control/keys/add \
  -H 'content-type: application/json' \
  -d '{"provider":"opencode","key":"sk-..."}'

# Or in the file, which survives a restart:
#   "providers": { "opencode": { "type": "opencode", "api_key": "sk-..." } }
```

Without a value, an axis command reports what is in force **and where it came
from** — the question worth asking while debugging a merge:

```sh
$ free-ai-router credential
credential: anonymous (config.json)
allowed: anonymous | account
```

### The axes that matter

| Axis | Values | What it decides |
|---|---|---|
| `mode` | `local` \| `server` | loopback with no token check, or a reachable listener where the token is mandatory |
| `credential` | `anonymous` \| `account` | whether a key is presented at all (counted against your address) or one is taken from the pool (counted against your account) |
| `key_rotation` | `on` \| `off` | whether a refusal that names the key spends the next key from the pool |
| `model_fallback` | `on` \| `off` | whether a model that cannot answer at all is replaced by another free model of the same provider — **off by default**, because the answer then names a model other than the one asked for |
| `family` | `auto` \| `v4` \| `v6` | the address family the traffic leaves from |
| `transport` | `direct` | how requests leave the machine |

### How `credential` decides

`credential` chooses the **source** of the credential; the pool only supplies the
value. The mode is a statement about what goes on the wire, not about what
happens to be lying in the configuration file:

| | `credential: "anonymous"` | `credential: "account"` |
|---|---|---|
| a key is present in the section | **not sent** — the service's own anonymity marker goes out instead | sent |
| there is no key | fine; the marker goes out | **the daemon refuses to start** |
| `key_rotation: on` | ignored — there is no key to replace | the pool is spent on a refusal that names the key |
| `key_rotation: off` | ignored | the current key is kept and the refusal reaches the client |

Two of those deserve a word.

A key on file under `anonymous` is **not consent to spend it**. The common case is
an operator who keeps a key configured while testing anonymously, and a pool that
leaked into the request would make the mode a label rather than a behaviour —
with the traffic attributed to an account that was meant to stay untouched.

`account` without a key is refused **at startup**, not at the first request. The
daemon's job is to serve, and a deployment that cannot serve should say so before
it accepts a single client. The refusal names all three places a key can come
from:

```
error: config: provider "opencode": credential "account" requires a key, and none
is configured: set api_key, api_key_env, or settings.keys — or switch the
credential to "anonymous"
```

`api_key_env` counts only when the variable is actually **set**: a name without a
value is not a key, and trusting the name alone would let the daemon start and
then reach the service without a credential — the likeliest shape of the bug in a
unit file, where the variable arrives from an `EnvironmentFile` that may not have
been written yet.

`family` exists because of how free tiers are counted: **per source address**.
Pinned to one family, the daemon spends one address's allowance and the per-IP
matrix shows exactly which. On `auto`, the same daemon reaches the service
through different addresses and the counters stop being comparable.

### Providers

```json
{
  "credential": "anonymous",
  "key_rotation": "on",
  "providers": {
    "opencode": { "type": "opencode" },
    "stub": { "type": "stub" }
  }
}
```

The key in `providers` is the identifier the service is visible under, and it
becomes part of every model name: `<provider>/<model>`. That is why model names
from different services cannot collide.

`api_key_env` is preferable to `api_key`: a variable name in the configuration is
safer than the key itself, because the declarative file ends up world-readable in
`/nix/store`.

A real service's model catalog is **not** part of the configuration. It lives
with the service and is learned by asking: a copy in the configuration would go
stale the day the service changed. Only `stub` declares its catalog, because
there is no service to ask.

What a service demands on the wire belongs with the service, not with this
README: [`docs/providers/`](docs/providers/) holds one file per provider.
[`opencode_zen.md`](docs/providers/opencode_zen.md) records the request shape the
`opencode` lane admits, the refusals it returns, and its free model list.

## Debugging without a rebuild

On NixOS the module rewrites `config.json` on every activation, so anything you
edit in the file is gone at the next `switch`. That is by design, and it is also
exactly why debugging through the declaration is painful: a one-value experiment
costs a full rebuild.

There are three ways out, in increasing order of isolation.

### 1. Flags on the unit (no rebuild, but a restart)

`ExecStart` is fixed by the module, but a systemd drop-in overrides it without
touching the declaration — and drop-ins live in your home, outside the repo:

```sh
systemctl --user edit free-ai-router
```

```ini
[Service]
# Try a value for real, against the live system.
ExecStart=
ExecStart=/run/current-system/sw/bin/free-ai-router up --listen 127.0.0.1:8787 --family v4 --rotate off
```

```sh
systemctl --user daemon-reload
systemctl --user restart free-ai-router
```

To go back: `rm ~/.config/systemd/user/free-ai-router.service.d/*.conf`, then
`daemon-reload` and `restart`. Nothing in the declaration changed, so the next
`switch` re-lands the module's `ExecStart` anyway.

Env variables work the same way and are often tidier:

```sh
systemctl --user edit free-ai-router
# [Service]
# Environment=FREE_AI_ROUTER_FAMILY=v4
```

### 2. A second daemon on a spare port (nothing is touched at all)

The cleanest option: the system daemon keeps serving `:8787`, and your experiment
runs beside it on its own port, its own config and its own state.

```sh
mkdir -p ~/far-debug

# 1. Write a config for the experiment. It has to name at least one provider:
#    a gateway with an empty providers section refuses to start, which is the
#    right answer — it would have nothing to serve.
#
#    The credential is anonymous, so no key is needed. Asking for "account"
#    here would be refused at startup unless a key is configured — see the
#    table above.
cat > ~/far-debug/config.json <<'JSON'
{
  "credential": "anonymous",
  "key_rotation": "off",
  "family": "v4",
  "providers": { "opencode": { "type": "opencode" } }
}
JSON

# 2. See what would run, without starting anything.
free-ai-router up \
  --config ~/far-debug/config.json \
  --state ~/far-debug/state.json \
  --listen 127.0.0.1:8799 \
  --print-config

# 3. Start it. The daemon lives in the foreground, so put it in its own
#    terminal, or background it in this one.
free-ai-router up --config ~/far-debug/config.json --state ~/far-debug/state.json \
  --listen 127.0.0.1:8799 &

# 4. Watch it in another terminal.
free-ai-router tui --config ~/far-debug/config.json --state ~/far-debug/state.json

# 5. Talk to it.
curl http://127.0.0.1:8799/v1/models

# 6. Stop it when done — the system daemon never noticed.
free-ai-router stop --config ~/far-debug/config.json --state ~/far-debug/state.json
```

Two details make this safe, and both are easy to get wrong:

`--config` **and** `--state` together. With `--config` alone the second daemon
reads your experiment's settings but still **writes the system daemon's
`state.json`**, and the two then disagree about what happened — the per-address
matrix fills with traffic that never went through the system daemon.

`--detach` is not a shell detach. It starts the daemon, and the command keeps
holding the terminal for as long as the daemon runs, which is fine from a script
or a second terminal but looks like a hang in an interactive shell. Backgrounding
with `&` is what actually gives your prompt back.

`--print-config` is the fastest loop of all. It shows the fully merged result —
every axis and where its value came from — and exits without starting anything or
writing a file:

```sh
$ free-ai-router up --rotate off --family v4 --print-config
family                  v4                           (flag)
key_rotation            off                          (flag)
credential              anonymous                    (default)
...
file:      /home/you/.config/free-ai-router/config.json
state:     /home/you/.local/state/free-ai-router/state.json
```

### 3. A source build instead of the packaged binary

While changing the code, use your own build — nothing about the install matters
for that:

```sh
CGO_ENABLED=0 go build -trimpath -o /tmp/far ./cmd/free-ai-router
/tmp/far up --config ~/far-debug/config.json --listen 127.0.0.1:8799 --print-config
```

`CGO_ENABLED=0` is required in this environment (no C compiler) and is the target
mode anyway.

On NixOS there is a second way, and it is the one to use when the build has to be
the build the *service* runs: point the module at your package.

```nix
services.free-ai-router.package =
  pkgs.callPackage /path/to/free-ai-router/nix/package.nix { };
# or any derivation of your own, as long as it has bin/free-ai-router
```

The unit, the configuration and the state stay exactly as they are and only the
binary changes, so a single `switch` tests your build in place — which is what
the second-daemon route cannot do, since that one runs beside the service rather
than as it.

### Which one to reach for

| Situation | Use |
|---|---|
| Try one value, on the real service, for a few minutes | a drop-in override |
| Test a whole different setup, or compare two side by side | a second daemon |
| Change the code | your own build, on a spare port |
| Just want to see the merged result | `--print-config` |

Debugging on NixOS is not "fight the module": the declaration owns the *default*,
and flags own the *run*.

## Monitoring

```sh
free-ai-router tui        # or: free-ai-router tui --config <path> --state <path>
```

The monitor reads what the daemon wrote and never writes anything itself. Four
blocks:

- the active credential and the address it leaves from;
- whether key rotation is on, and how many keys remain;
- the log, scrollable, filterable, with wrap toggled on and off;
- the per-address model matrix — one row per address, `OK` and `ERROR` columns.

That last block is the reason the TUI exists. Free tiers are counted per source
address, so "which of my addresses has spent its allowance on which model" is
the question you actually ask, and a single global counter cannot answer it.

Without a terminal:

```sh
free-ai-router status          # mode, listen, addresses, per-provider counters
free-ai-router status --json
free-ai-router models          # the catalog the gateway serves
free-ai-router stats           # counters by day
free-ai-router keys            # the key pool
free-ai-router logs -n 50
```

The same information is on the control surface, which is what the TUI reads:

```sh
curl http://127.0.0.1:8787/_control/status
curl http://127.0.0.1:8787/_control/keys
```

In `mode = "server"` these need the token:

```sh
curl -H "Authorization: Bearer $FREE_AI_ROUTER_TOKEN" http://host:8787/_control/status
```

## Client configuration

The gateway speaks the OpenAI API, so any client that lets you set a base URL
works. Point it at `/v1` and give it any non-empty key — the gateway does not
check one in `local` mode, but most clients insist on a value.

```sh
# OpenAI-compatible clients
OPENAI_BASE_URL=http://127.0.0.1:8787/v1
OPENAI_API_KEY=unused
```

Model names are composite, and the provider is part of the name:

```sh
curl http://127.0.0.1:8787/v1/models
curl http://127.0.0.1:8787/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"opencode/deepseek-v4.1-flash-free","messages":[{"role":"user","content":"hello"}]}'
```

`/v1/models` reports what the provider declared: the context window, the output
ceiling, the input modalities, whether the model reasons and with which effort
levels. A capacity nobody declared is **omitted rather than zeroed**, so a client
can tell "unknown" from "nothing" and does not have to invent a limit.

For DeepSeek Harness there is a companion plugin,
[free-ai-router-dshplugin](https://github.com/kyomufs/free-ai-router-dshplugin),
which registers every provider the gateway serves as its own route and takes the
model capabilities from this catalog.

## Reference

### Commands

| Command | Purpose |
|---|---|
| `up [flags]` | start the daemon |
| `status [--json]` | daemon state |
| `stop` | stop it |
| `tui` | the monitor |
| `models [--json]` | the catalog |
| `stats [--json]` | counters by day |
| `keys [--json]` | the key pool |
| `logs [-n N]` | the log |
| `data [clear]` | what is on disk, and clearing it |
| `config [--json]` | the effective configuration |
| `axes [--json]` | every setting, its values, its default |
| `systemd [--install]` | print the user unit, or write it |
| `version` | the version |
| `mode` \| `cred`/`credential` \| `rotate`/`key_rotation` \| `family` | one axis, read or written |

### Startup flags

| Flag | Purpose |
|---|---|
| `--mode` | `local` \| `server` |
| `--listen` | the address to listen on |
| `--token` | the token for server mode |
| `--transport` | `direct` |
| `--upstream` | address for providers that do not name their own (empty: none) |
| `--family` | `auto` \| `v4` \| `v6` |
| `--credential` | `anonymous` \| `account` |
| `--rotate` | `on` \| `off` |
| `--first-event-timeout` | wait for response headers |
| `--idle-timeout` | allowed silence between events |
| `--reasoning-idle-timeout` | silence allowed while reasoning |
| `--config` | the configuration file to read |
| `--state` | the state file to write |
| `--print-config` | show the merged configuration and exit |
| `--detach` | start in the background |
| `--json` | machine-readable output |

### Files

| Path | What |
|---|---|
| `~/.config/free-ai-router/config.json` | the configuration (or `$XDG_CONFIG_HOME`) |
| `~/.local/state/free-ai-router/state.json` | counters and per-address history |
| `~/.local/state/free-ai-router/app.log` | the log |

`free-ai-router data` prints all three with their sizes, and
`free-ai-router data clear` empties the log or the counters. It never touches the
configuration: deleting the file that describes the daemon's design is not
maintenance but the loss of a deployment.

### Ports

`127.0.0.1:8787` by default, loopback only. The gateway serves `/v1/*` for
clients and `/_control/*` for the monitor.

## License

MIT
