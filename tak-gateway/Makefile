# SPDX-License-Identifier: MIT
include $(TOPDIR)/rules.mk

PKG_NAME:=tak-gateway
PKG_VERSION:=0.1.0
PKG_RELEASE:=1

PKG_LICENSE:=MIT
PKG_MAINTAINER:=SECT0101

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/SECT0101/tak-gateway
GO_PKG_LDFLAGS_X:=main.version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/tak-gateway
  SECTION:=net
  CATEGORY:=Network
  TITLE:=TAK gateway - bridges TAK SA multicast and a TAK Server
  DEPENDS:=$(GO_ARCH_DEPENDS)
endef

define Package/tak-gateway/description
  Puts the router on the TAK map and connects local ATAK/iTAK/WinTAK users
  to a TAK Server: relays SA and GeoChat multicast between LAN/mesh networks
  and the server over TLS (client certificate) or TCP, de-duplicates traffic,
  decodes TAK Protocol v1 mesh packets, and reports the router's own position
  from a fixed location or gpsd. Imports TAK Server data packages.
endef

define Package/tak-gateway/conffiles
/etc/config/tak-gateway
/etc/tak-gateway/
endef

define Build/Prepare
	$(INSTALL_DIR) $(PKG_BUILD_DIR)
	$(CP) ./src/* $(PKG_BUILD_DIR)/
	rm -f $(PKG_BUILD_DIR)/*_test.go
endef

define Package/tak-gateway/install
	$(call GoPackage/Package/Install/Bin,$(PKG_INSTALL_DIR))
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_INSTALL_DIR)/usr/bin/tak-gateway $(1)/usr/bin/
	$(INSTALL_DIR) $(1)/etc/init.d $(1)/etc/config $(1)/etc/tak-gateway
	$(INSTALL_BIN) ./files/tak-gateway.init $(1)/etc/init.d/tak-gateway
	$(INSTALL_CONF) ./files/tak-gateway.config $(1)/etc/config/tak-gateway
endef

$(eval $(call GoBinPackage,tak-gateway))
$(eval $(call BuildPackage,tak-gateway))
