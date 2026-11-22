# SPDX-License-Identifier: MIT
include $(TOPDIR)/rules.mk

LUCI_TITLE:=LuCI page for the TAK gateway
LUCI_DESCRIPTION:=Configure tak-gateway, import TAK Server data packages and see local TAK users.
LUCI_DEPENDS:=+tak-gateway +openssl-util
LUCI_PKGARCH:=all

PKG_LICENSE:=MIT
PKG_MAINTAINER:=SECT0101

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
