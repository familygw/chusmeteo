#!/usr/bin/env bash
# Uso: ./build.sh <version> [--release]
#   ./build.sh v0.1.0             compila y empaqueta en dist/
#   ./build.sh v0.1.0 --release   además crea el release en GitHub con gh
# REPO=owner/nombre fuerza el repositorio destino (si no, gh usa el del directorio).
set -euo pipefail

cd "$(dirname "$0")"

VERSION="${1:-}"
RELEASE="${2:-}"
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.]+)?$ ]] || {
	echo "Uso: $0 vX.Y.Z [--release]" >&2
	exit 1
}

BIN=chusmeteo
PKG=./cmd/chusmeteo
DIST=dist
TARGETS=(darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64)

go vet ./...
go test ./...

rm -rf "$DIST"
mkdir -p "$DIST"

for t in "${TARGETS[@]}"; do
	os="${t%/*}"
	arch="${t#*/}"
	name="$BIN-$VERSION-$os-$arch"
	ext=""
	[[ "$os" == windows ]] && ext=".exe"

	echo "-> $name"
	stage="$DIST/$name"
	mkdir -p "$stage"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
		go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$stage/$BIN$ext" "$PKG"
	cp README.md "$stage/"

	if [[ "$os" == windows ]]; then
		(cd "$DIST" && zip -qr "$name.zip" "$name")
	else
		tar -C "$DIST" -czf "$DIST/$name.tar.gz" "$name"
	fi
	rm -rf "$stage"
done

(cd "$DIST" && shasum -a 256 *.tar.gz *.zip >checksums.txt)
echo "Artefactos en $DIST/"

if [[ "$RELEASE" == "--release" ]]; then
	command -v gh >/dev/null || { echo "Falta gh (https://cli.github.com)" >&2; exit 1; }
	repo_args=()
	[[ -n "${REPO:-}" ]] && repo_args=(--repo "$REPO")
	gh release create "$VERSION" "$DIST"/*.tar.gz "$DIST"/*.zip "$DIST"/checksums.txt \
		"${repo_args[@]}" --title "$VERSION" --generate-notes
fi
