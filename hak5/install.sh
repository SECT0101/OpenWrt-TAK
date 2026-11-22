#!/bin/sh
# Install tak-gateway on a Hak5 WiFi Pineapple (Pager, Mark VII or Tetra).
# Run from the unpacked bundle:  sh install.sh
set -e
cd "$(dirname "$0")"

# make sure this bundle matches the CPU (little-endian Pager/MK7 vs big-endian Tetra)
elf_endian() { dd if="$1" bs=1 skip=5 count=1 2>/dev/null | od -An -tu1 | tr -d ' '; }
if [ "$(elf_endian /bin/busybox)" != "$(elf_endian ./tak-gateway)" ]; then
	echo "This bundle is for a different CPU. Use the mipsel bundle on the Pager / Mark VII and the mips bundle on the Tetra." >&2
	exit 1
fi

cp tak-gateway /usr/bin/tak-gateway && chmod 755 /usr/bin/tak-gateway
cp tak-gateway.init /etc/init.d/tak-gateway && chmod 755 /etc/init.d/tak-gateway
if [ -f /etc/config/tak-gateway ]; then
	echo "Keeping your existing /etc/config/tak-gateway"
else
	cp tak-gateway.config /etc/config/tak-gateway
fi
mkdir -p /etc/tak-gateway
/etc/init.d/tak-gateway enable

echo
/usr/bin/tak-gateway version
cat <<'EOF'

Installed. Next steps (all on this device):

  1. Optional: import your TAK Server data package (needs the openssl command):
       tak-gateway import /tmp/your-data-package.zip
     It prints the server address and the certificate files it wrote.

  2. Set it up, for example:
       uci set tak-gateway.main.enabled=1
       uci set tak-gateway.main.callsign=PINE-PAGER
       uci set tak-gateway.main.gpsd=1                      # Pager GPS through gpsd
       uci set tak-gateway.server.enabled=1
       uci set tak-gateway.server.host=tak.example.org
       uci commit tak-gateway

  3. Start it:   /etc/init.d/tak-gateway restart
     Status:     cat /var/run/tak-gateway.json
     Log:        logread -e tak-gateway
EOF
