#!/usr/bin/env bash
# Build the Hak5 bundles (static Go binary + init script + config + installer).
#   hak5/build.sh            -> dist/tak-gateway-<ver>-hak5-mipsel.tar.gz (Pager, Mark VII)
#                               dist/tak-gateway-<ver>-hak5-mips.tar.gz   (Tetra)
set -euo pipefail
cd "$(dirname "$0")/.."
VER=$(sed -n 's/^PKG_VERSION:=//p' tak-gateway/Makefile)
mkdir -p dist
for spec in mipsle:mipsel mips:mips; do
	goarch=${spec%%:*}
	name=${spec##*:}
	work=$(mktemp -d)
	mkdir -p "$work/tak-gateway"
	(cd tak-gateway/src && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOMIPS=softfloat \
		go build -trimpath -ldflags "-s -w -X main.version=$VER" -o "$work/tak-gateway/tak-gateway" .)
	cp tak-gateway/files/tak-gateway.init tak-gateway/files/tak-gateway.config hak5/install.sh "$work/tak-gateway/"
	tar -C "$work" -czf "dist/tak-gateway-$VER-hak5-$name.tar.gz" tak-gateway
	rm -rf "$work"
	echo "dist/tak-gateway-$VER-hak5-$name.tar.gz"
done
