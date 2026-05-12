VERSION ?= 0.1.0
DIST    := dist
LDFLAGS := -s -w -X main.version=$(VERSION)

# Targets: GOOS/GOARCH pairs
TARGETS := \
	linux/amd64 \
	linux/arm64 \
	darwin/amd64 \
	darwin/arm64

.PHONY: all client server clean checksums release

all: client server checksums

# -------- Client (1master) cross-builds --------
client: $(addprefix $(DIST)/, $(addsuffix /1master, $(TARGETS)))

$(DIST)/%/1master:
	@mkdir -p $(@D)
	GOOS=$(word 1, $(subst /, ,$*)) GOARCH=$(word 2, $(subst /, ,$*)) \
		CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" \
		-o $@ ./client
	@echo "  built $@"

# -------- Server (1master-server) cross-builds --------
server: $(addprefix $(DIST)/, $(addsuffix /1master-server, $(TARGETS)))

$(DIST)/%/1master-server:
	@mkdir -p $(@D)
	GOOS=$(word 1, $(subst /, ,$*)) GOARCH=$(word 2, $(subst /, ,$*)) \
		CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" \
		-o $@ ./server
	@echo "  built $@"

# -------- Flat artifact layout for the installer --------
# Produces dist/1master-<os>-<arch> alongside the per-target dirs.
checksums: client server
	@cd $(DIST) && \
	for t in $(TARGETS); do \
		os=$$(echo $$t | cut -d/ -f1); arch=$$(echo $$t | cut -d/ -f2); \
		cp $$os/$$arch/1master 1master-$$os-$$arch; \
		cp $$os/$$arch/1master-server 1master-server-$$os-$$arch; \
	done && \
	shasum -a 256 1master-* > SHA256SUMS && \
	echo "  wrote $(DIST)/SHA256SUMS"

release: clean all
	@echo "Release $(VERSION) built in $(DIST)/"
	@ls -lh $(DIST)/

clean:
	rm -rf $(DIST)
