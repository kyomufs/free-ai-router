# free-ai-router — the package, as a standalone derivation.
#
# This is what the NixOS module builds when no package is supplied. It is kept
# beside the module rather than inside it so that the same file can be used from
# a plain `nix build`, a shell, or an overlay.
#
# UPDATING. `version`, `hash` and `vendorHash` move together:
#
#   version = "1.2.0";
#   # then:
#   nix-prefetch-url --unpack \
#     https://github.com/kyomufs/free-ai-router/archive/refs/tags/v1.2.0.tar.gz
#   nix-hash --to-sri --type sha256 <the base32 output>
#   # and vendorHash, if the dependencies changed:
#   nix build .#free-ai-router 2>&1 | grep 'got:'
#
# The `rev` is a tag rather than a commit on purpose: a name a human can check
# against the release list. The hashes are what make the build reproducible —
# without them Nix would have to trust whatever the network returned.
{ lib
, buildGoModule
, fetchFromGitHub
}:

buildGoModule rec {
  pname = "free-ai-router";
  version = "1.2.0";

  src = fetchFromGitHub {
    owner = "kyomufs";
    repo = "free-ai-router";
    rev = "v${version}";
    hash = "sha256-RrYy8vL9NtSmwNlYjewuT6Gza4bLEkJ6ZkEkMZmYPxk=";
  };

  # A static binary, so the unit needs neither LD_LIBRARY_PATH nor a wrapper, and
  # the build works on a machine with no C toolchain at all.
  env.CGO_ENABLED = "0";

  # The version goes into the binary. Without it `version` answers "unknown", and
  # a bug report becomes a guessing game about what exactly is running.
  ldflags = [ "-s" "-w" "-X" "main.buildVersion=${version}" ];

  # The Bubble Tea stack that the built-in monitor uses. The hash is mandatory:
  # without it the build would reach the network, which a Nix build must not do.
  vendorHash = "sha256-qpcRouxzBsHE/fSMvwAeuYYq8mRIAzPpsiVfE77z8Y8=";

  # The test suite runs during the build. It is hermetic — no test reaches the
  # network — so this costs nothing but the time, and it means a broken release
  # fails here rather than on the machine that installs it.
  doCheck = true;

  meta = {
    description = "OpenAI-compatible gateway aggregating free AI providers";
    homepage = "https://github.com/kyomufs/free-ai-router";
    license = lib.licenses.mit;
    mainProgram = "free-ai-router";
    platforms = lib.platforms.unix;
  };
}
