#!/usr/bin/env bash
set -euo pipefail

version="$HOOVERSION_VERSION"
if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-+][0-9A-Za-z.-]+)?$ ]]; then
  echo "::error::Hooversion version must be an unprefixed semantic version."
  exit 2
fi

case "$RUNNER_OS_VALUE" in
  Linux) os=linux ;;
  macOS) os=darwin ;;
  Windows) os=windows ;;
  *)
    echo "::error::Hooversion does not publish binaries for runner.os '${RUNNER_OS_VALUE}'."
    exit 1
    ;;
esac
case "$RUNNER_ARCH_VALUE" in
  X64) arch=amd64 ;;
  ARM64) arch=arm64 ;;
  *)
    echo "::error::Hooversion does not publish binaries for runner.arch '${RUNNER_ARCH_VALUE}'."
    exit 1
    ;;
esac
if [[ "$os" == windows && "$arch" != amd64 ]]; then
  echo "::error::Hooversion does not publish Windows ARM64 binaries."
  exit 2
fi

archive_name="hooversion_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/openhoo/hooversion/releases/download/v${version}"
signature_identity="https://github.com/openhoo/hooversion/.github/workflows/release.yml@refs/heads/main"
signature_issuer="https://token.actions.githubusercontent.com"
download_dir="$(mktemp -d "${RUNNER_TEMP}/hooversion-download.XXXXXXXX")"
archive="${download_dir}/${archive_name}"
checksums="${download_dir}/SHA256SUMS"
archive_bundle="${archive}.sigstore.json"
checksums_bundle="${checksums}.sigstore.json"
curl --fail --location --silent --show-error --retry 3 --connect-timeout 30 --output "$archive" "${base_url}/${archive_name}"
curl --fail --location --silent --show-error --retry 3 --connect-timeout 30 --output "$checksums" "${base_url}/SHA256SUMS"
curl --fail --location --silent --show-error --retry 3 --connect-timeout 30 --output "$archive_bundle" "${base_url}/${archive_name}.sigstore.json"
curl --fail --location --silent --show-error --retry 3 --connect-timeout 30 --output "$checksums_bundle" "${base_url}/SHA256SUMS.sigstore.json"

if ! command -v cosign >/dev/null 2>&1; then
  echo "::error::Pinned Cosign verifier is unavailable."
  exit 1
fi
cosign verify-blob "$archive" --bundle "$archive_bundle" \
  --certificate-identity "$signature_identity" --certificate-oidc-issuer "$signature_issuer"
cosign verify-blob "$checksums" --bundle "$checksums_bundle" \
  --certificate-identity "$signature_identity" --certificate-oidc-issuer "$signature_issuer"

expected="$(awk -v name="$archive_name" '$2 == name { print $1 }' "$checksums")"
if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
  echo "::error::SHA256SUMS contains no unique digest for ${archive_name}."
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$archive" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "$archive" | awk '{ print $1 }')"
fi
if [[ "$actual" != "$expected" ]]; then
  echo "::error::Checksum mismatch for ${archive_name}."
  exit 1
fi

bin_dir="$(mktemp -d "${RUNNER_TEMP}/hooversion-bin.XXXXXXXX")"
tar -xzf "$archive" -C "$bin_dir"
if [[ "$os" == windows ]]; then
  binary="${bin_dir}/hooversion.exe"
else
  binary="${bin_dir}/hooversion"
fi
if [[ ! -f "$binary" ]]; then
  echo "::error::Archive ${archive_name} contains no Hooversion binary at the expected path."
  exit 1
fi
chmod +x "$binary"
"$binary" version | grep -F "hooversion ${version}"
echo "$bin_dir" >> "$GITHUB_PATH"
echo "version=${version}" >> "$GITHUB_OUTPUT"
