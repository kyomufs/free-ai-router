# free-ai-router — a NixOS module for a gateway that aggregates free AI
# providers.
#
# WHAT THIS IS. A single, self-contained module for a machine running plain
# NixOS. It builds the gateway from its tagged release, declares its
# configuration, and runs it as a systemd USER service. It needs nothing beyond
# nixpkgs — no flakes of its own, no secret-management integration, no host
# abstraction.
#
# WHERE THE SETTINGS LIVE. The module OWNS config.json: the file is rendered from
# the options and rewritten on every activation. Editing it by hand is therefore
# pointless on a machine that uses this module — the next switch overwrites the
# edit. That is the intended trade: the settings become part of the host's git
# history, and the way to change one is to change the declaration.
#
# FOR DEBUGGING, the daemon also reads flags and environment variables, which
# outrank the file. A systemd drop-in is the way to try a value on a live daemon
# without touching the declaration:
#
#   systemctl --user edit free-ai-router
#   # [Service]
#   # ExecStart=
#   # ExecStart=%h/.local/bin/free-ai-router up --listen 127.0.0.1:8787 --family v4
#
# The empty ExecStart= is not optional: systemd appends to the list rather than
# replacing it, and a unit with two ExecStart lines is invalid.
#
# USAGE
#
#   imports = [ ./free-ai-router.nix ];
#
#   services.free-ai-router = {
#     enable = true;
#     providers.opencode.type = "opencode";
#   };
#
# Everything else has a working default. See the options below.

{ config, lib, pkgs, ... }:

let
  cfg = config.services.free-ai-router;
  inherit (lib) mkIf mkOption mkEnableOption types optional optionalString;

  # The gateway is built from a tagged release of its own repository. The
  # version and both hashes move together: `rev` is a tag rather than a commit
  # so that the source is a name a human can check, and the hashes make the
  # build reproducible without a network fetch at evaluation time.
  #
  # `mkDefault` rather than a plain assignment: the option exists so that a
  # caller can substitute a build of their own — a source checkout, a patch
  # under test — and that has to win over this default.
  defaultPackage = pkgs.callPackage ./package.nix { };

  # The first user is taken from the ordinary NixOS option. The service is a
  # USER unit because the gateway is a per-user gateway: it listens on loopback,
  # its state belongs in the user's home, and it needs no privileges at all.
  user = cfg.user;

  # config.json is rendered from the options IN FULL, and the file is what the
  # daemon reads on a machine without flags.
  #
  # Keys are snake_case, exactly as the daemon spells them. A mismatch is silent
  # by nature — the file is written, the daemon starts, the value never applies —
  # which is why the daemon rejects an unknown key instead of ignoring it, and a
  # typo surfaces at startup rather than as a setting that quietly does nothing.
  configFile = pkgs.writeText "free-ai-router-config.json" (
    builtins.toJSON {
      mode = cfg.mode;
      listen = cfg.listen;
      transport = cfg.transport;
      upstream = cfg.upstream;

      # The token is written as given and left empty when unset. An empty value
      # means "no check" in local mode; in server mode the daemon refuses to
      # start without one. The token is NOT rendered from `tokenFile`: that file
      # exists so the secret stays out of /nix/store, and copying its contents
      # here would defeat the purpose.
      token = cfg.token;

      credential = cfg.credential;
      key_rotation = cfg.keyRotation;
      family = cfg.family;

      first_event_timeout = cfg.firstEventTimeout;
      idle_timeout = cfg.idleTimeout;
      reasoning_idle_timeout = cfg.reasoningIdleTimeout;

      # Each provider is a separate section with its own keys: one service's
      # credentials must not leak into requests addressed to another.
      #
      # Empty fields are dropped rather than written empty. The daemon reads a
      # missing field as "not set", while an empty string in base_url would be a
      # configuration error rather than its absence — so filtering here is what
      # keeps the written file free of values nobody chose.
      providers = lib.mapAttrs
        (_: p:
          lib.filterAttrs (_: v: v != null && v != "" && v != [ ] && v != { }) {
            type = p.type;
            # A missing field means enabled, so an absent key and `true` are the
            # same thing. Only `false` is written deliberately, and it is kept in
            # the file because a switch that never reached it would do nothing.
            enabled = p.enabled;
            base_url = p.baseUrl;
            # The key itself goes in only when it was given. Prefer apiKeyEnv:
            # this file lands in /nix/store, which is world-readable.
            api_key = p.apiKey;
            api_key_env = p.apiKeyEnv;
            credential = p.credential;
            key_rotation = p.keyRotation;
            settings = p.settings;
          })
        cfg.providers;
    }
  );
