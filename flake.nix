{
  description = "Calendar — Minimalist time-blocking calendar with CalDAV, Rosé Pine design & Vim navigation";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      # Supported systems
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];

      # Package definitions per architecture
      systemOutputs = flake-utils.lib.eachSystem supportedSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          packages = rec {
            calendar = pkgs.buildGoModule {
              pname = "calendar";
              version = "1.0.0";
              src = ./.;

              subPackages = [ "cmd/server" ];
              vendorHash = null;

              postInstall = ''
                if [ -f "$out/bin/server" ]; then
                  mv "$out/bin/server" "$out/bin/calendar"
                fi
              '';

              meta = with pkgs.lib; {
                description = "Minimalist time-blocking calendar with CalDAV, Rosé Pine design & Vim navigation";
                homepage = "https://github.com/bhr-tools/Calendar";
                license = licenses.mit;
                mainProgram = "calendar";
              };
            };
            default = calendar;
          };

          apps = rec {
            calendar = flake-utils.lib.mkApp {
              drv = self.packages.${system}.calendar;
              name = "calendar";
            };
            default = calendar;
          };

          devShells.default = pkgs.mkShell {
            buildInputs = with pkgs; [
              go
              gopls
              sqlite
              nodejs
            ];
          };
        }
      );

      # Native multi-instance NixOS module for server / desktop
      nixosModule = { config, lib, pkgs, ... }:
        let
          cfg = config.services.calendar;
        in
        {
          options.services.calendar = {
            enable = lib.mkEnableOption "Calendar server (web app and CalDAV)";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
              description = "The Calendar package to use.";
            };

            instances = lib.mkOption {
              type = lib.types.attrsOf (lib.types.submodule ({ name, config, ... }: {
                options = {
                  port = lib.mkOption {
                    type = lib.types.port;
                    description = "HTTP listening port for this Calendar instance.";
                  };

                  host = lib.mkOption {
                    type = lib.types.str;
                    default = "127.0.0.1";
                    description = "IP address on which the instance listens.";
                  };

                  dataDir = lib.mkOption {
                    type = lib.types.path;
                    default = if (name == "default" || name == "perso") then "/var/lib/calendar" else "/var/lib/calendar-${name}";
                    description = "Data directory of the Calendar instance.";
                  };

                  dbPath = lib.mkOption {
                    type = lib.types.path;
                    default = "${config.dataDir}/calendar.db";
                    description = "Path to the SQLite database for this instance.";
                  };

                  timezone = lib.mkOption {
                    type = lib.types.str;
                    default = "Europe/Paris";
                    description = "Time zone of events written without one (floating times).";
                  };

                  refresh = lib.mkOption {
                    type = lib.types.str;
                    default = "30m";
                    description = "Interval between downloads of subscribed calendars (Go duration).";
                  };

                  noAuth = lib.mkOption {
                    type = lib.types.bool;
                    default = false;
                    description = "Disable password authentication (web app and CalDAV).";
                  };

                  user = lib.mkOption {
                    type = lib.types.str;
                    default = "calendar";
                    description = "System user under which to run the instance.";
                  };

                  group = lib.mkOption {
                    type = lib.types.str;
                    default = "calendar";
                    description = "System group under which to run the instance.";
                  };

                  openFirewall = lib.mkOption {
                    type = lib.types.bool;
                    default = false;
                    description = "Automatically open the port in the NixOS firewall.";
                  };
                };
              }));
              default = {};
              description = "Definition of independent Calendar instances.";
            };
          };

          config = lib.mkIf (cfg.enable && cfg.instances != {}) {
            networking.firewall.allowedTCPPorts = lib.concatLists (
              lib.mapAttrsToList (_: inst: lib.optional inst.openFirewall inst.port) cfg.instances
            );

            users.users = lib.mkIf (lib.any (inst: inst.user == "calendar") (lib.attrValues cfg.instances)) {
              calendar = {
                isSystemUser = true;
                group = "calendar";
                home = "/var/lib/calendar";
                createHome = true;
                description = "Calendar daemon user";
              };
            };

            users.groups = lib.mkIf (lib.any (inst: inst.group == "calendar") (lib.attrValues cfg.instances)) {
              calendar = {};
            };

            systemd.services = lib.mapAttrs' (name: inst:
              let
                serviceName = if name == "default" then "calendar" else "calendar-${name}";
              in
              lib.nameValuePair serviceName {
                description = "Calendar Server (${name})";
                after = [ "network-online.target" ];
                wants = [ "network-online.target" ];
                wantedBy = [ "multi-user.target" ];

                serviceConfig = {
                  ExecStart = "${cfg.package}/bin/calendar -host ${inst.host} -port ${toString inst.port} -data-dir ${toString inst.dataDir} -db ${toString inst.dbPath} -timezone ${inst.timezone} -refresh ${inst.refresh}"
                    + lib.optionalString inst.noAuth " -no-auth";
                  Restart = "always";
                  RestartSec = 3;
                  User = inst.user;
                  Group = inst.group;
                  WorkingDirectory = toString inst.dataDir;
                  ReadWritePaths = [ (toString inst.dataDir) ];
                  # Hardening: the service only needs its data directory and the
                  # network (subscriptions are downloaded from the internet).
                  NoNewPrivileges = true;
                  PrivateTmp = true;
                  PrivateDevices = true;
                  ProtectSystem = "strict";
                  ProtectHome = true;
                  ProtectKernelTunables = true;
                  ProtectKernelModules = true;
                  ProtectControlGroups = true;
                  RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ];
                  RestrictNamespaces = true;
                  LockPersonality = true;
                  MemoryDenyWriteExecute = true;
                  SystemCallArchitectures = "native";
                } // lib.optionalAttrs (lib.hasPrefix "/var/lib/" (toString inst.dataDir)) {
                  # systemd creates and owns the directory when it lives under /var/lib.
                  StateDirectory = lib.removePrefix "/var/lib/" (toString inst.dataDir);
                };
              }
            ) cfg.instances;
          };
        };
    in
    systemOutputs // {
      nixosModules.default = nixosModule;
      nixosModules.calendar = nixosModule;
      overlays.default = final: prev: {
        calendar = self.packages.${final.stdenv.hostPlatform.system}.default;
      };
    };
}