in
{
  options.services.free-ai-router = {
    enable = mkEnableOption "the free-ai-router gateway for free AI providers";

    package = mkOption {
      type = types.package;
      default = defaultPackage;
      defaultText = lib.literalExpression "the tagged release, built by ./package.nix";
      description = ''
        The gateway package to run.

        Set this to run a binary you built yourself — a source checkout, a
        patched build, a test of a fix — without touching the rest of the
        declaration. The module only needs {file}`bin/free-ai-router` from it.
      '';
    };

    user = mkOption {
      type = types.str;
      default = lib.head (lib.attrNames config.users.users);
      defaultText = lib.literalExpression "the first user declared in users.users";
      description = ''
        The user the service runs as, and whose home holds the configuration and
        the state.

        The service is a USER unit, so this is also the account that must be
        active for it to run. If the gateway should be up before anyone logs in,
        enable lingering:

        ```nix
        users.users.<name>.linger = true;
        ```
      '';
    };

    mode = mkOption {
      type = types.enum [ "local" "server" ];
      default = "local";
      description = ''
        The deployment shape.

        `local` listens on loopback and does not check the token: nobody but this
        machine can reach the listener. `server` listens on a reachable address
        and REQUIRES a token — a tokenless public listener spends the quota on
        behalf of anyone who finds the port, so the daemon refuses to start
        without one.

        Changing this needs a restart: the location is not an axis that is
        switched on a live daemon.
      '';
    };

    listen = mkOption {
      type = types.str;
      default = "127.0.0.1:8787";
      description = ''
        The address the gateway listens on, serving the OpenAI-compatible
        {file}`/v1` surface and the monitoring surface under {file}`/_control`.

        Keep the default unless clients on other machines need it. Exposing it
        means every request that reaches the port is made on your behalf.
      '';
    };

    token = mkOption {
      type = types.str;
      default = "";
      description = ''
        The bearer token clients present in server mode.

        WARNING: this value is rendered into the world-readable Nix store. It is
        acceptable for a throwaway token on a test machine and NOT for a real
        one — use {option}`tokenFile` instead, which keeps the secret out of the
        store.
      '';
    };

    tokenFile = mkOption {
      type = types.nullOr types.path;
      default = null;
      example = "/run/secrets/free-ai-router-token";
      description = ''
        A file holding the token, read by the service at start time through an
        `EnvironmentFile`.

        This is the path to use for a real token. The file has to be readable by
        {option}`user` and no one else, and it must already exist when the
        service starts — a missing file makes systemd refuse the start rather
        than run the daemon without a token.

        The expected format is `KEY=value`, which is what systemd reads:

        ```
        FREE_AI_ROUTER_TOKEN=<your token>
        ```

        The environment outranks the configuration file, so this value wins over
        {option}`token`.
      '';
    };

    credential = mkOption {
      type = types.enum [ "anonymous" "account" ];
      default = "anonymous";
      description = ''
        What the gateway presents to the providers:

        - `anonymous` — nothing. The request goes out with the service's own
          anonymity marker, and the free tier is counted against the address the
          request came from. A key configured in a provider section is NOT sent
          in this mode.
        - `account` — a key from the provider's section, counted against the
          account. A provider in this mode MUST have a key: the daemon refuses
          to start without one.

        A default, not a law: a provider's own {option}`credential` overrides it,
        which is how one service is reached anonymously while another needs a
        key.
      '';
    };

    keyRotation = mkOption {
      type = types.enum [ "on" "off" ];
      default = "on";
      description = ''
        Whether a refusal that names the key spends the next key from the pool:

        - `on` — take another key and repeat the request once;
        - `off` — pass the refusal to the client and keep the current key.

        IGNORED under `credential = "anonymous"`. No key is presented there, so
        there is nothing to replace, and the setting is kept rather than
        pretending to act.
      '';
    };

    family = mkOption {
      type = types.enum [ "auto" "v4" "v6" ];
      default = "auto";
      description = ''
        The address family the outgoing traffic uses: `auto`, `v4` or `v6`.

        This is about ACCOUNTING rather than preference. Free tiers are counted
        per source address, and a provider sees only the address the connection
        arrived from. Pinned to one family, the gateway spends one address's
        allowance and the per-address view in the monitor shows exactly which;
        left on `auto`, the same daemon reaches the service through different
        addresses and the counters stop being comparable.
      '';
    };

    transport = mkOption {
      type = types.enum [ "direct" ];
      default = "direct";
      description = ''
        How requests leave the machine. The only value is `direct`: the host's
        own routing with a warm connection pool.

        The pool is not an optimisation here but part of the job — a turn
        consists of many requests, and without connection reuse each one would
        pay for a new TCP and TLS handshake.
      '';
    };

    upstream = mkOption {
      type = types.str;
      default = "https://opencode.ai";
      description = "The default address for providers that do not name their own.";
    };

    firstEventTimeout = mkOption {
      type = types.str;
      default = "30s";
      description = "How long to wait for response headers before treating the attempt as hung.";
    };

    idleTimeout = mkOption {
      type = types.str;
      default = "120s";
      description = "Close the stream after this much silence from an ordinary model.";
    };

    reasoningIdleTimeout = mkOption {
      type = types.str;
      default = "300s";
      description = ''
        Close the stream after this much silence from a model that reasons: they
        hold pauses noticeably longer, and the ordinary budget would cut them off
        mid-thought.
      '';
    };

    providers = mkOption {
      default = { };
      description = ''
        The providers, one section per service. The attribute name is the
        identifier the service is visible under, and it becomes part of every
        model name (`<provider>/<model>`), so names from different services
        cannot collide.
      '';
      type = types.attrsOf (types.submodule {
        options = {
          type = mkOption {
            type = types.enum [ "opencode" "stub" ];
            description = ''
              The code serving this section:

              - `opencode` — OpenCode Zen, spoken to the way its own client
                speaks to it;
              - `stub` — a provider answering from inside the gateway with no
                network at all. It exists so the whole path can be exercised
                without a key, and it is the only type that declares its model
                catalog, because there is no service to ask.
            '';
          };

          enabled = mkOption {
            type = types.nullOr types.bool;
            default = null;
            description = ''
              Whether the section is live. `null` and `true` are the same thing:
              a missing field means enabled.

              `false` keeps the section in the configuration while the daemon
              ignores it, which is how a provider is parked without losing its
              settings — and it is also exempt from the "account needs a key"
              check, since it is not going to serve.
            '';
          };

          baseUrl = mkOption {
            type = types.str;
            default = "";
            description = "The service address. Empty takes the default for this type.";
          };

          apiKey = mkOption {
            type = types.str;
            default = "";
            description = ''
              The key, written directly into the configuration.

              Prefer {option}`apiKeyEnv`: this file is rendered into the
              world-readable Nix store, so a key declared here is a published
              secret. Acceptable for a throwaway key on a test machine, not for a
              real account.
            '';
          };

          apiKeyEnv = mkOption {
            type = types.str;
            default = "";
            description = ''
              The name of an environment variable holding the key.

              The variable counts only when it is actually SET in the service's
              environment: a name without a value is not a key, and the daemon
              refuses to start an `account` provider that has none.
            '';
          };

          credential = mkOption {
            type = types.nullOr (types.enum [ "anonymous" "account" ]);
            default = null;
            description = ''
              This provider's credential. `null` inherits the global one.

              Inheritance and an explicit `"anonymous"` are not the same thing:
              an inherited value follows the global setting when it changes, a
              written-out one does not.
            '';
          };

          keyRotation = mkOption {
            type = types.nullOr (types.enum [ "on" "off" ]);
            default = null;
            description = "This provider's key rotation. `null` inherits the global one.";
          };

          settings = mkOption {
            default = { };
            type = types.attrsOf types.anything;
            description = ''
              Whatever the chosen {option}`type` understands and nothing else.

              It is passed through untouched, which is why it is not spelled out
              as options here: the values each type reads are documented with
              that type. `settings.keys` is the one entry that is common — it
              declares a pool of keys when there is more than one.
            '';
          };
        };
      });
    };
  };

  config = mkIf cfg.enable {
    # The binary goes on the system PATH so the CLI is available from a shell.
    # There is deliberately no wrapper: the monitor is built into the binary, so
    # nothing else has to be installed for it to work.
    environment.systemPackages = [ cfg.package ];

    # THE CONFIGURATION IS SEEDED ON ACTIVATION, not by the unit.
    #
    # It is written by an activation script rather than by an ExecStartPre, and
    # the difference matters: the file must exist and be correct BEFORE the
    # service starts, including on the boot where the service is already enabled.
    # An ExecStartPre would do the same work on every restart, and it would also
    # mean the file is only ever right while the service is being started.
    #
    # It is COPIED rather than linked into the store. The CLI and the daemon WRITE
    # to this file — `free-ai-router cred account` rewrites it — and a symlink
    # into the read-only store would break on the first write. This is also why
    # the file is not managed by an option that produces a link.
    #
    # The guard is what keeps a switch quiet: an unchanged file is not rewritten,
    # so its timestamp stays put and nothing downstream reacts to a no-op.
    system.activationScripts.free-ai-router-config = ''
      cfgDir="/home/${cfg.user}/.config/free-ai-router"
      install -d -m 700 -o ${cfg.user} -g ${cfg.user} "$cfgDir"
      if ! cmp -s ${configFile} "$cfgDir/config.json"; then
        install -m 600 -o ${cfg.user} -g ${cfg.user} ${configFile} "$cfgDir/config.json"
      fi
    '';

    systemd.user.services.free-ai-router = {
      description = "free-ai-router — a gateway aggregating free AI providers";
      wantedBy = [ "default.target" ];

      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      serviceConfig = {
        Type = "simple";
        ExecStart = "${cfg.package}/bin/free-ai-router up --listen ${cfg.listen}";

        # Restart on failure: the gateway is meant to be up continuously, and a
        # crash caused by a provider hiccup should not need a human.
        Restart = "on-failure";
        RestartSec = 5;

        # The XDG roots the daemon resolves its paths from. They are set
        # explicitly so that the unit does not depend on whatever environment
        # systemd happens to hand it.
        Environment = [
          "XDG_CONFIG_HOME=%h/.config"
          "XDG_STATE_HOME=%h/.local/state"
        ];

        # The token arrives through the environment rather than through the
        # command line: a value in Environment would be written into the unit
        # file, which is itself a store path — exactly where the token must not
        # be. systemd reads `KEY=value` from this file, which is the format the
        # daemon expects too.
        #
        # A missing file makes systemd refuse the start rather than run the
        # daemon without a token, which is the behaviour worth having.
        EnvironmentFile = optional (cfg.tokenFile != null) (toString cfg.tokenFile);

        # A private /tmp and a ban on gaining privileges: the gateway needs
        # neither. Cheap insurance rather than a substitute for the listen
        # address and the token.
        PrivateTmp = true;
        NoNewPrivileges = true;
      };
    };
  };
}
